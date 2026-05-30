package domain

import (
	"testing"
	"time"
)

func TestSecurityIPBanActiveAt(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	before := now.Add(-time.Minute)
	after := now.Add(time.Minute)

	cases := []struct {
		name string
		ban  SecurityIPBan
		want bool
	}{
		{name: "revoked", ban: SecurityIPBan{RevokedAt: &before, ExpiresAt: &after}, want: false},
		{name: "permanent", ban: SecurityIPBan{}, want: true},
		{name: "expired", ban: SecurityIPBan{ExpiresAt: &before}, want: false},
		{name: "future expiry", ban: SecurityIPBan{ExpiresAt: &after}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ban.ActiveAt(now); got != tc.want {
				t.Fatalf("ActiveAt() = %v; want %v", got, tc.want)
			}
		})
	}
}
