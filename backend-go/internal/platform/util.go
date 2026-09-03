package platform

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
)

// newUUID returns a random RFC-4122 v4 UUID string.
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", randSeed())
	}
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

func randSeed() int64 {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	var v int64
	for _, c := range b {
		v = v<<8 | int64(c)
	}
	if v < 0 {
		v = -v
	}
	return v
}

// toJSON marshals v to json.RawMessage for DB JSONB columns.
func toJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
