package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Team invites — the consent-based replacement for claiming an account by
// user_id.
//
// The addressable user_id was the whole problem: no screen shows it to the
// invitee, the owner's user list only contains people already seated, and a
// sequential integer let any owner claim a freshly registered account and then
// use it (updateUserRole answers to agent_teams membership). An invite turns
// "I know your id" into "you accepted my link": the owner mints a one-time
// token, the invitee accepts it while logged in, and only the caller's own
// user_id is bound. Nothing about the invitee is guessable, and the seat is
// still gated by the plan at acceptance.
//
// Only the SHA-256 of the token is stored, so the link is shown once at
// creation (same contract as the API-key surface) and a database dump does not
// hand out working links.
const (
	teamInviteTTL = 7 * 24 * time.Hour

	sqlInsertTeamInvite = "INSERT INTO team_invites (owner_user_id, token_hash, display_name, skills, expires_at) " +
		"VALUES ($1,$2,$3,$4::text[], NOW() + make_interval(secs => $5)) RETURNING invite_id, expires_at"

	// Pending = unused and unexpired. Used rows stay behind as the record of who
	// joined through which link; this list is only what is still actionable.
	sqlPendingTeamInvites = "SELECT invite_id, display_name, skills, expires_at, created_at FROM team_invites " +
		"WHERE owner_user_id = $1 AND used_by_user_id IS NULL AND expires_at > NOW() ORDER BY invite_id DESC"

	sqlFindTeamInvite = "SELECT invite_id, owner_user_id, display_name, skills FROM team_invites " +
		"WHERE token_hash = $1 AND used_by_user_id IS NULL AND expires_at > NOW()"

	// The claim is a single guarded UPDATE: two accepts racing on one link can
	// both pass sqlFindTeamInvite, and only one of them may see a row here.
	sqlClaimTeamInvite = "UPDATE team_invites SET used_by_user_id = $1, used_at = NOW() " +
		"WHERE invite_id = $2 AND used_by_user_id IS NULL AND expires_at > NOW()"

	sqlRevokeTeamInvite = "DELETE FROM team_invites WHERE invite_id = $1 AND owner_user_id = $2 AND used_by_user_id IS NULL"
)

// generateTeamInviteToken mirrors generateWidgetToken: a random, prefix-tagged
// handle whose only job is to be unguessable.
func generateTeamInviteToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("ti_%x", time.Now().UnixNano())
	}
	return "ti_" + hex.EncodeToString(b)
}

func teamInviteHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (a *App) teamInviteURL(token string) string {
	return a.widgetEmbedSrc() + "/join?code=" + url.QueryEscape(token)
}

type createTeamInviteRequest struct {
	DisplayName string   `json:"display_name"`
	Skills      []string `json:"skills"`
}

// createTeamInvite — POST /api/v1/team/invites. Owner only. Seats are checked
// here so an owner at the cap gets the upgrade message immediately instead of
// generating a link that cannot be accepted.
func (a *App) createTeamInvite(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if !a.isTenantOwner(r.Context(), user) {
		return nil, ErrForbidden("只有租户所有者可以邀请客服")
	}
	var req createTeamInviteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if len([]rune(req.DisplayName)) > 100 {
		return nil, ErrBadRequest("客服称呼过长")
	}
	if len(req.Skills) > 20 {
		return nil, ErrBadRequest("技能标签过多")
	}
	if req.Skills == nil {
		req.Skills = []string{}
	}
	if err := a.checkSeatLimit(r.Context(), user.UserID); err != nil {
		return nil, err
	}

	token := generateTeamInviteToken()
	var inviteID int64
	var expires time.Time
	if err := a.DB.QueryRow(r.Context(), sqlInsertTeamInvite,
		user.UserID, teamInviteHash(token), req.DisplayName, req.Skills,
		int(teamInviteTTL.Seconds())).Scan(&inviteID, &expires); err != nil {
		return nil, ErrInternal("生成邀请失败")
	}
	return map[string]any{
		"invite_id":  inviteID,
		"url":        a.teamInviteURL(token),
		"expires_at": expires,
	}, nil
}

// listTeamInvites — GET /api/v1/team/invites. Owner only. The link itself is
// not returned: only its hash is stored, so a lost link is revoked and reissued.
func (a *App) listTeamInvites(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if !a.isTenantOwner(r.Context(), user) {
		return nil, ErrForbidden("只有租户所有者可以查看邀请")
	}
	rows, err := a.DB.Query(r.Context(), sqlPendingTeamInvites, user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var displayName string
		var skills []string
		var expires, created time.Time
		if err := rows.Scan(&id, &displayName, &skills, &expires, &created); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"invite_id": id, "display_name": displayName, "skills": skills,
			"expires_at": expires, "created_at": created,
		})
	}
	// A short read must not be published as "no pending invites".
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

// deleteTeamInvite — DELETE /api/v1/team/invites/{id}. Owner only; only a
// pending invite can be revoked (a used one is a record, not a control).
func (a *App) deleteTeamInvite(w http.ResponseWriter, r *http.Request, inviteID int32) (any, error) {
	user, _ := UserFrom(r)
	if !a.isTenantOwner(r.Context(), user) {
		return nil, ErrForbidden("只有租户所有者可以撤销邀请")
	}
	tag, err := a.DB.Exec(r.Context(), sqlRevokeTeamInvite, inviteID, user.UserID)
	if err != nil {
		return nil, ErrInternal("撤销失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("邀请不存在或已被接受")
	}
	return map[string]string{"message": "已撤销"}, nil
}

type acceptTeamInviteRequest struct {
	Code string `json:"code"`
}

// acceptTeamInvite — POST /api/v1/team/invites/accept. Any authenticated user.
//
// The caller is the target: this endpoint can only ever bind the authenticated
// account itself, which is the property the old user_id form lacked. The whole
// thing runs in one transaction with the owner row locked, because the seat
// count and the invite claim have to be decided together — a rejected accept
// must leave the link usable (rollback), and two accepts on the last seat must
// not both win.
func (a *App) acceptTeamInvite(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req acceptTeamInviteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	code := strings.TrimSpace(req.Code)
	if code == "" || len(code) > 64 || !strings.HasPrefix(code, "ti_") {
		return nil, ErrNotFound("邀请链接无效、已被使用或已过期")
	}
	if user.IsPlatformAdmin() {
		return nil, ErrForbidden("平台管理员账号不能作为客服加入")
	}
	ctx := r.Context()

	var inviteID int64
	var ownerID int32
	var displayName string
	var skills []string
	err := a.DB.QueryRow(ctx, sqlFindTeamInvite, teamInviteHash(code)).
		Scan(&inviteID, &ownerID, &displayName, &skills)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound("邀请链接无效、已被使用或已过期")
	}
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	if ownerID == user.UserID {
		return nil, ErrBadRequest("不能接受自己的邀请")
	}
	// An account that already has its own tenant state cannot become someone's
	// agent: agent_teams membership is the tenant boundary for updateUserRole,
	// and the owner would gain role/status authority over data that is not
	// theirs. Same refusal as the platform break-glass path in addTeamAgent.
	var isTenant bool
	if err := a.DB.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM tenant_billing b WHERE b.user_id = $1) "+
			"  OR EXISTS (SELECT 1 FROM platform_configs pc WHERE pc.user_id = $1) "+
			"  OR EXISTS (SELECT 1 FROM sessions ss WHERE ss.user_id = $1) "+
			"  OR EXISTS (SELECT 1 FROM knowledge_documents kd WHERE kd.uploaded_by = $1)",
		user.UserID).Scan(&isTenant); err != nil {
		return nil, ErrInternal("查询失败")
	}
	if isTenant {
		return nil, ErrForbidden("该账号是独立的商家账号，不能作为客服加入")
	}

	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return nil, ErrInternal("加入失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize seat decisions for this tenant: without the lock, two accepts
	// for the last seat could both read used < allowance and both insert.
	if _, err := tx.Exec(ctx, "SELECT 1 FROM users WHERE user_id = $1 FOR UPDATE", ownerID); err != nil {
		return nil, ErrInternal("加入失败")
	}
	if err := a.checkSeatLimitFrom(ctx, tx, ownerID); err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, sqlClaimTeamInvite, user.UserID, inviteID)
	if err != nil {
		return nil, ErrInternal("加入失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("邀请链接无效、已被使用或已过期")
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO agent_teams (owner_user_id, agent_user_id, display_name, skills, is_active) VALUES ($1,$2,$3,$4::text[],true)",
		ownerID, user.UserID, displayName, skills); err != nil {
		var pge *pgconn.PgError
		if errors.As(err, &pge) && pge.Code == "23505" {
			// 059's uq_agent_teams_agent_user_id: one account, one team.
			return nil, ErrConflict("该账号已在客服团队中")
		}
		return nil, ErrInternal("加入失败")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, ErrInternal("加入失败")
	}
	return map[string]any{"message": "已加入", "owner_user_id": ownerID}, nil
}
