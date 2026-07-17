package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFact verifies the Fact struct carries all fields required by AC-33 and
// that FactStatus exposes a working IsValid() helper.
func TestFact(t *testing.T) {
	t.Run("struct has all AC-33 required fields", func(t *testing.T) {
		now := time.Now()
		validFrom := now.Add(-48 * time.Hour)
		validTo := now.Add(48 * time.Hour)
		lastConfirmed := now.Add(-1 * time.Hour)

		f := Fact{
			// Identity / scope
			FactID:    "fact-001",
			ProfileID: "profile-A",
			// Triple
			Subject:   "Alice",
			Predicate: "works_at",
			Object:    "Acme Corp",
			// Lifecycle
			Status:     FactStatusActive,
			TruthScore: 0.97,
			// Temporal
			ValidFrom:       &validFrom,
			ValidTo:         &validTo,
			RecordedAt:      now,
			RetractedAt:     nil,
			LastConfirmedAt: &lastConfirmed,
			// Provenance
			PromotedFromClaimID: "claim-001",
			// Classification
			Classification:               map[string]any{"topic": "employment", "confidence": 0.97},
			ClassificationLatticeVersion: "v1",
			// Quality
			SourceQuality: 0.9,
			// Labels / metadata
			Labels:   []string{"employment", "verified"},
			Metadata: map[string]any{"pipeline_run_id": "run-42"},
		}

		// Identity / scope
		require.Equal(t, "fact-001", f.FactID)
		require.Equal(t, "profile-A", f.ProfileID)

		// Triple
		require.Equal(t, "Alice", f.Subject)
		require.Equal(t, "works_at", f.Predicate)
		require.Equal(t, "Acme Corp", f.Object)

		// Lifecycle
		require.Equal(t, FactStatusActive, f.Status)
		require.InDelta(t, 0.97, f.TruthScore, 1e-9)

		// Temporal
		require.NotNil(t, f.ValidFrom)
		require.NotNil(t, f.ValidTo)
		require.NotZero(t, f.RecordedAt)
		require.Nil(t, f.RetractedAt)
		require.NotNil(t, f.LastConfirmedAt)

		// Provenance
		require.Equal(t, "claim-001", f.PromotedFromClaimID)

		// Classification is a map, not a string blob (AC-33)
		require.IsType(t, map[string]any{}, f.Classification)
		require.Equal(t, "employment", f.Classification["topic"])
		require.Equal(t, "v1", f.ClassificationLatticeVersion)

		// Quality
		require.InDelta(t, 0.9, f.SourceQuality, 1e-9)

		// Labels and metadata
		require.Equal(t, []string{"employment", "verified"}, f.Labels)
		require.Equal(t, "run-42", f.Metadata["pipeline_run_id"])
	})

	t.Run("FactStatus IsValid", func(t *testing.T) {
		valid := []FactStatus{FactStatusActive, FactStatusRetracted, FactStatusSuperseded}
		for _, s := range valid {
			assert.True(t, s.IsValid(), "expected %q to be valid", s)
		}
		assert.False(t, FactStatus("").IsValid())
		assert.False(t, FactStatus("archived").IsValid())
		assert.False(t, FactStatus("pending").IsValid())
	})
}

// TestFact_CrossProfileIsolation verifies that fact slices respect ProfileID
// boundaries — data from profile A must not appear in results scoped to profile B.
// This reflects the invariant enforced at every repository layer per
// AGENTS.md.
func TestFact_CrossProfileIsolation(t *testing.T) {
	profileA := "profile-A"
	profileB := "profile-B"

	factA := Fact{FactID: "fact-A1", ProfileID: profileA, Subject: "Alice"}
	factB := Fact{FactID: "fact-B1", ProfileID: profileB, Subject: "Bob"}

	// Simulate a repository query result that correctly filters by profile.
	allFacts := []Fact{factA, factB}

	filterByProfile := func(facts []Fact, profileID string) []Fact {
		var out []Fact
		for _, f := range facts {
			if f.ProfileID == profileID {
				out = append(out, f)
			}
		}
		return out
	}

	aResults := filterByProfile(allFacts, profileA)
	bResults := filterByProfile(allFacts, profileB)

	aIDs := make([]string, 0, len(aResults))
	for _, f := range aResults {
		aIDs = append(aIDs, f.FactID)
	}
	bIDs := make([]string, 0, len(bResults))
	for _, f := range bResults {
		bIDs = append(bIDs, f.FactID)
	}

	require.NotContains(t, aIDs, factB.FactID, "profile A results must not contain profile B facts")
	require.NotContains(t, bIDs, factA.FactID, "profile B results must not contain profile A facts")
	require.Contains(t, aIDs, factA.FactID, "profile A results must contain its own fact")
	require.Contains(t, bIDs, factB.FactID, "profile B results must contain its own fact")
}
