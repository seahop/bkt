package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("token has expired")
)

// Token types. A refresh token must never be accepted as an access token on a
// protected route — otherwise the long refresh lifetime defeats the short
// access-token expiry.
const (
	TokenTypeAccess  = "access"
	TokenTypeRefresh = "refresh"
)

type Claims struct {
	UserID   uuid.UUID `json:"user_id"`
	Username string    `json:"username"`
	IsAdmin  bool      `json:"is_admin"`
	// TokenType distinguishes access from refresh tokens.
	TokenType string `json:"token_type"`
	// TokenVersion is compared against the user's current TokenVersion on every
	// request; bumping the user's version invalidates all previously-issued
	// tokens (logout-everywhere, lock, password change, admin demotion).
	TokenVersion int `json:"token_version"`
	// PairJTI (access tokens only) is the JTI of the refresh token issued in
	// the same pair, so logout can revoke the sibling refresh token even when
	// the client never stored or sent it.
	PairJTI string `json:"pair_jti,omitempty"`
	jwt.RegisteredClaims
}

// GenerateToken creates a new signed JWT of the given type for a user.
func GenerateToken(userID uuid.UUID, username string, isAdmin bool, tokenVersion int, tokenType, secret string, duration time.Duration) (string, error) {
	claims := Claims{
		UserID:       userID,
		Username:     username,
		IsAdmin:      isAdmin,
		TokenType:    tokenType,
		TokenVersion: tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.New().String(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(duration)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// GenerateTokenPair issues an access+refresh token pair. The access token
// carries the refresh token's JTI (pair_jti) so that logout — which only ever
// sees the access token — can revoke the sibling refresh token too.
func GenerateTokenPair(userID uuid.UUID, username string, isAdmin bool, tokenVersion int, secret string, accessDuration, refreshDuration time.Duration) (accessToken string, refreshToken string, err error) {
	refreshJTI := uuid.New().String()
	now := time.Now()

	refreshClaims := Claims{
		UserID:       userID,
		Username:     username,
		IsAdmin:      isAdmin,
		TokenType:    TokenTypeRefresh,
		TokenVersion: tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        refreshJTI,
			ExpiresAt: jwt.NewNumericDate(now.Add(refreshDuration)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	refreshToken, err = jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString([]byte(secret))
	if err != nil {
		return "", "", err
	}

	accessClaims := Claims{
		UserID:       userID,
		Username:     username,
		IsAdmin:      isAdmin,
		TokenType:    TokenTypeAccess,
		TokenVersion: tokenVersion,
		PairJTI:      refreshJTI,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.New().String(),
			ExpiresAt: jwt.NewNumericDate(now.Add(accessDuration)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	accessToken, err = jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString([]byte(secret))
	if err != nil {
		return "", "", err
	}
	return accessToken, refreshToken, nil
}

// ParseTokenClaims parses a JWT and returns claims without enforcing expiry.
// Use only for revocation on logout — never for auth decisions.
func ParseTokenClaims(tokenString string, secret string) (*Claims, error) {
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	token, err := parser.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// ValidateToken validates a JWT token and returns the claims
func ValidateToken(tokenString string, secret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))

	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	// Verify expiration time exists and is valid
	if claims.ExpiresAt == nil {
		return nil, ErrInvalidToken
	}

	if time.Now().After(claims.ExpiresAt.Time) {
		return nil, ErrExpiredToken
	}

	return claims, nil
}
