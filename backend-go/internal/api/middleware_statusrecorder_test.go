package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// countingHeaderWriter counts how many times the wrapped writer was asked to
// write a header — i.e. how many calls actually reached net/http, which logs
// "superfluous response.WriteHeader call" on the second one.
type countingHeaderWriter struct {
	http.ResponseWriter
	headers int
}

func (c *countingHeaderWriter) WriteHeader(code int) {
	c.headers++
	c.ResponseWriter.WriteHeader(code)
}

// The production symptom was 29 "superfluous response.WriteHeader call from
// .../middleware.go:120" lines in three days: the wrapper forwarded every call
// instead of the first.
func TestStatusRecorderForwardsOnlyFirstWriteHeader(t *testing.T) {
	inner := &countingHeaderWriter{ResponseWriter: httptest.NewRecorder()}
	rec := &statusRecorder{ResponseWriter: inner, status: 200}

	rec.WriteHeader(http.StatusNotFound)
	rec.WriteHeader(http.StatusInternalServerError)

	if inner.headers != 1 {
		t.Fatalf("underlying WriteHeader called %d times, want 1 (the extra call is what net/http logs as superfluous)", inner.headers)
	}
	if rec.status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d — a second WriteHeader must not rewrite the logged code", rec.status, http.StatusNotFound)
	}
}

// A handler that writes a body first sends an implicit 200; a later
// WriteHeader must not be recorded as the status while net/http ignores it.
func TestStatusRecorderWritePinsLoggedStatus(t *testing.T) {
	inner := &countingHeaderWriter{ResponseWriter: httptest.NewRecorder()}
	rec := &statusRecorder{ResponseWriter: inner, status: 200}

	if _, err := rec.Write([]byte("ok")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	rec.WriteHeader(http.StatusInternalServerError)

	if rec.status != http.StatusOK {
		t.Fatalf("status = %d, want %d — the log must name the code that reached the wire", rec.status, http.StatusOK)
	}
	if inner.headers != 0 {
		t.Fatalf("underlying WriteHeader called %d times after a body write, want 0", inner.headers)
	}
}
