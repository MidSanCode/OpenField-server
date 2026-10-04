package main

import (
	"testing"
)

// A banned account may still reach the few endpoints it needs to understand its
// status, but nothing that acts on the service.
func TestBanAllowedPath(t *testing.T) {
	allowed := [][2]string{
		{"GET", "/api/v1/users/me"},
		{"GET", "/api/v1/users/me/punishments"},
		{"POST", "/api/v1/auth/logout"},
		{"POST", "/api/v1/auth/refresh"},
	}
	for _, c := range allowed {
		if !banAllowedPath(c[0], c[1]) {
			t.Errorf("expected %s %s to be allowed for a banned user", c[0], c[1])
		}
	}

	denied := [][2]string{
		{"POST", "/api/v1/posts"},
		{"POST", "/api/v1/chat/conversations"},
		{"POST", "/api/v1/account/transfer"},
		{"DELETE", "/api/v1/users/me"},
		{"POST", "/api/v1/account/punish/5"},
		{"GET", "/api/v1/users"},
		{"POST", "/api/v1/attachments"},
		{"PUT", "/api/v1/users/me"},
	}
	for _, c := range denied {
		if banAllowedPath(c[0], c[1]) {
			t.Errorf("expected %s %s to be denied for a banned user", c[0], c[1])
		}
	}
}
