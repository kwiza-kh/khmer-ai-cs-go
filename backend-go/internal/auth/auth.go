// Package auth provides HS256 JWT and bcrypt password primitives with the
// exact wire format of the Rust backend (and the original Go one): claims
// user_id/username/role/exp/iat/iss="khmer-ai-cs", bcrypt cost 12.
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

type Claims struct {
	UserID   int32  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

type JWT struct {
	secret     []byte
	expireHour int64
}

func NewJWT(secret string, expireHour int64) *JWT {
	return &JWT{secret: []byte(secret), expireHour: expireHour}
}

// GenerateToken signs an HS256 token identical to the Rust claims shape.
func (j *JWT) GenerateToken(userID int32, username, role string) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(j.expireHour) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "khmer-ai-cs",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(j.secret)
}

// ParseToken validates signature + expiry and rejects algorithm confusion.
func (j *JWT) ParseToken(token string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return j.secret, nil
	}, jwt.WithIssuer("khmer-ai-cs"), jwt.WithExpirationRequired())
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
