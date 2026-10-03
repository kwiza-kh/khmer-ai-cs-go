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
	var (
		p        Persona
		beginRaw []byte
		toolsRaw []byte
	)
	err := s.DB.QueryRow(ctx, "SELECT "+personaCols+" FROM personas WHERE persona_id = $1", personaID).
		Scan(&p.ID, &p.Name, &p.SystemPrompt, &beginRaw, &toolsRaw, &p.ErrorReply)
	if err == pgx.ErrNoRows {
		return Persona{}, false, nil
	}
	if err != nil {
		return Persona{}, false, err
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
	return p, true, nil
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
