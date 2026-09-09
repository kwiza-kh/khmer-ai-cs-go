package migrations

import (
	"strings"
	"testing"
)

func TestMigrationsAreLexicallyOrderedAndComplete(t *testing.T) {
	files, err := Files()
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	if len(files) != 42 {
		t.Fatalf("expected all 42 migration files, got %d", len(files))
	}
	wantFirst := "001_init.sql"
	wantLast := "042_google_sso.sql"
	if files[0][0] != wantFirst {
		t.Errorf("first migration = %s, want %s", files[0][0], wantFirst)
	}
	if files[len(files)-1][0] != wantLast {
		t.Errorf("last migration = %s, want %s", files[len(files)-1][0], wantLast)
	}
	for i := 1; i < len(files); i++ {
		if files[i-1][0] >= files[i][0] {
			t.Fatalf("files not lexically sorted at %s", files[i][0])
		}
	}
	// Spot check content: pgvector extension and later features survived embedding.
	if !strings.Contains(files[0][1], "CREATE EXTENSION IF NOT EXISTS vector") {
		t.Error("001_init.sql lost the pgvector extension")
	}
	if !strings.Contains(files[33][1], "notification_pref") {
		t.Error("034_user_preferences.sql lost the notification_pref column")
	}
}

func TestDefaultAdminHashIsTheExpectedPlaceholder(t *testing.T) {
	if !strings.HasPrefix(InsecureDefaultAdminHash, "$2a$10$") {
		t.Error("default admin hash must be the $2a$10$ placeholder")
	}
}
