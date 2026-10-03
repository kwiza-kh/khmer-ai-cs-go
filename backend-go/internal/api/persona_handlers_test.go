package api

import (
	"strings"
	"testing"
)

// The persona payload checks are the only thing standing between an admin form
// and the exact text the model is handed on every turn of the bound scope, so
// they get a direct table test rather than a handler test that would need a
// database.
func TestPersonaRequestValidation(t *testing.T) {
	base := func() personaRequest {
		return personaRequest{
			Name:         "Tire desk",
			SystemPrompt: "You answer tyre questions.",
			BeginDialogs: []string{"hello", "Hi, how can I help?"},
		}
	}

	cases := []struct {
		name    string
		mutate  func(*personaRequest)
		wantErr string
	}{
		{"valid", func(*personaRequest) {}, ""},
		{"blank name", func(r *personaRequest) { r.Name = "   " }, "人格名称不能为空"},
		{"long name", func(r *personaRequest) { r.Name = strings.Repeat("好", maxPersonaName+1) }, "人格名称不能为空"},
		{"name at the limit", func(r *personaRequest) { r.Name = strings.Repeat("好", maxPersonaName) }, ""},
		{"blank prompt", func(r *personaRequest) { r.SystemPrompt = " " }, "系统提示词不能为空"},
		{"oversized prompt", func(r *personaRequest) { r.SystemPrompt = strings.Repeat("x", maxPersonaPrompt+1) }, "系统提示词不能为空"},
		{"oversized error reply", func(r *personaRequest) { r.ErrorReply = strings.Repeat("x", maxPersonaErrorReply+1) }, "失败提示不能超过"},
		{"bad persona id", func(r *personaRequest) { r.PersonaID = "has space" }, "人格 ID 只能包含"},
		{"one char persona id", func(r *personaRequest) { r.PersonaID = "p" }, "人格 ID 只能包含"},
		{"good persona id", func(r *personaRequest) { r.PersonaID = "p_9aF-_" }, ""},
		{"too many dialogs", func(r *personaRequest) {
			r.BeginDialogs = make([]string, maxPersonaDialogs+1)
			for i := range r.BeginDialogs {
				r.BeginDialogs[i] = "x"
			}
		}, "开场对话最多"},
		{"blank dialog", func(r *personaRequest) { r.BeginDialogs = []string{"hi", "  "} }, "开场对话第 2 条为空"},
		{"oversized dialog", func(r *personaRequest) { r.BeginDialogs = []string{strings.Repeat("x", maxPersonaDialog+1)} }, "开场对话第 1 条超过"},
		{"too many tools", func(r *personaRequest) { r.Tools = make([]string, maxPersonaTools+1) }, "工具白名单最多"},
		{"blank tool", func(r *personaRequest) { r.Tools = []string{"search", ""} }, "工具白名单里有空项"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base()
			tc.mutate(&req)
			got, err := req.toPersona()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("toPersona: %v", err)
				}
				if got.Name != strings.TrimSpace(req.Name) {
					t.Errorf("name = %q, want it trimmed", got.Name)
				}
				if got.ID != strings.TrimSpace(req.PersonaID) {
					t.Errorf("id = %q, want it trimmed", got.ID)
				}
				return
			}
			if err == nil {
				t.Fatalf("toPersona accepted %+v, want %q", req, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// A tools list that is absent (nil: every tool), empty ([]) and populated must
// all survive validation distinctly — the column is three-valued and the console
// edits it as such.
func TestPersonaRequestKeepsTheThreeValuedToolsList(t *testing.T) {
	req := personaRequest{Name: "n", SystemPrompt: "p"}
	p, err := req.toPersona()
	if err != nil {
		t.Fatalf("nil tools: %v", err)
	}
	if p.Tools != nil {
		t.Errorf("absent tools = %#v, want nil (every tool)", p.Tools)
	}

	req.Tools = []string{}
	if p, err = req.toPersona(); err != nil {
		t.Fatalf("empty tools: %v", err)
	}
	if p.Tools == nil || len(p.Tools) != 0 {
		t.Errorf("empty tools = %#v, want an empty non-nil slice (no tools)", p.Tools)
	}
}

func TestNormalizeScopeTarget(t *testing.T) {
	cases := []struct {
		scope, target      string
		wantScope, wantTgt string
		wantErr            bool
	}{
		{"global", "", "global", "", false},
		{"global", "ignored", "global", "", false},
		{"session", "sess-1", "session", "sess-1", false},
		{"conversation", "conv-1", "conversation", "conv-1", false},
		{"session", "  sess-1  ", "session", "sess-1", false},
		{"session", "", "", "", true},
		{"conversation", "   ", "", "", true},
		{"platform", "sess-1", "", "", true},
		{"", "", "", "", true},
		{"session", strings.Repeat("s", maxBindingTarget+1), "", "", true},
	}
	for _, tc := range cases {
		scope, target, err := normalizeScopeTarget(tc.scope, tc.target)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizeScopeTarget(%q, %q) = (%q, %q), want an error", tc.scope, tc.target, scope, target)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeScopeTarget(%q, %q): %v", tc.scope, tc.target, err)
			continue
		}
		if scope != tc.wantScope || target != tc.wantTgt {
			t.Errorf("normalizeScopeTarget(%q, %q) = (%q, %q), want (%q, %q)",
				tc.scope, tc.target, scope, target, tc.wantScope, tc.wantTgt)
		}
	}
}

// A minted id has to pass the same check the caller-supplied one does, or the
// API would reject its own output on the next save.
func TestMintPersonaIDIsValidAndUnique(t *testing.T) {
	a, b := mintPersonaID(), mintPersonaID()
	if !validPersonaID(a) {
		t.Errorf("mintPersonaID() = %q, which validPersonaID rejects", a)
	}
	if a == b {
		t.Errorf("two calls returned the same id %q", a)
	}
}
