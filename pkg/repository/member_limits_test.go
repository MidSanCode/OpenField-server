package repository

import (
	"errors"
	"testing"
)

// The caps must be positive and sane: a zero or negative value would either
// block every group from ever gaining a member or silently mean "unlimited".
func TestMemberLimitConstantsAreUsable(t *testing.T) {
	if MaxGroupMembers <= 0 {
		t.Fatalf("MaxGroupMembers = %d; a non-positive cap would block all membership", MaxGroupMembers)
	}
	if MaxCampMembers <= 0 {
		t.Fatalf("MaxCampMembers = %d; a non-positive cap would block all membership", MaxCampMembers)
	}
	// A camp is the larger container; a group smaller than a camp is the
	// intended relationship, and guards against swapping the two values.
	if MaxGroupMembers > MaxCampMembers {
		t.Fatalf("MaxGroupMembers (%d) exceeds MaxCampMembers (%d); the caps look transposed",
			MaxGroupMembers, MaxCampMembers)
	}
}

// The sentinel must be matchable through errors.Is so both services can map it
// to a 409 instead of a 500.
func TestErrMemberLimitReachedIsSentinel(t *testing.T) {
	wrapped := errors.Join(errors.New("context"), ErrMemberLimitReached)
	if !errors.Is(wrapped, ErrMemberLimitReached) {
		t.Fatal("ErrMemberLimitReached is not matchable through errors.Is")
	}
	if errors.Is(ErrForbidden, ErrMemberLimitReached) {
		t.Fatal("distinct sentinels must not match each other")
	}
}
