package repository

import (
	"strings"
	"testing"
)

func TestSecurityBanListWhereClauseAlwaysExcludesRevoked(t *testing.T) {
	for _, includeExpired := range []bool{false, true} {
		where := securityBanListWhereClause(includeExpired)
		if !strings.Contains(where, "revoked_at IS NULL") {
			t.Fatalf("where clause %q should exclude revoked bans", where)
		}
	}
}

func TestSecurityBanListWhereClauseExpiredFilterIsOptional(t *testing.T) {
	activeOnly := securityBanListWhereClause(false)
	if !strings.Contains(activeOnly, "expires_at IS NULL OR expires_at > NOW()") {
		t.Fatalf("active-only where clause %q should filter expired bans", activeOnly)
	}

	includeExpired := securityBanListWhereClause(true)
	if strings.Contains(includeExpired, "expires_at") {
		t.Fatalf("include-expired where clause %q should not filter expired bans", includeExpired)
	}
}
