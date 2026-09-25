package main

// Tests for the READ-ONLY guard and the corpus helpers.
//
// assertSelectOnly is the mechanical half of the read-only contract, so it is
// tested against the statements it must REFUSE as carefully as the ones it must
// accept: a guard that passes an UPDATE is worse than no guard, because the
// comment above it claims the database is safe.

import (
	"strings"
	"testing"
)

func TestAssertSelectOnlyAcceptsRealQueries(t *testing.T) {
	ok := []struct {
		name string
		sql  string
	}{
		{"the corpus query", corpusSQL},
		{"the corpus stats query", corpusStatsSQL},
		{"the document stats query", documentStatsSQL},
		{"leading whitespace and newlines", "\n\t  SELECT 1"},
		{"a trailing semicolon", "SELECT 1;"},
		{"a leading line comment", "-- why not\nSELECT 1"},
		{"a leading block comment", "/* why not */ SELECT 1"},
		{"lowercase", "select chunk_id from knowledge_chunks"},
		{"a quoted keyword is not the first token", `SELECT 'insert into x'`},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			if err := assertSelectOnly(tc.sql); err != nil {
				t.Errorf("assertSelectOnly refused a SELECT: %v", err)
			}
		})
	}
}

func TestAssertSelectOnlyRefusesWrites(t *testing.T) {
	bad := []struct {
		name string
		sql  string
	}{
		{"UPDATE", "UPDATE knowledge_chunks SET embedding = NULL"},
		{"INSERT", "INSERT INTO knowledge_chunks (doc_id) VALUES (1)"},
		{"DELETE", "DELETE FROM knowledge_chunks"},
		{"TRUNCATE", "TRUNCATE knowledge_chunks"},
		{"ALTER", "ALTER TABLE knowledge_chunks ADD COLUMN x int"},
		{"DROP", "DROP TABLE knowledge_chunks"},
		{"CREATE", "CREATE TABLE x (a int)"},
		{"SET", "SET default_transaction_read_only = off"},
		{"COPY", "COPY knowledge_chunks TO '/tmp/dump.csv'"},
		{"VACUUM", "VACUUM knowledge_chunks"},
		{"a write hidden after a SELECT", "SELECT 1; DELETE FROM knowledge_chunks"},
		{"a write hidden after a comment", "-- SELECT\nUPDATE knowledge_chunks SET embedding = NULL"},
		{"a write hidden after a block comment", "/* SELECT */ DELETE FROM knowledge_chunks"},
		{"a CTE that writes", "WITH x AS (DELETE FROM knowledge_chunks RETURNING *) SELECT * FROM x"},
		{"an empty statement", "   "},
		{"only a comment", "-- nothing here"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			err := assertSelectOnly(tc.sql)
			if err == nil {
				t.Fatalf("assertSelectOnly ACCEPTED a non-SELECT statement: %q", tc.sql)
			}
			if !strings.Contains(err.Error(), "READ-ONLY") && !strings.Contains(err.Error(), "more than one statement") && !strings.Contains(err.Error(), "empty") {
				t.Errorf("refusal message does not explain itself: %v", err)
			}
		})
	}
}

func TestStripSQLComments(t *testing.T) {
	got := stripSQLComments("SELECT 1 -- trailing\nFROM t /* inline */ WHERE a = 1")
	if strings.Contains(got, "--") || strings.Contains(got, "/*") {
		t.Errorf("comments survived: %q", got)
	}
	// Two tokens separated by a comment must not fuse into one word.
	if got := stripSQLComments("SELECT/*x*/1"); !strings.Contains(got, "SELECT 1") {
		t.Errorf("comment removal fused tokens: %q", got)
	}
}

func TestParseVectorLiteral(t *testing.T) {
	got, err := parseVectorLiteral("[1,2.5,-3]")
	if err != nil {
		t.Fatalf("parseVectorLiteral: %v", err)
	}
	want := []float32{1, 2.5, -3}
	if len(got) != len(want) {
		t.Fatalf("parsed %d elements, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("element %d = %v, want %v", i, got[i], want[i])
		}
	}

	// The value pgvector actually emits for a 768-dim column, spot-checked at the
	// edges: a parser that mishandles the first or last element would silently
	// corrupt every similarity.
	long := "[" + strings.Repeat("0.1,", 767) + "0.9]"
	got, err = parseVectorLiteral(long)
	if err != nil {
		t.Fatalf("parseVectorLiteral(768 dims): %v", err)
	}
	if len(got) != 768 {
		t.Fatalf("parsed %d elements, want 768", len(got))
	}
	if got[767] != float32(0.9) {
		t.Errorf("last element = %v, want 0.9", got[767])
	}

	// pgvector pads with spaces in some renderings.
	if _, err := parseVectorLiteral("[ 1 , 2 ]"); err != nil {
		t.Errorf("spaced literal rejected: %v", err)
	}
}

func TestParseVectorLiteralRejectsGarbage(t *testing.T) {
	// A bad vector must be an ERROR, never a silent zero vector: a chunk scored
	// at similarity 0 looks exactly like a retrieval failure caused by the
	// embedding convention, which is the one thing this tool must not fake.
	for _, bad := range []string{"", "   ", "[]", "[1,2,]", "abc", "[1,x]"} {
		if vec, err := parseVectorLiteral(bad); err == nil {
			t.Errorf("parseVectorLiteral(%q) = %v, want an error", bad, vec)
		}
	}
}
