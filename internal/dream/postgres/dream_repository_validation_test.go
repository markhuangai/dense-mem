package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
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
	var repo Store
	_, err := repo.GetHypothesis(context.Background(), GetHypothesisInput{
		TeamID:       uuid.NewString(),
		HypothesisID: "not-a-uuid",
	})
	require.ErrorIs(t, err, ErrDreamHypothesisIDInvalid)
}

func TestDreamListSortValidation(t *testing.T) {
	input := normalizeListHypothesesInput(ListHypothesesInput{
		TeamID:    uuid.NewString(),
		Sort:      " CREATED_AT ",
		Direction: " ASC ",
	})
	require.NoError(t, validateListHypothesesInput(input))
	assert.Equal(t, "created_at ASC", hypothesisListOrder(input.Sort, input.Direction))

	for _, invalid := range []ListHypothesesInput{
		{TeamID: uuid.NewString(), Sort: "updated_at; DROP TABLE hypotheses", Direction: "asc"},
		{TeamID: uuid.NewString(), Sort: "updated_at", Direction: "desc NULLS FIRST"},
		{TeamID: uuid.NewString(), Sort: "last_evaluated_at", Direction: "desc"},
	} {
		normalized := normalizeListHypothesesInput(invalid)
		require.Error(t, validateListHypothesesInput(normalized))
	}
}

func TestStaleDreamSourceErrorMapsMissingRelationship(t *testing.T) {
	require.ErrorIs(t, staleDreamSourceError(sql.ErrNoRows, uuid.NewString()), ErrDreamSourceStale)
}

func TestUpdateHypothesisStatusValidationBindsDecisionToStatus(t *testing.T) {
	input := UpdateHypothesisStatusInput{
		TeamID:         uuid.NewString(),
		ActorProfileID: uuid.NewString(),
		HypothesisID:   uuid.NewString(),
	}
	for _, tc := range []struct {
		name     string
		status   string
		decision string
		wantErr  string
	}{
		{name: "reject", status: "rejected", decision: "reject"},
		{name: "stale", status: "stale", decision: "stale"},
		{name: "reinforce", status: "reinforced", decision: "reinforce"},
		{name: "contradicting", status: "rejected", decision: "reinforce", wantErr: `decision "reinforce" requires status "reinforced"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input.Status = tc.status
			input.Decision = tc.decision
			err := validateUpdateHypothesisStatusInput(input)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
