package migrations

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// migrationName matches the required NNN_snake_case.sql filename shape.
var migrationName = regexp.MustCompile(`^(\d{3})_[a-z0-9_]+\.sql$`)

// TestMigrationsAreLexicallyOrderedAndComplete validates the embedded
// migration set by its invariants rather than by a fixed count.
//
// The previous version asserted `len(files) == 46` and "last is
// 046_session_archive.sql". Both hold only until someone adds a migration, at
// which point the test fails for a change that is entirely correct — exactly
// what happened when 047–049 landed. Asserting the *shape* (numbered from 001,
// gapless, lexically ordered) still catches real breakage — a skipped or
// misnumbered file — without needing an edit every time a migration is added.
func TestMigrationsAreLexicallyOrderedAndComplete(t *testing.T) {
	files, err := Files()
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations embedded — the //go:embed directive is broken")
	}

	wantFirst := "001_init.sql"
	if files[0][0] != wantFirst {
		t.Errorf("first migration = %s, want %s", files[0][0], wantFirst)
	}

	for i, f := range files {
		name, body := f[0], f[1]

		m := migrationName.FindStringSubmatch(name)
		if m == nil {
			t.Errorf("migration %q does not match NNN_snake_case.sql", name)
			continue
		}
		num, err := strconv.Atoi(m[1])
		if err != nil {
			t.Errorf("migration %q has an unparsable number: %v", name, err)
			continue
		}
		// Numbering starts at 001 and advances by exactly one per file, so a
		// gap or duplicate is a hard error rather than a silent resequencing.
		if want := i + 1; num != want {
			t.Errorf("migration %q is number %03d, want %03d (gap or duplicate in the sequence)", name, num, want)
		}
		// Ordering is by filename, so lexical must agree with numeric order.
		if i > 0 && files[i-1][0] >= name {
			t.Fatalf("files not lexically sorted at %s (previous %s)", name, files[i-1][0])
		}
		if strings.TrimSpace(body) == "" {
			t.Errorf("migration %q is empty", name)
		}
	}

	// Spot check that content survived embedding: pgvector from the first
	// migration, and a late-added column from a much later one. Both are looked
	// up by name so this never rots when migrations are appended.
	if !strings.Contains(files[0][1], "CREATE EXTENSION IF NOT EXISTS vector") {
		t.Error("001_init.sql lost the pgvector extension")
	}
	if !strings.Contains(migrationBody(files, "034_user_preferences.sql"), "notification_pref") {
		t.Error("034_user_preferences.sql lost the notification_pref column")
	}
}

// TestMirrorDirectoryIsInSync guards backend-go/migrations, the psql-inspection
// mirror the dev guide requires to be kept in step with the embedded set. It
// had silently fallen 13 files behind (stuck at 036 while the embedded set
// reached 049), so anyone reading the mirror would see the wrong schema. A
// stale schema copy is worse than none, so the drift needs to fail loudly.
func TestMirrorDirectoryIsInSync(t *testing.T) {
	embeddedFiles, err := Files()
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	// The mirror lives at backend-go/migrations; tests run in the package dir.
	mirrorDir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(mirrorDir)
	if err != nil {
		t.Skipf("mirror directory unavailable: %v", err)
	}

	var mirror []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			mirror = append(mirror, e.Name())
		}
	}
	sort.Strings(mirror)

	if len(mirror) != len(embeddedFiles) {
		t.Errorf("mirror has %d migrations, embedded set has %d — keep them in sync "+
			"(deploy-khmer-ai-cs/references/dev-guide.md §2)", len(mirror), len(embeddedFiles))
	}

	embedded := make(map[string]bool, len(embeddedFiles))
	for _, f := range embeddedFiles {
		embedded[f[0]] = true
	}
	for _, name := range mirror {
		if !embedded[name] {
			t.Errorf("mirror migration %q has no counterpart in the embedded set", name)
		}
	}

	// Same name is not enough — the bodies must match too, or the mirror
	// silently documents a different schema.
	for _, f := range embeddedFiles {
		path := filepath.Join(mirrorDir, f[0])
		body, err := os.ReadFile(path)
		if err != nil {
			continue // absence already reported by the count check
		}
		if string(body) != f[1] {
			t.Errorf("mirror %s differs from the embedded copy — re-copy it", f[0])
		}
	}
}

// migrationBody returns the body of the named migration, or "" when absent.
func migrationBody(files [][2]string, name string) string {
	for _, f := range files {
		if f[0] == name {
			return f[1]
		}
	}
	return ""
}

func TestDefaultAdminHashIsTheExpectedPlaceholder(t *testing.T) {
	if !strings.HasPrefix(InsecureDefaultAdminHash, "$2a$10$") {
		t.Error("default admin hash must be the $2a$10$ placeholder")
	}
}
