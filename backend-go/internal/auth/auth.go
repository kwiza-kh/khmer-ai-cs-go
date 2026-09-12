// Package auth provides HS256 JWT and bcrypt password primitives with the
// exact wire format of the Rust backend (and the original Go one): claims
// user_id/username/role/exp/iat/iss, bcrypt cost 12.
package auth

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const PasswordHashCost = 12

const (
	// JWTIssuer is the `iss` claim stamped on every token this service mints.
	JWTIssuer = "relaychat"

	// legacyJWTIssuer is the pre-rename issuer ("khmer-ai-cs"). Tokens signed
	// with it are still accepted so the rename does not sign every logged-in
	// user out at deploy time.
	//
	// Safe to delete once one full token lifetime has passed since the rename
	// shipped — JWT_EXPIRE_HOUR, 24h by default — after which no token bearing
	// it can still be valid.
	legacyJWTIssuer = "khmer-ai-cs"
)

type Claims struct {
	UserID   int32  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// TokenVersion must equal users.token_version. Bumping that column
	// invalidates every token already issued for the user, which is how a
	// password change, role change or 2FA change terminates existing sessions
	// (JWTs are otherwise valid until they expire).
	TokenVersion int `json:"tv"`
	jwt.RegisteredClaims
}

type JWT struct {
	secret     []byte
	expireHour int64
}

func NewJWT(secret string, expireHour int64) *JWT {
	return &JWT{secret: []byte(secret), expireHour: expireHour}
}

// GenerateToken signs an HS256 token identical to the Rust claims shape, with
// the user's current token_version stamped in. Pass the value read from
// users.token_version; a mismatch on later requests means the token was
// revoked and the middleware rejects it.
func (j *JWT) GenerateToken(userID int32, username, role string, tokenVersion int) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:       userID,
		Username:     username,
		Role:         role,
		TokenVersion: tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(j.expireHour) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    JWTIssuer,
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(j.secret)
}

// ParseToken validates signature + expiry and rejects algorithm confusion.
// The issuer is checked by hand rather than via jwt.WithIssuer because two
// values are valid during the rename window (see legacyJWTIssuer).
func (j *JWT) ParseToken(token string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return j.secret, nil
	}, jwt.WithExpirationRequired())
	if err == nil && claims.Issuer != JWTIssuer && claims.Issuer != legacyJWTIssuer {
		err = fmt.Errorf("unexpected issuer %q", claims.Issuer)
	}
	if err != nil {
		return nil, errors.New("令牌无效或已过期")
	}
	return claims, nil
}

// HashPassword produces a cost-12 bcrypt hash compatible with the Rust/Go
// backends (x/crypto/bcrypt).
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), PasswordHashCost)
	return string(h), err
}

// VerifyPassword works with $2a$, $2b$ and $2y$ hashes.
func VerifyPassword(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// PasswordNeedsRehash reports whether the stored hash predates the current
// cost and should be upgraded at login (cost < 12).
func PasswordNeedsRehash(hash string) bool {
	parts := strings.Split(hash, "$")
	// $2a$10$... → ["", "2a", "10", "..."]
	if len(parts) < 3 {
		return false
	}
	cost, err := strconv.Atoi(parts[2])
	if err != nil {
		return false
	}
	return cost < PasswordHashCost
}
