package middleware

import (
	"testing"
)

// A purpose token must not be accepted by the access-token parsers, while a
// normal access token must keep working.
func TestPurposeTokenNotAcceptedAsAccessToken(t *testing.T) {
	secret := "test-secret-key-for-purpose-confusion"
	tm := NewTokenManager(secret, 1)

	access, err := tm.GenerateToken(42, "u@example.com", "u", false)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	purposeStr, err := tm.GeneratePurposeToken(42, "bind")
	if err != nil {
		t.Fatalf("GeneratePurposeToken: %v", err)
	}

	// Access token still parses through both paths.
	if uid, err := ParseToken(access, secret); err != nil || uid != 42 {
		t.Fatalf("access token rejected: uid=%d err=%v", uid, err)
	}
	if uid, _, err := ParseTokenWithClaims(access, secret); err != nil || uid != 42 {
		t.Fatalf("access token rejected by WithClaims: uid=%d err=%v", uid, err)
	}

	// The purpose token must be refused as an access token.
	if uid, err := ParseToken(purposeStr, secret); err == nil {
		t.Fatalf("purpose token accepted as access token: uid=%d", uid)
	}
	if uid, _, err := ParseTokenWithClaims(purposeStr, secret); err == nil {
		t.Fatalf("purpose token accepted by WithClaims: uid=%d", uid)
	}

	// ...but it must still work for its intended purpose.
	if uid, err := tm.ParsePurposeToken(purposeStr, "bind"); err != nil || uid != 42 {
		t.Fatalf("purpose token rejected for its own purpose: uid=%d err=%v", uid, err)
	}
	// ...and not for a different purpose.
	if _, err := tm.ParsePurposeToken(purposeStr, "reset"); err == nil {
		t.Fatal("purpose token accepted for the wrong purpose")
	}
}
