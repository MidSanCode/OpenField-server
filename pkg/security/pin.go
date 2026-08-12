package security

import (
	"errors"
	"regexp"

	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidPin reports a PIN that fails the length / digit policy.
var ErrInvalidPin = errors.New("pin must be 6 digits")

var pinPattern = regexp.MustCompile(`^[0-9]{6}$`)

// ValidPin reports whether a payment PIN meets the 6-digit policy.
func ValidPin(pin string) bool {
	return pinPattern.MatchString(pin)
}

// HashPin hashes a payment PIN for storage. Only ever store the hash.
func HashPin(pin string) (string, error) {
	if !ValidPin(pin) {
		return "", ErrInvalidPin
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPin reports whether the PIN matches the stored bcrypt hash.
func VerifyPin(pin, hash string) bool {
	if !ValidPin(pin) {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pin)) == nil
}