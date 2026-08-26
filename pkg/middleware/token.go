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

// ExpirySeconds returns the access token lifetime in seconds.
func (tm *TokenManager) ExpirySeconds() int {
	return int(tm.expiry.Seconds())
}

// Claims represents JWT claims.
type Claims struct {
	UserID   int64  `json:"user_id"`
	Email    string `json:"email"`
	Username string `json:"username"`
	// NeedsRegistration marks an account that has logged in through OIDC but
	// has not picked a username yet. The gateway refuses such tokens on most
	// authenticated routes so unfinished sign-ups cannot poke at private data.
	NeedsRegistration bool   `json:"nreg,omitempty"`
	Purpose           string `json:"purpose,omitempty"`
	jwt.RegisteredClaims
}

// GenerateToken creates a new JWT token for a user. The needsRegistration flag
// is embedded in the JWT so the gateway can lock down unfinished OAuth sign-ups
// across every service without a database lookup.
func (tm *TokenManager) GenerateToken(userID int64, email, username string, needsRegistration bool) (string, error) {
	claims := &Claims{
		UserID:            userID,
		Email:             email,
		Username:          username,
		NeedsRegistration: needsRegistration,
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

// TokenNeedsRegistration returns whether the given token belongs to an OAuth
// user that has not finished registration yet.
func (tm *TokenManager) TokenNeedsRegistration(tokenStr string) bool {
	tk, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return tm.secretKey, nil
	})
	if err != nil {
		return false
	}
	claims, ok := tk.Claims.(*Claims)
	if !ok {
		return false
	}
	return claims.NeedsRegistration
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

// ParseTokenWithClaims is like ParseToken but also reports whether the
// authenticated user still needs to finish registration. The gateway uses
// this to forward the nreg flag to internal services so they can refuse
// privileged operations until the user has picked a username.
func ParseTokenWithClaims(tokenStr string, secretKey string) (int64, bool, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secretKey), nil
	})
	if err != nil {
		return 0, false, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return 0, false, fmt.Errorf("invalid token claims")
	}
	return claims.UserID, claims.NeedsRegistration, nil
}
