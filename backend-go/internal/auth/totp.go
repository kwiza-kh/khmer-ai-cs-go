// TOTP (RFC 6238) verification — port of the Rust `totp.rs` (base32 codec +
// HOTP with SHA-1, 6 digits, 30s step, ±1 drift tolerance).
package auth

import (
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"strings"
	"time"
)

const base32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

func base32Encode(b []byte) string {
	var out strings.Builder
	buffer, bits := uint32(0), 0
	for _, c := range b {
		buffer = (buffer << 8) | uint32(c)
		bits += 8
		for bits >= 5 {
			out.WriteByte(base32Alphabet[(buffer>>(bits-5))&0x1f])
			bits -= 5
		}
	}
	if bits > 0 {
		out.WriteByte(base32Alphabet[(buffer<<(5-bits))&0x1f])
	}
	return out.String()
}

func base32Decode(s string) ([]byte, bool) {
	var buffer uint32
	bits := 0
	var out []byte
	for _, ch := range strings.ToUpper(strings.ReplaceAll(s, "=", "")) {
		v := -1
		for i, a := range base32Alphabet {
			if a == ch {
				v = i
				break
			}
		}
		if v < 0 {
			return nil, false
		}
		buffer = (buffer << 5) | uint32(v)
		bits += 5
		if bits >= 8 {
			out = append(out, byte(buffer>>(bits-8)&0xff))
			bits -= 8
		}
	}
	return out, true
}

func hotp(secret []byte, counter uint64) int {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, secret)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	binCode := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]&0xff) << 16) |
		(uint32(sum[offset+2]&0xff) << 8) |
		uint32(sum[offset+3]&0xff)
	return int(binCode % 1_000_000)
}

// VerifyTOTP checks a candidate code against the base32 secret, tolerating ±1
// 30-second step of clock skew.
func VerifyTOTP(secret, candidate string) bool {
	key, ok := base32Decode(secret)
	if !ok {
		return false
	}
	code := 0
	for _, ch := range strings.TrimSpace(candidate) {
		if ch < '0' || ch > '9' {
			return false
		}
		code = code*10 + int(ch-'0')
	}
	if code < 0 || code > 999_999 {
		return false
	}
	step := uint64(time.Now().Unix()) / 30
	for _, off := range []int64{0, -1, 1} {
		if hotp(key, uint64(int64(step)+off)) == code {
			return true
		}
	}
	return false
}

// GenerateTOTPSecret returns a fresh base32 secret (for 2FA setup).
func GenerateTOTPSecret() string {
	raw := make([]byte, 20)
	if _, err := crand.Read(raw); err != nil {
		return ""
	}
	return base32Encode(raw)
}
