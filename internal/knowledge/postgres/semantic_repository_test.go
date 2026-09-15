package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSemanticRepositoryFailsClosedWithoutDependencies(t *testing.T) {
	_, err := (*Store)(nil).CreateEntity(context.Background(), CreateEntityInput{
		TeamID:         "f9f8b369-3240-44b8-a9b1-64ad3b56bcab",
		OwnerProfileID: "5d285966-87d9-47b1-b1f7-1c7bb1415de4",
		EntityKind:     "person",
		CanonicalName:  "Mark",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database is required")

	repo := &Store{db: &gorm.DB{}}
	_, err = repo.CreateEntity(context.Background(), CreateEntityInput{
		TeamID:         "f9f8b369-3240-44b8-a9b1-64ad3b56bcab",
		OwnerProfileID: "5d285966-87d9-47b1-b1f7-1c7bb1415de4",
		EntityKind:     "person",
		CanonicalName:  "Mark",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rls helper is required")
}

func TestSemanticApplyRelationshipDecisionValidationRejectsMalformedSupportForAnyVerdict(t *testing.T) {
	input := validApplyRelationshipDecisionInput()
	input.EvidenceVerdict = "insufficient"
	input.Support = &EvidenceSupportInput{
		FragmentID:     uuid.NewString(),
		SourceID:       uuid.NewString(),
		SourceGroupKey: "conversation:1",
		SpanStart:      0,
		SpanEnd:        5,
		Authority:      "primary",
	}

	err := validateApplyRelationshipDecisionInput(normalizeApplyRelationshipDecisionInput(input))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "source_id and source_revision_id must be provided together")
}

func TestSemanticApplyRelationshipDecisionValidationRejectsLegacySupportAuthority(t *testing.T) {
	input := validApplyRelationshipDecisionInput()
	input.Support.Authority = "derived"

	err := validateApplyRelationshipDecisionInput(normalizeApplyRelationshipDecisionInput(input))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported support authority")
}

func TestSemanticApplyRelationshipDecisionValidationRequiresEntailedSupport(t *testing.T) {
	input := validApplyRelationshipDecisionInput()
	input.Support = nil

	err := validateApplyRelationshipDecisionInput(normalizeApplyRelationshipDecisionInput(input))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "entailed relationship decisions require support")
}

func TestSemanticApplyRelationshipDecisionValidationAllowsStructuralRememberAcceptance(t *testing.T) {
	input := validApplyRelationshipDecisionInput()
	input.AssessorAccepted = true
	input.AssessmentID = uuid.NewString()
	input.EvidenceVerdict = ""
	input.Confidence = nil
	input.Rationale = ""
	input.AssessmentPolicyVersion = ""
	input.ThresholdUsed = nil
	input.GateResult = ""

	err := validateApplyRelationshipDecisionInput(normalizeApplyRelationshipDecisionInput(input))

	require.NoError(t, err)
}

func validApplyRelationshipDecisionInput() ApplyRelationshipDecisionInput {
	return ApplyRelationshipDecisionInput{
		TeamID:           uuid.NewString(),
		OwnerProfileID:   uuid.NewString(),
		IngestID:         uuid.NewString(),
		SubjectEntityID:  uuid.NewString(),
		PredicateKey:     "works_on",
		PredicateVersion: 1,
		ObjectEntityID:   uuid.NewString(),
		EvidenceVerdict:  "entailed",
		Support: &EvidenceSupportInput{
			FragmentID:     uuid.NewString(),
			SourceGroupKey: "conversation:1",
			SpanStart:      0,
			SpanEnd:        5,
			Authority:      "primary",
		},
	}
}
