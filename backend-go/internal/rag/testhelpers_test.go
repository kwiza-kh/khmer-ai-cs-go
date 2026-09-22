package rag

import "testing"

// mustEval is the shared test helper for the DB-gated eval/perf harnesses:
// fail the test immediately on any setup error.
func mustEval(err error, t *testing.T) {
	if err == nil {
		return
	}
	t.Fatal(err)
}
