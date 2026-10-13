package platform

import (
	"crypto/rand"
	"encoding/json"
)

// NewUUID returns a random RFC-4122 v4 UUID string. It is the one generator in
// the tree: internal/api used to carry its own copy.
func NewUUID() string {
	b := make([]byte, 16)
	// crypto/rand.Read never returns an error on Go >= 1.24 (it panics when no
	// entropy source is available), so an error branch here is dead code that
	// only hid a downgrade path: the old fallback returned randSeed()'s 0, i.e.
	// the constant string "0", which collapsed every session id and SSO flow id
	// onto one shared value. A missing id must never look like a valid one.
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	const hexChars = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, c := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexChars[c>>4], hexChars[c&0x0f])
	}
	return string(out)
}

// toJSON marshals v to json.RawMessage for DB JSONB columns.
func toJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
