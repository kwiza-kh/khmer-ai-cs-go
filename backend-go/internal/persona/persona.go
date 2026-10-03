// Package persona resolves which instruction set a turn runs under.
//
// A tenant used to have one system prompt (plus the versioned history in
// migration 063). This package adds named personalities and the three-level
// resolution AstrBot uses (astrbot/core/persona/persona_mgr.py): a session
// binding wins, then a conversation binding, then the tenant's global default.
//
// The resolution is pure so the precedence is testable without a database; the
// store below is the only part that touches SQL.
package persona

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Scopes, most specific first. The order of this slice IS the precedence.
var Scopes = []string{"session", "conversation", "global"}

// Persona is a named instruction set for the model.
type Persona struct {
	ID           string
	Name         string
	SystemPrompt string
	// BeginDialogs are opening turns placed ahead of the real history. They are
	// not part of the system prompt: the model has to see them as things already
	// said, which is why they are copied into the request rather than merged into
	// the instructions.
	BeginDialogs []string
	// Tools is nil for "every tool", empty for "no tools", otherwise a whitelist.
	Tools []string
	// ErrorReply is shown to the customer when the turn fails.
	ErrorReply string
}

// Binding points a persona at a scope target.
type Binding struct {
	PersonaID string
	Scope     string
	// Target is the conversation or session id; empty for the global scope.
	Target string
}

// Resolve returns the binding that applies to a turn, most specific first.
//
// sessionID wins over conversationID, which wins over the tenant default — so an
// operator can switch one live conversation to a different personality without
// touching the other conversations of that tenant.
func Resolve(bindings []Binding, sessionID, conversationID string) (Binding, bool) {
	for _, want := range []struct {
		scope  string
		target string
	}{
		{"session", sessionID},
		{"conversation", conversationID},
		{"global", ""},
	} {
		if want.scope != "global" && strings.TrimSpace(want.target) == "" {
			continue
		}
		for _, b := range bindings {
			if b.Scope != want.scope {
				continue
			}
			if want.scope == "global" {
				return b, true
			}
			if b.Target == want.target {
				return b, true
			}
		}
	}
	return Binding{}, false
}

// Store reads personas and bindings.
type Store struct{ DB *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{DB: db} }

const personaCols = "persona_id, name, system_prompt, begin_dialogs, tools, error_reply"

// Get loads one persona by id.
func (s *Store) Get(ctx context.Context, personaID string) (Persona, bool, error) {
	if s.DB == nil {
		return Persona{}, false, nil
	}
	p, err := scanPersona(s.DB.QueryRow(ctx, "SELECT "+personaCols+" FROM personas WHERE persona_id = $1", personaID))
	if err == pgx.ErrNoRows {
		return Persona{}, false, nil
	}
	if err != nil {
		return Persona{}, false, err
	}
	return p, true, nil
}

// scanner is satisfied by both pgx.Row and pgx.Rows, so the column list and the
// JSON decoding live in exactly one place.
type scanner interface{ Scan(dest ...any) error }

func scanPersona(row scanner) (Persona, error) {
	var (
		p        Persona
		beginRaw []byte
		toolsRaw []byte
	)
	if err := row.Scan(&p.ID, &p.Name, &p.SystemPrompt, &beginRaw, &toolsRaw, &p.ErrorReply); err != nil {
		return Persona{}, err
	}
	p.BeginDialogs = decodeStrings(beginRaw)
	if len(toolsRaw) > 0 {
		// Distinguish "no tools" ([]) from "every tool" (NULL): a nil slice means
		// NULL here, an empty non-nil slice means [].
		p.Tools = decodeStrings(toolsRaw)
		if p.Tools == nil {
			p.Tools = []string{}
		}
	}
	return p, nil
}

// List returns every persona of one tenant, ordered by name then id so the
// console's list does not reshuffle between reads.
func (s *Store) List(ctx context.Context, userID int32) ([]Persona, error) {
	if s.DB == nil {
		return nil, nil
	}
	rows, err := s.DB.Query(ctx,
		"SELECT "+personaCols+" FROM personas WHERE user_id = $1 ORDER BY name, persona_id", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Persona
	for rows.Next() {
		p, err := scanPersona(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Create inserts a persona for one tenant. A duplicate persona_id is an error
// rather than an overwrite, so a colliding id can never rewrite another
// tenant's persona in place.
func (s *Store) Create(ctx context.Context, userID int32, p Persona) error {
	if s.DB == nil {
		return errors.New("persona: no database")
	}
	_, err := s.DB.Exec(ctx,
		"INSERT INTO personas (persona_id, user_id, name, system_prompt, begin_dialogs, tools, error_reply) "+
			"VALUES ($1,$2,$3,$4,$5,$6,$7)",
		p.ID, userID, p.Name, p.SystemPrompt, encodeDialogs(p.BeginDialogs), encodeTools(p.Tools), p.ErrorReply)
	return err
}

// Update rewrites one persona of one tenant. false means it is not this tenant's.
func (s *Store) Update(ctx context.Context, userID int32, p Persona) (bool, error) {
	if s.DB == nil {
		return false, nil
	}
	tag, err := s.DB.Exec(ctx,
		"UPDATE personas SET name=$1, system_prompt=$2, begin_dialogs=$3, tools=$4, error_reply=$5, updated_at=NOW() "+
			"WHERE persona_id=$6 AND user_id=$7",
		p.Name, p.SystemPrompt, encodeDialogs(p.BeginDialogs), encodeTools(p.Tools), p.ErrorReply, p.ID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Delete removes one persona. Its bindings cascade (067_personas.sql).
func (s *Store) Delete(ctx context.Context, userID int32, personaID string) (bool, error) {
	if s.DB == nil {
		return false, nil
	}
	tag, err := s.DB.Exec(ctx, "DELETE FROM personas WHERE persona_id=$1 AND user_id=$2", personaID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SetBinding upserts one binding, and reports false when the persona is not this
// tenant's.
//
// The write is an INSERT ... SELECT over personas rather than a VALUES clause, so
// the ownership check and the write are one statement: a separate read would
// leave a window in which the persona is deleted between the check and the
// insert, and would let tenant A bind tenant B's persona by guessing its id.
// Target is normalised to "" for the global scope, exactly as Resolve reads it.
func (s *Store) SetBinding(ctx context.Context, userID int32, b Binding) (bool, error) {
	if s.DB == nil {
		return false, nil
	}
	target := b.Target
	if b.Scope == "global" {
		target = ""
	}
	tag, err := s.DB.Exec(ctx,
		"INSERT INTO persona_bindings (user_id, persona_id, scope, target) "+
			"SELECT $1, persona_id, $3, $4 FROM personas WHERE persona_id = $2 AND user_id = $1 "+
			"ON CONFLICT (user_id, scope, target) DO UPDATE SET persona_id = EXCLUDED.persona_id",
		userID, b.PersonaID, b.Scope, target)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ClearBinding removes one binding. false means there was nothing to remove.
func (s *Store) ClearBinding(ctx context.Context, userID int32, scope, target string) (bool, error) {
	if s.DB == nil {
		return false, nil
	}
	if scope == "global" {
		target = ""
	}
	tag, err := s.DB.Exec(ctx,
		"DELETE FROM persona_bindings WHERE user_id=$1 AND scope=$2 AND target=$3", userID, scope, target)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// encodeDialogs always writes a JSON array, so a persona with no opening turns
// reads back as [] rather than NULL.
func encodeDialogs(dialogs []string) []byte {
	b, err := json.Marshal(dialogs)
	if err != nil || len(b) == 0 {
		return []byte("[]")
	}
	if string(b) == "null" {
		return []byte("[]")
	}
	return b
}

// encodeTools maps the three-valued Tools field onto the column: nil (every
// tool) is SQL NULL, an empty non-nil slice (no tools) is '[]'.
func encodeTools(tools []string) any {
	if tools == nil {
		return nil
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return nil
	}
	return b
}

// Bindings loads every binding for a tenant.
func (s *Store) Bindings(ctx context.Context, userID int32) ([]Binding, error) {
	if s.DB == nil {
		return nil, nil
	}
	rows, err := s.DB.Query(ctx, "SELECT persona_id, scope, target FROM persona_bindings WHERE user_id = $1", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Binding
	for rows.Next() {
		var b Binding
		if err := rows.Scan(&b.PersonaID, &b.Scope, &b.Target); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ForTurn resolves the persona that applies to one turn, or ok=false when the
// tenant has none (callers then keep the existing per-tenant system prompt).
func (s *Store) ForTurn(ctx context.Context, userID int32, sessionID, conversationID string) (Persona, bool, error) {
	bindings, err := s.Bindings(ctx, userID)
	if err != nil {
		return Persona{}, false, err
	}
	b, ok := Resolve(bindings, sessionID, conversationID)
	if !ok {
		return Persona{}, false, nil
	}
	p, found, err := s.Get(ctx, b.PersonaID)
	if err != nil {
		return Persona{}, false, err
	}
	if !found {
		return Persona{}, false, fmt.Errorf("persona: binding points at missing persona %q", b.PersonaID)
	}
	return p, true, nil
}

func decodeStrings(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
