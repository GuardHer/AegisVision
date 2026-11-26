package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims defines the JWT payload carried by AegisVision access tokens.
type Claims struct {
	UserID   string `json:"uid"`
	Username string `json:"uname"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// TokenManager issues and parses HS256 JWT access tokens.
type TokenManager struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenManager creates a TokenManager with secret and default access token TTL.
func NewTokenManager(secret string, ttl time.Duration) *TokenManager {
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	return &TokenManager{
		secret: []byte(secret),
		ttl:    ttl,
	}
}

// IssueAccessToken signs a new JWT access token and returns (token, jti, expiresAt, err).
func (m *TokenManager) IssueAccessToken(userID, username, role string) (string, string, time.Time, error) {
	now := time.Now().UTC()
	exp := now.Add(m.ttl)
	jti := uuid.Must(uuid.NewV7()).String()

	claims := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			Issuer:    "aegisvision",
		},
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(m.secret)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return signed, jti, exp, nil
}

// ParseAccessToken verifies signature and expiration and returns token claims.
func (m *TokenManager) ParseAccessToken(tokenStr string) (*Claims, error) {
	parsed, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected jwt signing method: %v", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("parse access token: %w", err)
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid access token claims")
	}
	return claims, nil
}

// GenerateOpaqueToken produces a cryptographically random URL-safe opaque token.
func GenerateOpaqueToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate opaque token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
