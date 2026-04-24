package middleware

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// AuthKind describes how the request was authenticated.
type AuthKind int

const (
	AuthNone AuthKind = iota
	AuthAPIKey
	AuthJWT
)

// AuthInfo is set when a protected route is accessed successfully.
type AuthInfo struct {
	Kind   AuthKind
	UserID string // JWT sub; empty for API key
}

func constantTimeEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// CheckProtected validates API key (X-API-Key or Bearer token matching DATAPULSE_API_KEY)
// or Supabase JWT (Authorization: Bearer when SUPABASE_JWT_SECRET is set).
// If authDisabled is true, always returns ok=true with AuthNone.
func CheckProtected(w http.ResponseWriter, r *http.Request, authDisabled bool, apiKey, jwtSecret string) (info AuthInfo, ok bool) {
	if authDisabled {
		return AuthInfo{Kind: AuthNone}, true
	}

	if k := strings.TrimSpace(r.Header.Get("X-API-Key")); k != "" && apiKey != "" {
		if constantTimeEq(k, apiKey) {
			return AuthInfo{Kind: AuthAPIKey}, true
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return AuthInfo{}, false
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return AuthInfo{}, false
	}
	token := strings.TrimSpace(auth[7:])
	if token == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return AuthInfo{}, false
	}

	if jwtSecret != "" {
		sub, err := ParseSupabaseJWT(jwtSecret, token)
		if err == nil && sub != "" {
			return AuthInfo{Kind: AuthJWT, UserID: sub}, true
		}
	}

	if apiKey != "" && constantTimeEq(token, apiKey) {
		return AuthInfo{Kind: AuthAPIKey}, true
	}

	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return AuthInfo{}, false
}

// ParseSupabaseJWT verifies HS256 JWT and returns the "sub" claim.
func ParseSupabaseJWT(secret, tokenString string) (string, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return "", err
	}
	if !token.Valid {
		return "", fmt.Errorf("invalid token")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", fmt.Errorf("invalid claims")
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", fmt.Errorf("missing sub")
	}
	return sub, nil
}
