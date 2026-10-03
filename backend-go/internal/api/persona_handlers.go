// Package api — persona management.
//
// A persona is a named instruction set that replaces the tenant's system prompt
// for a turn. internal/persona holds the resolution (session → conversation →
// global) and the tables (migration 067); the inbound pipeline applies it in its
// resolve-persona stage. This file is the console's CRUD surface plus the binding
// editor.
//
// Bindings are addressed by (scope, target), never by binding_id: 067_personas.sql
// already makes that pair unique per tenant, so the console never has to carry
// back an id it never asked for, and a rebind is one PUT instead of a delete plus
// a create.
//
// Every endpoint here is a tenant-admin action (adminOnly), not a platform-admin
// one: a persona changes what the AI says to this merchant's customers, which is
// the merchant's call, not the operator's.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"khmer-ai-cs-go/internal/persona"
)

// Persona payload bounds. These are limits on what the model is handed verbatim
// on every turn, not style preferences: an unbounded system prompt is a request
// the provider rejects only after a full round trip, and an unbounded
// begin_dialogs list is unbounded input tokens billed to the merchant on every
// turn that persona is bound to.
const (
	maxPersonaBody        = 128 << 10
	maxPersonaName        = 80
	maxPersonaPrompt      = 32 << 10
	maxPersonaErrorReply  = 1 << 10
	maxPersonaDialogs     = 20
	maxPersonaDialog      = 4 << 10
	maxPersonaTools       = 50
	maxBindingTarget      = 200
	maxPersonaBindingBody = 8 << 10
)

type bindingJSON struct {
	Scope  string `json:"scope"`
	Target string `json:"target"`
}

type personaJSON struct {
	PersonaID    string        `json:"persona_id"`
	Name         string        `json:"name"`
	SystemPrompt string        `json:"system_prompt"`
	BeginDialogs []string      `json:"begin_dialogs"`
	Tools        []string      `json:"tools"`
	ErrorReply   string        `json:"error_reply"`
	Bindings     []bindingJSON `json:"bindings"`
}

type personaRequest struct {
	PersonaID    string   `json:"persona_id"`
	Name         string   `json:"name"`
	SystemPrompt string   `json:"system_prompt"`
	BeginDialogs []string `json:"begin_dialogs"`
	Tools        []string `json:"tools"`
	ErrorReply   string   `json:"error_reply"`
}

// bindingRequest doubles as the DELETE query shape: toPersona-less deletes only
// need the scope and the target.
type bindingRequest struct {
	PersonaID string `json:"persona_id"`
	Scope     string `json:"scope"`
	Target    string `json:"target"`
}

// listPersonas — every persona of the tenant plus the bindings that point at
// them, in one round trip. The console edits the two together, and a per-persona
// call would be an N+1 for a list that is never long.
func (a *App) listPersonas(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	ctx := r.Context()
	store := persona.NewStore(a.DB)
	list, err := store.List(ctx, user.UserID)
	if err != nil {
		return nil, ErrInternal("读取人格列表失败")
	}
	bindings, err := store.Bindings(ctx, user.UserID)
	if err != nil {
		return nil, ErrInternal("读取人格绑定失败")
	}
	out := make([]personaJSON, 0, len(list))
	for _, p := range list {
		out = append(out, personaToJSON(p, bindingsOf(bindings, p.ID)))
	}
	// Scopes travels with the payload so the binding editor offers exactly the
	// precedences the resolver implements, in order, without hard-coding them.
	return map[string]any{"personas": out, "scopes": persona.Scopes}, nil
}

// createPersona — POST /api/v1/personas. A missing persona_id is minted here.
func (a *App) createPersona(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	req, err := decodePersonaRequest(w, r)
	if err != nil {
		return nil, err
	}
	p, err := req.toPersona()
	if err != nil {
		return nil, err
	}
	if p.ID == "" {
		p.ID = mintPersonaID()
	}
	if err := persona.NewStore(a.DB).Create(r.Context(), user.UserID, p); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict("该人格 ID 已存在")
		}
		return nil, ErrInternal("创建人格失败")
	}
	return map[string]any{"persona": personaToJSON(p, nil)}, nil
}

// updatePersona — PUT /api/v1/personas/{id}. The path id wins over the body, so a
// console that echoes a stale body cannot rename the row it is editing.
func (a *App) updatePersona(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	req, err := decodePersonaRequest(w, r)
	if err != nil {
		return nil, err
	}
	req.PersonaID = strings.TrimSpace(r.PathValue("id"))
	p, err := req.toPersona()
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	store := persona.NewStore(a.DB)
	ok, err := store.Update(ctx, user.UserID, p)
	if err != nil {
		return nil, ErrInternal("保存人格失败")
	}
	if !ok {
		return nil, ErrNotFound("人格不存在")
	}
	bindings, err := store.Bindings(ctx, user.UserID)
	if err != nil {
		return nil, ErrInternal("读取人格绑定失败")
	}
	return map[string]any{"persona": personaToJSON(p, bindingsOf(bindings, p.ID))}, nil
}

// deletePersona — DELETE /api/v1/personas/{id}. The persona's bindings go with it
// (persona_bindings cascades on delete in 067_personas.sql), so a deleted persona
// cannot leave a dangling binding that makes every turn resolve to nothing.
func (a *App) deletePersona(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	ok, err := persona.NewStore(a.DB).Delete(r.Context(), user.UserID, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		return nil, ErrInternal("删除人格失败")
	}
	if !ok {
		return nil, ErrNotFound("人格不存在")
	}
	return map[string]any{"message": "已删除"}, nil
}

// putPersonaBinding — PUT /api/v1/persona-bindings. Rebinding a scope+target
// replaces the previous persona, which is what an operator toggling a personality
// on one conversation means.
func (a *App) putPersonaBinding(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req bindingRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPersonaBindingBody)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	scope, target, err := normalizeScopeTarget(req.Scope, req.Target)
	if err != nil {
		return nil, err
	}
	personaID := strings.TrimSpace(req.PersonaID)
	if personaID == "" {
		return nil, ErrBadRequest("必须指定 persona_id")
	}
	ok, err := persona.NewStore(a.DB).SetBinding(r.Context(), user.UserID,
		persona.Binding{PersonaID: personaID, Scope: scope, Target: target})
	if err != nil {
		return nil, ErrInternal("保存绑定失败")
	}
	if !ok {
		// SetBinding's INSERT ... SELECT finds no row when the persona is not this
		// tenant's, which is also what a bad id looks like. Both are a 404: telling
		// them apart would confirm another tenant's persona ids.
		return nil, ErrNotFound("人格不存在")
	}
	return map[string]any{"persona_id": personaID, "scope": scope, "target": target}, nil
}

// deletePersonaBinding — DELETE /api/v1/persona-bindings?scope=&target=. Removing
// a binding that is not there is success: the caller asked for a state, and that
// state now holds.
func (a *App) deletePersonaBinding(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	scope, target, err := normalizeScopeTarget(r.URL.Query().Get("scope"), r.URL.Query().Get("target"))
	if err != nil {
		return nil, err
	}
	if _, err := persona.NewStore(a.DB).ClearBinding(r.Context(), user.UserID, scope, target); err != nil {
		return nil, ErrInternal("解绑失败")
	}
	return map[string]any{"scope": scope, "target": target}, nil
}

func decodePersonaRequest(w http.ResponseWriter, r *http.Request) (*personaRequest, error) {
	var req personaRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPersonaBody)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	return &req, nil
}

// toPersona validates and normalises a payload into a persona.Persona. Every
// check here runs on data that reaches the model on every turn of the bound
// scope, so none of them are optional.
func (req *personaRequest) toPersona() (persona.Persona, error) {
	p := persona.Persona{
		ID:           strings.TrimSpace(req.PersonaID),
		Name:         strings.TrimSpace(req.Name),
		SystemPrompt: strings.TrimSpace(req.SystemPrompt),
		ErrorReply:   strings.TrimSpace(req.ErrorReply),
		Tools:        req.Tools,
	}
	if p.Name == "" || utf8.RuneCountInString(p.Name) > maxPersonaName {
		return persona.Persona{}, ErrBadRequest(fmt.Sprintf("人格名称不能为空，且不超过 %d 个字", maxPersonaName))
	}
	if p.SystemPrompt == "" || len(p.SystemPrompt) > maxPersonaPrompt {
		return persona.Persona{}, ErrBadRequest("系统提示词不能为空，且不超过 32KB")
	}
	if len(p.ErrorReply) > maxPersonaErrorReply {
		return persona.Persona{}, ErrBadRequest("失败提示不能超过 1KB")
	}
	if p.ID != "" && !validPersonaID(p.ID) {
		return persona.Persona{}, ErrBadRequest("人格 ID 只能包含字母、数字、下划线和连字符，长度 2-64")
	}
	if len(req.BeginDialogs) > maxPersonaDialogs {
		return persona.Persona{}, ErrBadRequest(fmt.Sprintf("开场对话最多 %d 条", maxPersonaDialogs))
	}
	for i, raw := range req.BeginDialogs {
		d := strings.TrimSpace(raw)
		if d == "" {
			return persona.Persona{}, ErrBadRequest(fmt.Sprintf("开场对话第 %d 条为空", i+1))
		}
		if len(d) > maxPersonaDialog {
			return persona.Persona{}, ErrBadRequest(fmt.Sprintf("开场对话第 %d 条超过 4KB", i+1))
		}
		p.BeginDialogs = append(p.BeginDialogs, d)
	}
	if len(p.Tools) > maxPersonaTools {
		return persona.Persona{}, ErrBadRequest(fmt.Sprintf("工具白名单最多 %d 项", maxPersonaTools))
	}
	for _, name := range p.Tools {
		if strings.TrimSpace(name) == "" {
			return persona.Persona{}, ErrBadRequest("工具白名单里有空项")
		}
	}
	return p, nil
}

// normalizeScopeTarget validates a binding address. A global binding carries no
// target (067_personas.sql defaults it to an empty string), and the two must agree, or the same
// binding would be addressable two ways.
func normalizeScopeTarget(scope, target string) (string, string, error) {
	scope = strings.TrimSpace(scope)
	valid := false
	for _, s := range persona.Scopes {
		if s == scope {
			valid = true
			break
		}
	}
	if !valid {
		return "", "", ErrBadRequest("scope 必须是 session、conversation 或 global")
	}
	target = strings.TrimSpace(target)
	if scope == "global" {
		return scope, "", nil
	}
	if target == "" {
		return "", "", ErrBadRequest("session/conversation 绑定必须给出目标 ID")
	}
	if len(target) > maxBindingTarget {
		return "", "", ErrBadRequest("绑定目标 ID 过长")
	}
	return scope, target, nil
}

func personaToJSON(p persona.Persona, bindings []bindingJSON) personaJSON {
	tools := p.Tools
	if tools == nil {
		tools = []string{}
	}
	dialogs := p.BeginDialogs
	if dialogs == nil {
		dialogs = []string{}
	}
	if bindings == nil {
		bindings = []bindingJSON{}
	}
	return personaJSON{
		PersonaID:    p.ID,
		Name:         p.Name,
		SystemPrompt: p.SystemPrompt,
		BeginDialogs: dialogs,
		Tools:        tools,
		ErrorReply:   p.ErrorReply,
		Bindings:     bindings,
	}
}

func bindingsOf(all []persona.Binding, personaID string) []bindingJSON {
	out := []bindingJSON{}
	for _, b := range all {
		if b.PersonaID == personaID {
			out = append(out, bindingJSON{Scope: b.Scope, Target: b.Target})
		}
	}
	return out
}

func validPersonaID(id string) bool {
	if len(id) < 2 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// mintPersonaID returns a fresh persona id. Random rather than slugged from the
// name: persona names are routinely Khmer or Chinese, and transliterating them
// collides in exactly the cases the console cannot explain to the operator.
func mintPersonaID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on the platforms this ships on; the fallback
		// keeps create working rather than failing the request outright.
		return fmt.Sprintf("p_%d", time.Now().UnixNano())
	}
	return "p_" + hex.EncodeToString(b[:])
}

func isUniqueViolation(err error) bool {
	var pge *pgconn.PgError
	return errors.As(err, &pge) && pge.Code == "23505"
}
