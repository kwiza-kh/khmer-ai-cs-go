package api

import "testing"

// The correlation id is echoed back in a header and written to every log line,
// so only a well-formed token is accepted; anything else is replaced.
func TestSafeRequestID(t *testing.T) {
	for _, ok := range []string{"abc", "a-b_c.123", "0f6b2c1e-1a2b-4c5d-8e9f-0123456789ab"} {
		if got := safeRequestID(ok); got != ok {
			t.Errorf("safeRequestID(%q) = %q, want it kept", ok, got)
		}
	}
	for _, bad := range []string{"", "  ", "has space", "a\nb", "a\rb", "中文", "<script>", string(make([]byte, 65))} {
		if got := safeRequestID(bad); got != "" {
			t.Errorf("safeRequestID(%q) = %q, want rejected", bad, got)
		}
	}
}
