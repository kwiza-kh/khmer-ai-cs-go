package tenantscope

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClassifier pins the rules themselves, so a change to the regexes cannot
// silently stop flagging (or start false-flagging) real shapes.
func TestClassifier(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool // true = bound in-statement
	}{
		{"tenant column", "SELECT 1 FROM sessions WHERE user_id = $1", true},
		{"aliased tenant column", "SELECT 1 FROM sessions s WHERE s.user_id = $1 AND s.status = 'active'", true},
		{"unscoped read", "SELECT content FROM sessions WHERE session_id = $1", false},
		{"derived table with join scope", "SELECT cm.content FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id WHERE s.user_id = $1", true},
		{"derived table alone", "SELECT content FROM chat_messages WHERE session_id = $1", false},
		{"named owner column", "SELECT 1 FROM agent_teams WHERE owner_user_id = $1", true},
		{"uploaded_by", "SELECT 1 FROM knowledge_documents WHERE uploaded_by = $1", true},
		{"insert naming tenant", "INSERT INTO sessions (session_id, user_id) VALUES ($1,$2)", true},
		{"insert omitting tenant", "INSERT INTO chat_messages (session_id, role) VALUES ($1,'user')", false},
		{"windows function table", "SELECT kc.content FROM knowledge_chunks kc JOIN knowledge_documents kd ON kc.doc_id = kd.doc_id WHERE kd.uploaded_by = $1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tables := touchedTenantTables(referencedTables(tc.sql))
			if len(tables) == 0 {
				t.Fatalf("no tenant table detected in %q", tc.sql)
			}
			got := boundInStatement(tc.sql, tables) || insertNamesTenant(tc.sql)
			if got != tc.want {
				t.Errorf("bound = %v, want %v (tables=%v)", got, tc.want, tables)
			}
		})
	}
}

// TestAnalyzeDetectsUnguardedStatement runs the whole pipeline against a
// throwaway module: an unscoped statement is reported, and each accepted
// shape (in-statement predicate, funnel, marker) is not.
func TestAnalyzeDetectsUnguardedStatement(t *testing.T) {
	dir := t.TempDir()
	src := `package probe

// unguarded is the leak shape: a session-derived table read with no tenant.
func unguarded(sessionID string) string {
	return "SELECT content FROM chat_messages WHERE session_id = $1"
}

// guarded runs the reviewed funnel first.
func guarded(sessionID string) string {
	ensureSessionAccess(sessionID)
	return "SELECT content FROM chat_messages WHERE session_id = $1"
}

// marked carries an explicit review decision.
//
// tenantscope:ok the row id is the capability
func marked(id int32) string {
	return "UPDATE widget_tokens SET is_active = false WHERE token_id = $1"
}

// scoped names the tenant itself.
func scoped(userID int32) string {
	return "UPDATE sessions SET status = 'closed' WHERE user_id = $1"
}
`
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Analyze(dir)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(rep.Gaps) != 1 {
		for _, g := range rep.Gaps {
			t.Logf("gap: %s", g)
		}
		t.Fatalf("gaps = %d, want exactly the unguarded function", len(rep.Gaps))
	}
	g := rep.Gaps[0]
	if g.Func != "unguarded" {
		t.Errorf("gap reported for %s(), want unguarded()", g.Func)
	}
	if !strings.Contains(strings.Join(g.Tables, ","), "chat_messages") {
		t.Errorf("gap tables = %v, want chat_messages", g.Tables)
	}
}
