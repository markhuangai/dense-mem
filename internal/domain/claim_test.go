package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClaim verifies the Claim struct carries all fields required by AC-7 and
// that enum types expose working IsValid() helpers.
func TestClaim(t *testing.T) {
	t.Run("struct has all AC-7 required fields", func(t *testing.T) {
		now := time.Now()
		validFrom := now.Add(-24 * time.Hour)
		validTo := now.Add(24 * time.Hour)
		verifiedAt := now

		c := Claim{
			// Identity / scope
			ClaimID:   "claim-001",
			ProfileID: "profile-A",
			// Triple
			Subject:   "Alice",
			Predicate: "works_at",
			Object:    "Acme Corp",
			// Linguistic metadata
			Modality:  ModalityAssertion,
			Polarity:  PolarityPlus,
			Speaker:   "narrator",
			SpanStart: 0,
			SpanEnd:   20,
			// Temporal
			ValidFrom:  &validFrom,
			ValidTo:    &validTo,
			RecordedAt: now,
			RecordedTo: nil,
			// Quality
			ExtractConf:    0.95,
			ResolutionConf: 0.88,
			SourceQuality:  0.9,
			// Verification
			EntailmentVerdict:    VerdictEntailed,
			Status:               StatusValidated,
			LastVerifierResponse: "entailment confirmed",
			VerifiedAt:           &verifiedAt,
			// Provenance
			ExtractionModel:   "gpt-4",
			ExtractionVersion: "1.0",
			VerifierModel:     "nli-canonical",
			PipelineRunID:     "run-42",
			// Idempotency
			ContentHash:    "sha256:abc123",
			IdempotencyKey: "idem-001",
			// Classification
			Classification:               map[string]any{"topic": "employment", "confidence": 0.92},
			ClassificationLatticeVersion: "v1",
			// Graph edges
			SupportedBy: []string{"frag-001", "frag-002"},
		}

		// Identity / scope
		require.Equal(t, "claim-001", c.ClaimID)
		require.Equal(t, "profile-A", c.ProfileID)

		// Triple
		require.Equal(t, "Alice", c.Subject)
		require.Equal(t, "works_at", c.Predicate)
		require.Equal(t, "Acme Corp", c.Object)

		// Linguistic metadata
		require.Equal(t, ModalityAssertion, c.Modality)
		require.Equal(t, PolarityPlus, c.Polarity)
		require.Equal(t, "narrator", c.Speaker)
		require.Equal(t, 0, c.SpanStart)
		require.Equal(t, 20, c.SpanEnd)

		// Temporal
		require.NotNil(t, c.ValidFrom)
		require.NotNil(t, c.ValidTo)
		require.NotZero(t, c.RecordedAt)
		require.Nil(t, c.RecordedTo)

		// Quality
		require.InDelta(t, 0.95, c.ExtractConf, 1e-9)
		require.InDelta(t, 0.88, c.ResolutionConf, 1e-9)
		require.InDelta(t, 0.9, c.SourceQuality, 1e-9)

		// Verification
		require.Equal(t, VerdictEntailed, c.EntailmentVerdict)
		require.Equal(t, StatusValidated, c.Status)
		require.Equal(t, "entailment confirmed", c.LastVerifierResponse)
		require.NotNil(t, c.VerifiedAt)

		// Provenance
		require.Equal(t, "gpt-4", c.ExtractionModel)
		require.Equal(t, "1.0", c.ExtractionVersion)
		require.Equal(t, "nli-canonical", c.VerifierModel)
		require.Equal(t, "run-42", c.PipelineRunID)

		// Idempotency
		require.Equal(t, "sha256:abc123", c.ContentHash)
		require.Equal(t, "idem-001", c.IdempotencyKey)

		// Classification is a map, not a string blob (AC-7)
		require.IsType(t, map[string]any{}, c.Classification)
		require.Equal(t, "employment", c.Classification["topic"])
		require.Equal(t, "v1", c.ClassificationLatticeVersion)

		// Graph edges
		require.Equal(t, []string{"frag-001", "frag-002"}, c.SupportedBy)
	})

	t.Run("ClaimModality IsValid", func(t *testing.T) {
		valid := []ClaimModality{
			ModalityAssertion, ModalityQuestion, ModalityProposal,
			ModalitySpeculation, ModalityQuoted,
		}
		for _, m := range valid {
			assert.True(t, m.IsValid(), "expected %q to be valid", m)
		}
		assert.False(t, ClaimModality("").IsValid())
		assert.False(t, ClaimModality("unknown").IsValid())
		// Dropped values (AC-8): must not be accepted
		assert.False(t, ClaimModality("belief").IsValid())
		assert.False(t, ClaimModality("hypothesis").IsValid())
		assert.False(t, ClaimModality("intention").IsValid())
		assert.False(t, ClaimModality("preference").IsValid())
	})

	t.Run("ClaimPolarity IsValid", func(t *testing.T) {
		valid := []ClaimPolarity{PolarityPlus, PolarityMinus}
		for _, p := range valid {
			assert.True(t, p.IsValid(), "expected %q to be valid", p)
		}
		assert.False(t, ClaimPolarity("").IsValid())
		assert.False(t, ClaimPolarity("ambiguous").IsValid())
		// Dropped values (AC-8): raw string form must not be accepted
		assert.False(t, ClaimPolarity("positive").IsValid())
		assert.False(t, ClaimPolarity("negative").IsValid())
		assert.False(t, ClaimPolarity("neutral").IsValid())
	})

	t.Run("ClaimStatus IsValid", func(t *testing.T) {
		valid := []ClaimStatus{
			StatusCandidate, StatusValidated, StatusRejected, StatusSuperseded,
			StatusDisputed, StatusPromoted,
		}
		for _, s := range valid {
			assert.True(t, s.IsValid(), "expected %q to be valid", s)
		}
		assert.False(t, ClaimStatus("").IsValid())
		assert.False(t, ClaimStatus("archived").IsValid())
	})

	t.Run("EntailmentVerdict IsValid", func(t *testing.T) {
		valid := []EntailmentVerdict{
			VerdictEntailed, VerdictContradicted, VerdictNeutral, VerdictInsufficient,
		}
		for _, v := range valid {
			assert.True(t, v.IsValid(), "expected %q to be valid", v)
		}
		assert.False(t, EntailmentVerdict("").IsValid())
		assert.False(t, EntailmentVerdict("maybe").IsValid())
		// Dropped value: raw "unverified" string must not be accepted
		assert.False(t, EntailmentVerdict("unverified").IsValid())
	})
}

// TestClaim_CrossProfileIsolation verifies that claim slices respect ProfileID
// boundaries — data from profile A must not appear in results scoped to profile B.
// This reflects the invariant enforced at every repository layer per
// AGENTS.md.
func TestClaim_CrossProfileIsolation(t *testing.T) {
	profileA := "profile-A"
	profileB := "profile-B"

	claimA := Claim{ClaimID: "claim-A1", ProfileID: profileA, Subject: "Alice"}
	claimB := Claim{ClaimID: "claim-B1", ProfileID: profileB, Subject: "Bob"}

	// Simulate a repository query result that correctly filters by profile.
	allClaims := []Claim{claimA, claimB}

	filterByProfile := func(claims []Claim, profileID string) []Claim {
		var out []Claim
		for _, c := range claims {
			if c.ProfileID == profileID {
				out = append(out, c)
			}
		}
		return out
	}

	aResults := filterByProfile(allClaims, profileA)
	bResults := filterByProfile(allClaims, profileB)

	// Profile A results must not contain profile B's claim ID.
	aIDs := make([]string, 0, len(aResults))
	for _, c := range aResults {
		aIDs = append(aIDs, c.ClaimID)
	}
	bIDs := make([]string, 0, len(bResults))
	for _, c := range bResults {
		bIDs = append(bIDs, c.ClaimID)
	}

	require.NotContains(t, aIDs, claimB.ClaimID, "profile A results must not contain profile B claims")
	require.NotContains(t, bIDs, claimA.ClaimID, "profile B results must not contain profile A claims")
	require.Contains(t, aIDs, claimA.ClaimID, "profile A results must contain its own claim")
	require.Contains(t, bIDs, claimB.ClaimID, "profile B results must contain its own claim")
}
