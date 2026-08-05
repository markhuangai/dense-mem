package memoryservice

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmissionAssessmentPlanPreservesValidatedReviewContext(t *testing.T) {
	ledger, _, _, _, _ := submissionAssessmentWorkerFixture(t)
	relationships := ledger.placement.Proposal["relationship_hints"].([]any)
	first := relationships[0].(map[string]any)
	second := relationships[1].(map[string]any)
	first["correction_target"] = map[string]any{
		"relationship_id":  uuid.NewString(),
		"expected_version": 3,
	}
	second["conflict_context"] = map[string]any{
		"conflict_id":      uuid.NewString(),
		"expected_version": 4,
	}

	plan, err := buildSubmissionAssessmentPlan(ledger.placement)

	require.NoError(t, err)
	require.NotNil(t, plan.relationshipsByRef["r:uses"].CorrectionTarget)
	assert.Equal(t, 3, plan.relationshipsByRef["r:uses"].CorrectionTarget.ExpectedVersion)
	require.NotNil(t, plan.relationshipsByRef["r:depends"].ConflictContext)
	assert.Equal(t, 4, plan.relationshipsByRef["r:depends"].ConflictContext.ExpectedVersion)
}

func TestSubmissionAssessmentPlanPrefersKnownEntityIDForDuplicateMention(t *testing.T) {
	ledger, _, _, _, _ := submissionAssessmentWorkerFixture(t)
	relationships := ledger.placement.Proposal["relationship_hints"].([]any)
	original := relationships[0].(map[string]any)
	knownSubject := make(map[string]any)
	for key, value := range original["subject"].(map[string]any) {
		knownSubject[key] = value
	}
	knownID := uuid.NewString()
	knownSubject["known_entity_id"] = knownID
	duplicate := make(map[string]any)
	for key, value := range original {
		duplicate[key] = value
	}
	duplicate["ref"] = "r:uses-known"
	duplicate["subject"] = knownSubject
	ledger.placement.Proposal["relationship_hints"] = append(relationships, duplicate)

	plan, err := buildSubmissionAssessmentPlan(ledger.placement)

	require.NoError(t, err)
	entity := plan.entityTargetsByRef["entity:evidence:0:0:5:concept"]
	assert.Equal(t, knownID, entity.KnownEntityID)
}

func TestSubmissionAssessmentPlanRejectsInvalidReviewContext(t *testing.T) {
	for _, field := range []string{"correction_target", "conflict_context"} {
		t.Run(field, func(t *testing.T) {
			ledger, _, _, _, _ := submissionAssessmentWorkerFixture(t)
			relationships := ledger.placement.Proposal["relationship_hints"].([]any)
			relationships[0].(map[string]any)[field] = map[string]any{"expected_version": 1}

			_, err := buildSubmissionAssessmentPlan(ledger.placement)

			require.Error(t, err)
		})
	}
}

func TestSubmissionAssessmentPlanRejectsUnsupportedEndpointContext(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "subject outside support",
			mutate: func(relationship map[string]any) {
				relationship["supports"] = []any{map[string]any{"evidence_index": 0, "start": 0, "end": 4}}
			},
		},
		{
			name:   "unsupported modality",
			mutate: func(relationship map[string]any) { relationship["modality"] = "unsupported" },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger, _, _, _, _ := submissionAssessmentWorkerFixture(t)
			relationship := ledger.placement.Proposal["relationship_hints"].([]any)[0].(map[string]any)
			test.mutate(relationship)

			_, err := buildSubmissionAssessmentPlan(ledger.placement)

			require.Error(t, err)
		})
	}
}

func TestSubmissionAssessmentValueProposalRejectsUnsafeFields(t *testing.T) {
	ledger, _, _, _, _ := submissionAssessmentWorkerFixture(t)
	plan, err := buildSubmissionAssessmentPlan(submissionAssessmentValueFixturePlacement(t, ledger.run))
	require.NoError(t, err)
	items := plan.itemsByEvidenceID

	base := map[string]any{
		"surface": "42 ms",
		"type":    "number",
		"value":   42,
		"span":    map[string]any{"evidence_index": 0, "start": 11, "end": 16},
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unsupported type", mutate: func(value map[string]any) { value["type"] = "unsupported" }},
		{name: "missing canonical value", mutate: func(value map[string]any) { value["value"] = []string{"unsupported"} }},
		{name: "display is too long", mutate: func(value map[string]any) { value["display"] = strings.Repeat("x", 4097) }},
		{name: "unit is too long", mutate: func(value map[string]any) { value["unit"] = strings.Repeat("x", 129) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := make(map[string]any, len(base))
			for key, item := range base {
				value[key] = item
			}
			test.mutate(value)
			_, _, _, _, err := submissionAssessmentValueFromProposal(value, items)
			require.Error(t, err)
		})
	}
}

func TestSubmissionAssessmentSupportsFromProposalRejectsUnsafeSpans(t *testing.T) {
	ledger, _, _, _, _ := submissionAssessmentWorkerFixture(t)
	plan, err := buildSubmissionAssessmentPlan(ledger.placement)
	require.NoError(t, err)

	valid := map[string]any{"evidence_index": 0, "start": 0, "end": 16}
	tests := []map[string]any{
		{"supports": []map[string]any{valid, valid}},
		{"supports": []map[string]any{{"evidence_index": 99, "start": 0, "end": 1}}},
		{"supports": []map[string]any{{"evidence_index": 0, "start": 0, "end": 999}}},
		{"supports": []any{map[string]any{"evidence_index": "invalid", "start": 0, "end": 1}}},
	}
	for _, raw := range tests {
		_, err := submissionAssessmentSupportsFromProposal(raw, plan.itemsByEvidenceID)
		require.Error(t, err)
	}
}
