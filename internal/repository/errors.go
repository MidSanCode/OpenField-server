package repository

import (
	"errors"
	"strings"
)

// Sentinel errors returned by repositories.
var (
	ErrUsernameTaken = errors.New("username already taken")
	ErrNotFound      = errors.New("not found")
)

// isUniqueViolation detects PostgreSQL unique constraint violations.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}
