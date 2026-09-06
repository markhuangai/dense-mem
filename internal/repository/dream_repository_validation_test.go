package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateEvidenceDiscoveryHypothesisUsesCanonicalAuthority(t *testing.T) {
	teamID := uuid.NewString()
	runID := uuid.NewString()
	subjectID := uuid.NewString()
	objectID := uuid.NewString()
	evidenceID := uuid.NewString()
	input := normalizeUpsertHypothesisInput(UpsertHypothesisInput{
		TeamID: teamID, RunID: runID, Lane: "evidence_discovery",
		Statement: "A may use B.", SubjectEntityID: subjectID, PredicateKey: "uses", PredicateVersion: 1,
		ObjectEntityID: objectID, ContentHash: "sha256:test", TargetIdentity: hypothesisTargetIdentity(teamID, subjectID, "uses", objectID, ""),
		SourceEvidenceIDs: []string{evidenceID}, EvidenceDerivations: []EvidenceDerivationSource{{
			EvidenceID: evidenceID, FragmentID: evidenceID, SourceGroupKey: "ingest:test", SpanStart: 0, SpanEnd: 1,
			Quote: "A", Authority: "derived",
		}},
	})
	require.ErrorContains(t, validateUpsertHypothesisInput(input, true), "evidence_derivations[0].authority is unsupported")
}

func TestGetHypothesisClassifiesInvalidHypothesisID(t *testing.T) {
	var repo SemanticRepositoryImpl
	_, err := repo.GetHypothesis(context.Background(), GetHypothesisInput{
		TeamID:       uuid.NewString(),
		HypothesisID: "not-a-uuid",
	})
	require.ErrorIs(t, err, ErrDreamHypothesisIDInvalid)
}
