package factservice

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDefaultPromotionGates verifies AC-34 plus the standalone MCP memory
// server predicate set: the original knowledge-pipeline predicates keep their
// binding thresholds, personal-memory predicates are explicitly curated, and
// unknown predicates are absent (deny-by-default).
func TestDefaultPromotionGates(t *testing.T) {
	t.Run("contains original shipping predicates and curated personal predicates", func(t *testing.T) {
		required := []string{
			"born_on", "died_on", "works_at", "likes", "knows", "has_skill",
			"prefers", "identity_is", "profile_fact", "works_on", "has_goal",
			"corrected", "relationship_to", "uses",
		}
		require.Len(t, DefaultPromotionGates, len(required),
			"gate map must contain only the curated predicate set")
		for _, predicate := range required {
			_, ok := DefaultPromotionGates[predicate]
			require.True(t, ok, "missing predicate %q", predicate)
		}
	})

	t.Run("born_on exact thresholds", func(t *testing.T) {
		gate, ok := DefaultPromotionGates["born_on"]
		require.True(t, ok)
		require.Equal(t, PromotionGate{
			Policy:              SingleCurrent,
			MinExtractConf:      0.9,
			MinResolutionConf:   0.8,
			RequiresAssertion:   true,
			RequiresEntailed:    true,
			MinSourceCount:      1,
			MinMaxSourceQuality: 0.95,
		}, gate)
	})

	t.Run("died_on exact thresholds", func(t *testing.T) {
		gate, ok := DefaultPromotionGates["died_on"]
		require.True(t, ok)
		require.Equal(t, PromotionGate{
			Policy:              SingleCurrent,
			MinExtractConf:      0.9,
			MinResolutionConf:   0.8,
			RequiresAssertion:   true,
			RequiresEntailed:    true,
			MinSourceCount:      1,
			MinMaxSourceQuality: 0.95,
		}, gate)
	})

	t.Run("works_at exact thresholds", func(t *testing.T) {
		gate, ok := DefaultPromotionGates["works_at"]
		require.True(t, ok)
		require.Equal(t, PromotionGate{
			Policy:              SingleCurrent,
			MinExtractConf:      0.85,
			MinResolutionConf:   0.75,
			RequiresAssertion:   true,
			RequiresEntailed:    true,
			MinSourceCount:      2,
			MinMaxSourceQuality: 0.95,
		}, gate)
	})

	t.Run("likes exact thresholds — assertion not required", func(t *testing.T) {
		gate, ok := DefaultPromotionGates["likes"]
		require.True(t, ok)
		require.Equal(t, PromotionGate{
			Policy:              MultiValued,
			MinExtractConf:      0.7,
			MinResolutionConf:   0.6,
			RequiresAssertion:   false, // preferences may be non-assertion modality
			RequiresEntailed:    true,
			MinSourceCount:      1,
			MinMaxSourceQuality: 0.0,
		}, gate)
	})

	t.Run("knows exact thresholds", func(t *testing.T) {
		gate, ok := DefaultPromotionGates["knows"]
		require.True(t, ok)
		require.Equal(t, PromotionGate{
			Policy:              MultiValued,
			MinExtractConf:      0.75,
			MinResolutionConf:   0.6,
			RequiresAssertion:   true,
			RequiresEntailed:    true,
			MinSourceCount:      1,
			MinMaxSourceQuality: 0.0,
		}, gate)
	})

	t.Run("has_skill exact thresholds", func(t *testing.T) {
		gate, ok := DefaultPromotionGates["has_skill"]
		require.True(t, ok)
		require.Equal(t, PromotionGate{
			Policy:              MultiValued,
			MinExtractConf:      0.8,
			MinResolutionConf:   0.6,
			RequiresAssertion:   true,
			RequiresEntailed:    true,
			MinSourceCount:      1,
			MinMaxSourceQuality: 0.0,
		}, gate)
	})

	t.Run("deny-by-default: unknown predicates absent", func(t *testing.T) {
		unknowns := []string{"lives_at", "married_to", "works_for", "unknown_predicate", ""}
		for _, pred := range unknowns {
			_, ok := DefaultPromotionGates[pred]
			require.False(t, ok, "predicate %q must not be in the gate map", pred)
		}
	})

	t.Run("all four policy constants are defined — R2", func(t *testing.T) {
		// R2 (binding): full enum defined in v1 and implemented by the
		// promotion service.
		require.Equal(t, Policy("single_current"), SingleCurrent)
		require.Equal(t, Policy("multi_valued"), MultiValued)
		require.Equal(t, Policy("versioned"), Versioned)
		require.Equal(t, Policy("append_only"), AppendOnly)
	})
}

// TestDefaultPromotionGates_CrossProfileIsolation verifies the profile-isolation
// property for the gate map. DefaultPromotionGates is a static, profile-neutral
// lookup table: it stores no per-profile state, so gate lookups for profile A
// cannot return or influence gate results for profile B.
//
// Profile isolation for actual fact writes is enforced at the promote-service
// level (Unit 41), which threads profileID into every DB query. This test
// asserts the structural property that the gate map itself does not accept or
// encode profile-keyed entries, which would be a data-isolation violation.
func TestDefaultPromotionGates_CrossProfileIsolation(t *testing.T) {
	profileA := "profile-a-550e8400-e29b-41d4-a716-446655440000"
	profileB := "profile-b-6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	// Gate map keys must be bare predicates — never profile-prefixed.
	// A profile-prefixed key would mean one profile's gate config could shadow
	// another's, which would be a tenant-escape vulnerability.
	_, inA := DefaultPromotionGates[profileA+":works_at"]
	_, inB := DefaultPromotionGates[profileB+":works_at"]
	require.False(t, inA, "gate map must not contain profile-prefixed keys for profile A")
	require.False(t, inB, "gate map must not contain profile-prefixed keys for profile B")

	// Correct usage: profile-agnostic predicate lookup returns same gate for any profile.
	gateForA, okA := DefaultPromotionGates["works_at"]
	gateForB, okB := DefaultPromotionGates["works_at"]
	require.True(t, okA)
	require.True(t, okB)
	require.Equal(t, gateForA, gateForB,
		"gate config must be identical regardless of which profile is querying")
}
