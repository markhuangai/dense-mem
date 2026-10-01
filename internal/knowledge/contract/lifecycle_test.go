package contract

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestStatusForEffectiveSupport(t *testing.T) {
	tests := []struct {
		status, withoutSupport, withSupport string
	}{
		{"active", "pending_evidence", "active"},
		{"pending_evidence", "pending_evidence", "active"},
		{"needs_review", "needs_review", "needs_review"},
		{"rejected", "rejected", "rejected"},
		{"retracted", "retracted", "retracted"},
		{"superseded", "superseded", "superseded"},
		{"quarantined", "quarantined", "quarantined"},
		{"disputed", "disputed", "disputed"},
		{"unknown", "unknown", "unknown"},
	}
	covered := make(map[string]bool)
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			require.Equal(t, test.withoutSupport, StatusForEffectiveSupport(test.status, 0))
			require.Equal(t, test.withSupport, StatusForEffectiveSupport(test.status, 1))
			require.Equal(t, test.withSupport, StatusForEffectiveSupport(test.status, 2))
		})
		covered[test.status] = true
	}
	for _, status := range domain.RelationshipStatuses() {
		require.True(t, covered[status], "native status %q needs an explicit lifecycle expectation", status)
	}
}

func TestRelationshipEligibleForCorrection(t *testing.T) {
	require.False(t, RelationshipEligibleForCorrection(nil))
	for _, status := range domain.RelationshipStatuses() {
		t.Run(status, func(t *testing.T) {
			for _, supportCount := range []int{0, 1, 2} {
				record := RelationshipRecord{Status: status, SupportCount: supportCount}
				before := record
				require.Equal(t, status == "active" && supportCount > 0, RelationshipEligibleForCorrection(&record))
				require.Equal(t, before, record)
				record.IdentityAliasOfID = "canonical-relationship"
				require.False(t, RelationshipEligibleForCorrection(&record), "aliased records cannot be corrected")
			}
		})
	}
}

func TestRelationshipEligibleForConflictPlacement(t *testing.T) {
	tests := []struct {
		name   string
		record *RelationshipRecord
		want   bool
	}{
		{"nil", nil, false},
		{"active_single_state", &RelationshipRecord{Status: "active", SupportCount: 1, RelationshipKind: "state", CurrentCardinality: "one"}, true},
		{"multiple_supports", &RelationshipRecord{Status: "active", SupportCount: 2, RelationshipKind: "state", CurrentCardinality: "one"}, true},
		{"unsupported", &RelationshipRecord{Status: "active", RelationshipKind: "state", CurrentCardinality: "one"}, false},
		{"many_state", &RelationshipRecord{Status: "active", SupportCount: 1, RelationshipKind: "state", CurrentCardinality: "many"}, false},
		{"event", &RelationshipRecord{Status: "active", SupportCount: 1, RelationshipKind: "event", CurrentCardinality: "one"}, false},
		{"alias_preserves_placement_gate", &RelationshipRecord{Status: "active", SupportCount: 1, RelationshipKind: "state", CurrentCardinality: "one", IdentityAliasOfID: "canonical-relationship"}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, RelationshipEligibleForConflictPlacement(test.record))
		})
	}
	for _, status := range domain.RelationshipStatuses() {
		record := RelationshipRecord{Status: status, SupportCount: 1, RelationshipKind: "state", CurrentCardinality: "one"}
		require.Equal(t, status == "active", RelationshipEligibleForConflictPlacement(&record), status)
	}
}
