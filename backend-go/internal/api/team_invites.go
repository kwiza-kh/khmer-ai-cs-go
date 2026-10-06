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

	"khmer-ai-cs-go/internal/usage"
)

// Team invites — the consent-based replacement for claiming an account by
// user_id.
//
// The addressable user_id was the whole problem: no screen shows it to the
// invitee, the owner's user list only contains people already seated, and a
// sequential integer let any owner claim a freshly registered account and then
// use it (updateUserRole answers to agent_teams membership). An invite turns
// "I know your id" into "you accepted my link": the owner mints a token, the
// invitee accepts it while logged in, and only the caller's own user_id is
// bound.
//
// A link carries its own expiry and a cap on how many people may join through
// it (default 1), and every acceptance is written to team_invite_uses — which is
// the invite history: who joined, when, and through which link. The row is the
// record; revoking marks it instead of deleting it, so the log stays readable.
//
// Only the SHA-256 of the token is stored, so the link itself is shown once at
// creation (same contract as the API-key surface) and a database dump does not
// hand out working links. The history shows an 8-character fingerprint instead.
const (
	defaultInviteTTLHours = 7 * 24
	maxInviteTTLHours     = 24 * 90
	maxInviteUses         = 100

	sqlInsertTeamInvite = "INSERT INTO team_invites (owner_user_id, token_hash, display_name, skills, max_uses, expires_at) " +
		"VALUES ($1,$2,$3,$4::text[],$5, NOW() + make_interval(secs => $6)) RETURNING invite_id, expires_at"

	// Pending = not revoked, not expired, and not used up.
	sqlPendingTeamInvites = "SELECT i.invite_id, i.display_name, i.skills, i.expires_at, i.created_at, i.max_uses, COALESCE(x.cnt, 0) " +
		"FROM team_invites i " +
		"LEFT JOIN (SELECT invite_id, count(*) AS cnt FROM team_invite_uses GROUP BY invite_id) x ON x.invite_id = i.invite_id " +
		"WHERE i.owner_user_id = $1 AND i.revoked_at IS NULL AND i.expires_at > NOW() AND COALESCE(x.cnt, 0) < i.max_uses " +
		"ORDER BY i.invite_id DESC"

	sqlFindTeamInvite = "SELECT invite_id, owner_user_id, display_name, skills FROM team_invites " +
		"WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > NOW()"

	// The claim runs inside the acceptance transaction: FOR UPDATE serializes two
	// accepts racing for the last use of a multi-use link.
	sqlLockTeamInvite = "SELECT i.max_uses, (SELECT count(*) FROM team_invite_uses u WHERE u.invite_id = i.invite_id) " +
		"FROM team_invites i " +
		"WHERE i.invite_id = $1 AND i.revoked_at IS NULL AND i.expires_at > NOW() FOR UPDATE"

	sqlInsertTeamInviteUse = "INSERT INTO team_invite_uses (invite_id, user_id, username, display_name) VALUES ($1,$2,$3,$4)"

	// Revoking is a mark, not a delete: the history row (and its acceptances)
	// must survive so the owner can still see who joined through it.
	sqlRevokeTeamInvite = "UPDATE team_invites SET revoked_at = NOW() " +
		"WHERE invite_id = $1 AND owner_user_id = $2 AND revoked_at IS NULL"

	sqlInviteHistory = "SELECT i.invite_id, i.display_name, i.skills, i.max_uses, i.expires_at, i.created_at, i.revoked_at, i.token_hash, COALESCE(x.cnt, 0) " +
		"FROM team_invites i " +
		"LEFT JOIN (SELECT invite_id, count(*) AS cnt FROM team_invite_uses GROUP BY invite_id) x ON x.invite_id = i.invite_id " +
		"WHERE i.owner_user_id = $1 ORDER BY i.invite_id DESC LIMIT 100"

	sqlInviteUsesForIDs = "SELECT invite_id, COALESCE(username, ''), COALESCE(display_name, ''), used_at " +
		"FROM team_invite_uses WHERE invite_id = ANY($1::bigint[]) ORDER BY used_at"
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

// inviteStatus derives the row's state for the history view. Exhausted comes
// before expired so a link that was used up on day one does not read as merely
// "expired" a week later.
func inviteStatus(revokedAt *time.Time, expiresAt time.Time, useCount, maxUses int64, now time.Time) string {
	switch {
	case revokedAt != nil:
		return "revoked"
	case useCount >= maxUses:
		return "exhausted"
	case !now.Before(expiresAt):
		return "expired"
	default:
		return "pending"
	}
}

type createTeamInviteRequest struct {
	DisplayName string   `json:"display_name"`
	Skills      []string `json:"skills"`
	// ExpiresInHours is the link's validity; 0 means the 7-day default.
	ExpiresInHours int `json:"expires_in_hours"`
	// MaxUses caps how many people may join through this link; 0 means one.
	MaxUses int `json:"max_uses"`
}

// createTeamInvite — POST /api/v1/team/invites. Owner only.
//
// Seats are checked here so an owner at the cap gets the upgrade message
// immediately instead of minting a link that cannot be accepted, and the
// requested capacity is capped by the seats actually left.
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
	if req.ExpiresInHours == 0 {
		req.ExpiresInHours = defaultInviteTTLHours
	}
	if req.ExpiresInHours < 1 || req.ExpiresInHours > maxInviteTTLHours {
		return nil, ErrBadRequest("有效期设置无效")
	}
	if req.MaxUses == 0 {
		req.MaxUses = 1
	}
	if req.MaxUses < 1 || req.MaxUses > maxInviteUses {
		return nil, ErrBadRequest("可加入人数无效")
	}
	if err := a.checkSeatLimit(r.Context(), user.UserID); err != nil {
		return nil, err
	}
	// A link for more people than there are seats will just fail one acceptance
	// at a time; say so now. Unlimited plans skip the comparison.
	spec := a.tenantPlan(r.Context(), user.UserID)
	if spec.Seats < usage.Unlimited {
		var used int64
		if err := a.DB.QueryRow(r.Context(), sqlCountActiveSeats, user.UserID).Scan(&used); err == nil {
			if int64(req.MaxUses) > spec.Seats-used {
				return nil, ErrBadRequest("可加入人数超过剩余席位")
			}
		}
	}

	token := generateTeamInviteToken()
	var inviteID int64
	var expires time.Time
	if err := a.DB.QueryRow(r.Context(), sqlInsertTeamInvite,
		user.UserID, teamInviteHash(token), req.DisplayName, req.Skills, req.MaxUses,
		req.ExpiresInHours*3600).Scan(&inviteID, &expires); err != nil {
		return nil, ErrInternal("生成邀请失败")
	}
	return map[string]any{
		"invite_id":  inviteID,
		"url":        a.teamInviteURL(token),
		"expires_at": expires,
		"max_uses":   req.MaxUses,
	}, nil
}

// listTeamInvites — GET /api/v1/team/invites. Owner only: the links that are
// still actionable (pending, unexpired, with capacity left).
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
		var id, useCount, maxUses int64
		var displayName string
		var skills []string
		var expires, created time.Time
		if err := rows.Scan(&id, &displayName, &skills, &expires, &created, &maxUses, &useCount); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"invite_id": id, "display_name": displayName, "skills": skills,
			"expires_at": expires, "created_at": created,
			"max_uses": maxUses, "use_count": useCount,
		})
	}
	// A short read must not be published as "no pending invites".
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

// listTeamInviteHistory — GET /api/v1/team/invites/history. Owner only.
//
// Every link ever minted, newest first, with its state, its expiry, how many
// people joined through it and who they were (name snapshotted at acceptance).
// This is the audit trail; the pending list above is only the actionable slice.
func (a *App) listTeamInviteHistory(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if !a.isTenantOwner(r.Context(), user) {
		return nil, ErrForbidden("只有租户所有者可以查看邀请历史")
	}
	rows, err := a.DB.Query(r.Context(), sqlInviteHistory, user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()

	type historyRow struct {
		entry   map[string]any
		invite  int64
		maxUses int64
	}
	order := make([]historyRow, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var id, maxUses, useCount int64
		var displayName, tokenHash string
		var skills []string
		var expires, created time.Time
		var revokedAt *time.Time
		if err := rows.Scan(&id, &displayName, &skills, &maxUses, &expires, &created, &revokedAt, &tokenHash, &useCount); err != nil {
			continue
		}
		fingerprint := tokenHash
		if len(fingerprint) > 8 {
			fingerprint = fingerprint[:8]
		}
		order = append(order, historyRow{
			invite:  id,
			maxUses: maxUses,
			entry: map[string]any{
				"invite_id": id, "display_name": displayName, "skills": skills,
				"expires_at": expires, "created_at": created, "revoked_at": revokedAt,
				"status":      inviteStatus(revokedAt, expires, useCount, maxUses, time.Now()),
				"max_uses":    maxUses,
				"use_count":   useCount,
				"fingerprint": fingerprint,
				"invited":     []map[string]any{},
			},
		})
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}

	// One extra query for all the acceptances, grouped in memory — a link's
	// invitees are a handful of rows, and N+1 here would be pure waste.
	byInvite := make(map[int64][]map[string]any, len(ids))
	if len(ids) > 0 {
		useRows, err := a.DB.Query(r.Context(), sqlInviteUsesForIDs, ids)
		if err != nil {
			return nil, ErrInternal("查询失败")
		}
		defer useRows.Close()
		for useRows.Next() {
			var inviteID int64
			var username, displayName string
			var usedAt time.Time
			if err := useRows.Scan(&inviteID, &username, &displayName, &usedAt); err != nil {
				continue
			}
			byInvite[inviteID] = append(byInvite[inviteID], map[string]any{
				"username": username, "display_name": displayName, "used_at": usedAt,
			})
		}
		if err := useRows.Err(); err != nil {
			return nil, ErrInternal("查询失败")
		}
	}
	out := make([]map[string]any, 0, len(order))
	for _, row := range order {
		if uses, ok := byInvite[row.invite]; ok {
			row.entry["invited"] = uses
		}
		out = append(out, row.entry)
	}
	return out, nil
}

// deleteTeamInvite — DELETE /api/v1/team/invites/{id}. Owner only; revoking
// marks the row so the history keeps the link and everyone who joined.
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
		return nil, ErrNotFound("邀请不存在或已被撤销")
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
// thing runs in one transaction with the owner row and the invite row locked,
// because the seat count, the link's remaining capacity and the claim have to be
// decided together — a rejected accept must leave the link usable (rollback),
// and two accepts on the last use must not both win.
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

	// Lock order is owner row then invite row, always: two accepts for the same
	// tenant serialize, and two accepts on one link serialize on the link.
	if _, err := tx.Exec(ctx, "SELECT 1 FROM users WHERE user_id = $1 FOR UPDATE", ownerID); err != nil {
		return nil, ErrInternal("加入失败")
	}
	// The link's own state is answered first: an exhausted or revoked link cannot
	// be used whatever the plan says, so "buy more seats" would be the wrong
	// instruction. Capacity is counted under the row lock, which is what stops two
	// accepts from both taking the last use.
	var maxUses, useCount int64
	err = tx.QueryRow(ctx, sqlLockTeamInvite, inviteID).Scan(&maxUses, &useCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound("邀请链接无效、已被使用或已过期")
	}
	if err != nil {
		return nil, ErrInternal("加入失败")
	}
	if useCount >= maxUses {
		return nil, ErrConflict("邀请链接已用完")
	}
	if err := a.checkSeatLimitFrom(ctx, tx, ownerID); err != nil {
		return nil, err
	}
	// Snapshot the account as it is now: the log must stay readable after a
	// rename or an account deletion (user_id then goes NULL in the uses table).
	var username, memberName string
	if err := tx.QueryRow(ctx, "SELECT username, COALESCE(display_name,'') FROM users WHERE user_id = $1", user.UserID).
		Scan(&username, &memberName); err != nil {
		return nil, ErrInternal("加入失败")
	}
	if _, err := tx.Exec(ctx, sqlInsertTeamInviteUse, inviteID, user.UserID, username, memberName); err != nil {
		return nil, ErrInternal("加入失败")
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO agent_teams (owner_user_id, agent_user_id, display_name, skills, is_active) VALUES ($1,$2,$3,$4::text[],true)",
		ownerID, user.UserID, displayName, skills); err != nil {
		var pge *pgconn.PgError
		if errors.As(err, &pge) && pge.Code == "23505" {
			// 059's uq_agent_teams_agent_user_id: one account, one team. The
			// rollback also discards the use row, so the link keeps its capacity.
			return nil, ErrConflict("该账号已在客服团队中")
		}
		return nil, ErrInternal("加入失败")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, ErrInternal("加入失败")
	}
	return map[string]any{"message": "已加入", "owner_user_id": ownerID}, nil
}
