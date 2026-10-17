// TOTP (RFC 6238) verification — port of the Rust `totp.rs` (base32 codec +
// HOTP with SHA-1, 6 digits, 30s step, ±1 drift tolerance).
package auth

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

const base32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

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

// VerifyTOTPStep checks a candidate code against the base32 secret, tolerating
// ±1 30-second step of clock skew, and returns the step that matched. The
// comparison is constant-time per candidate: the digit-by-digit integer loop it
// replaced leaked how many leading digits were right.
//
// The step is returned so the caller can refuse replaying the same (or an older)
// window — a code stays valid for up to 90 seconds with the tolerance, which is
// long enough to be reused otherwise.
func VerifyTOTPStep(secret, candidate string) (int64, bool) {
	key, ok := base32Decode(secret)
	if !ok {
		return 0, false
	}
	code := strings.TrimSpace(candidate)
	if len(code) != 6 {
		return 0, false
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	now := time.Now().Unix() / 30
	for _, off := range []int64{0, -1, 1} {
		want := fmt.Sprintf("%06d", hotp(key, uint64(now+off)))
		if subtle.ConstantTimeCompare([]byte(code), []byte(want)) == 1 {
			return now + off, true
		}
	}
	return 0, false
}

// VerifyTOTP is the boolean form, for callers that do not police replay.
func VerifyTOTP(secret, candidate string) bool {
	_, ok := VerifyTOTPStep(secret, candidate)
	return ok
}
