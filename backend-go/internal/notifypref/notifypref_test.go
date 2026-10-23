package notifypref

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"":              All,
		"  ":            All,
		"all":           All,
		"ALL":           All,
		"escalations":   Escalations,
		" Escalations ": Escalations,
		"none":          None,
		"NONE":          None,
		"garbage":       All,
	}
	for raw, want := range cases {
		if got := Normalize(raw); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestAllows(t *testing.T) {
	cases := []struct {
		pref string
		kind string
		want bool
	}{
		{All, "message", true},
		{All, "handoff", true},
		{All, "sla", true},
		{All, "warning", true},
		{Escalations, "message", false},
		{Escalations, "handoff", true},
		{Escalations, "sla", true},
		{Escalations, "warning", true},
		{Escalations, "negative_feedback", true},
		{Escalations, "brand_new_kind", false},
		{None, "message", false},
		{None, "handoff", false},
		{None, "sla", false},
		{None, "warning", false},
		// Unknown stored value degrades to "all", never to "silently mute".
		{"legacy-value", "handoff", true},
	}
	for _, c := range cases {
		if got := Allows(c.pref, c.kind); got != c.want {
			t.Errorf("Allows(%q, %q) = %v, want %v", c.pref, c.kind, got, c.want)
		}
	}
}

// stubQueryer lets Load be tested without a database.
type stubQueryer struct {
	value string
	err   error
}

type stubRow struct {
	value string
	err   error
}

func (r stubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if p, ok := dest[0].(*string); ok {
		*p = r.value
	}
	return nil
}

func (q stubQueryer) QueryRow(context.Context, string, ...any) pgx.Row {
	return stubRow(q)
}

func TestLoad(t *testing.T) {
	got, err := Load(context.Background(), stubQueryer{value: "none"}, 7)
	if err != nil || got != None {
		t.Fatalf("Load = (%q, %v), want (%q, nil)", got, err, None)
	}
	// The column is nullable in older rows; COALESCE turns NULL into "".
	got, err = Load(context.Background(), stubQueryer{value: ""}, 7)
	if err != nil || got != All {
		t.Fatalf("Load(empty) = (%q, %v), want (%q, nil)", got, err, All)
	}
	// A read failure must fall back to All (callers send and log).
	if got, err := Load(context.Background(), stubQueryer{err: errors.New("db down")}, 7); err == nil || got != All {
		t.Fatalf("Load(error) = (%q, %v), want (%q, error)", got, err, All)
	}
}
