package middleware

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenManager handles JWT token operations.
type TokenManager struct {
	secretKey []byte
	expiry    time.Duration
}

// NewTokenManager creates a new TokenManager.
func NewTokenManager(secretKey string, expiryHours int) *TokenManager {
	return &TokenManager{
		secretKey: []byte(secretKey),
		expiry:    time.Duration(expiryHours) * time.Hour,
	}
}

// Claims represents JWT claims.
type Claims struct {
	UserID   int64  `json:"user_id"`
	Email    string `json:"email"`
	Username string `json:"username"`
	Purpose  string `json:"purpose,omitempty"`
	jwt.RegisteredClaims
}

// GenerateToken creates a new JWT token for a user.
func (tm *TokenManager) GenerateToken(userID int64, email, username string) (string, error) {
	claims := &Claims{
		UserID:   userID,
		Email:    email,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(tm.expiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Subject:   "user",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(tm.secretKey)
}

// GeneratePurposeToken creates a short-lived JWT token for a specific purpose,
// e.g. to sign the OAuth state parameter during an OIDC account-binding flow.
func (tm *TokenManager) GeneratePurposeToken(userID int64, purpose string) (string, error) {
	claims := &Claims{
		UserID:  userID,
		Purpose: purpose,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Subject:   purpose,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(tm.secretKey)
}

// Parse validates a JWT and returns the user ID it carries.
func (tm *TokenManager) Parse(tokenStr string) (int64, error) {
	return ParseToken(tokenStr, string(tm.secretKey))
}

// ParsePurposeToken validates a purpose token and returns its user ID.
func (tm *TokenManager) ParsePurposeToken(tokenStr, purpose string) (int64, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return tm.secretKey, nil
	})
	if err != nil {
		return 0, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return 0, fmt.Errorf("invalid token claims")
	}
	if claims.Purpose != purpose {
		return 0, fmt.Errorf("unexpected token purpose %q", claims.Purpose)
	}
	return claims.UserID, nil
}

// ParseToken validates and parses a JWT token.
func ParseToken(tokenStr string, secretKey string) (int64, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secretKey), nil
	})

	if err != nil {
		return 0, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims.UserID, nil
	}

	return 0, fmt.Errorf("invalid token claims")
}
