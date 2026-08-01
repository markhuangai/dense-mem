package memoryservice

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestSubmissionProposalObjectValuePreservesJSONNumberPrecision(t *testing.T) {
	value, ok := submissionProposalObjectValue(map[string]any{
		"type":  "number",
		"value": json.Number("9007199254740993"),
	})
	require.True(t, ok)
	require.Equal(t, "9007199254740993", value.CanonicalValue)
}

func TestSubmissionProposalObjectValueAcceptsOnlyFiniteScalarBindings(t *testing.T) {
	for _, testCase := range []struct {
		name string
		raw  any
		want string
		ok   bool
	}{
		{name: "string", raw: "PostgreSQL", want: "PostgreSQL", ok: true},
		{name: "bool", raw: true, want: "true", ok: true},
		{name: "float64", raw: 1.25, want: "1.25", ok: true},
		{name: "float32", raw: float32(2.5), want: "2.5", ok: true},
		{name: "int", raw: 3, want: "3", ok: true},
		{name: "int32", raw: int32(4), want: "4", ok: true},
		{name: "int64", raw: int64(5), want: "5", ok: true},
		{name: "json number", raw: json.Number("6.75"), want: "6.75", ok: true},
		{name: "nan", raw: math.NaN()},
		{name: "infinity", raw: math.Inf(1)},
		{name: "out of range json number", raw: json.Number("1e1000")},
		{name: "object", raw: map[string]any{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := submissionProposalCanonicalValue(testCase.raw)
			require.Equal(t, testCase.ok, ok)
			if testCase.ok {
				require.Equal(t, testCase.want, got)
			}
		})
	}

	for _, fields := range []map[string]any{
		nil,
		{"type": "string"},
		{"type": "string", "value": "PostgreSQL", "display": 1},
		{"type": "string", "value": "PostgreSQL", "unit": 1},
	} {
		value, ok := submissionProposalObjectValue(fields)
		require.False(t, ok)
		require.Nil(t, value)
	}

	value, ok := submissionProposalObjectValue(map[string]any{
		"type": "string", "value": "PostgreSQL", "display": " PostgreSQL ", "unit": " database ",
	})
	require.True(t, ok)
	require.Equal(t, "PostgreSQL", *value.Display)
	require.Equal(t, "database", *value.Unit)
}

func TestSubmissionAssessmentRequiredProposalBindsCompleteObjectValue(t *testing.T) {
	display := "PostgreSQL"
	unit := "database"
	required := submissionAssessmentRequiredProposal(submissionAssessmentProposal{Relationships: []submissionAssessmentRelationshipProposal{{
		ProposalID: "relationship", ObjectValue: map[string]any{
			"type": "string", "value": "PostgreSQL", "display": display, "unit": unit,
		},
	}}})
	require.Len(t, required.Relationships, 1)
	got := required.Relationships[0]
	require.Equal(t, "string", got.ObjectValueType)
	require.Equal(t, "PostgreSQL", got.ObjectValueCanonical)
	require.Equal(t, &display, got.ObjectValueDisplay)
	require.Equal(t, &unit, got.ObjectValueUnit)
}

func TestSubmissionStageRememberRequestRevalidatesStoredProposal(t *testing.T) {
	request := submissionContractValidRequest()
	staged := &repository.Submission{
		Evidence: []repository.SubmissionEvidence{{
			EvidenceIndex:           0,
			Content:                 request.Evidence[0].Content,
			SubmissionEvidenceInput: submissionRepositoryEvidence(request.Evidence[0]),
		}},
		Proposal: map[string]any{
			"entities":      request.EntityHints,
			"relationships": request.RelationshipHints,
		},
	}

	got, proposal, err := submissionStageRememberRequest(staged)
	require.NoError(t, err)
	require.Equal(t, request.Evidence[0].Content, got.Evidence[0].Content)
	require.Empty(t, got.Evidence[0].Metadata)
	require.Len(t, proposal.Entities, 2)
	require.Len(t, proposal.Relationships, 1)
	require.Equal(t, "rel:uses", proposal.Relationships[0].ProposalID)
	require.Equal(t, "+", proposal.Relationships[0].Polarity)
	require.Equal(t, "statement", proposal.Relationships[0].Modality)

	required := submissionAssessmentRequiredProposal(proposal)
	require.Equal(t, "evidence:0", required.Entities[0].EvidenceID)
	require.Equal(t, "uses", required.Relationships[0].OriginalPredicate)
	require.Equal(t, "evidence:0", required.Relationships[0].PredicateEvidenceID)
	require.Equal(t, "evidence:0", required.Relationships[0].Evidence[0].EvidenceID)
}

func TestSubmissionStageRememberRequestRejectsMalformedStoredSubmission(t *testing.T) {
	valid := submissionContractValidRequest()
	base := repository.Submission{
		Evidence: []repository.SubmissionEvidence{{
			EvidenceIndex:           0,
			Content:                 valid.Evidence[0].Content,
			SubmissionEvidenceInput: submissionRepositoryEvidence(valid.Evidence[0]),
		}},
		Proposal: map[string]any{"entities": valid.EntityHints, "relationships": valid.RelationshipHints},
	}

	for _, testCase := range []struct {
		name string
		item *repository.Submission
		want string
	}{
		{"missing submission", nil, "staged submission is required"},
		{"missing evidence", &repository.Submission{}, "has no evidence"},
		{"noncontiguous evidence", &repository.Submission{Evidence: []repository.SubmissionEvidence{{EvidenceIndex: 1, Content: valid.Evidence[0].Content}}, Proposal: base.Proposal}, "indices are not contiguous"},
		{"missing proposal", &repository.Submission{Evidence: base.Evidence}, "staged proposal.entities is required"},
		{"missing entities", &repository.Submission{Evidence: base.Evidence, Proposal: map[string]any{"relationships": valid.RelationshipHints}}, "staged proposal.entities is required"},
		{"relationships not array", &repository.Submission{Evidence: base.Evidence, Proposal: map[string]any{"entities": valid.EntityHints, "relationships": "invalid"}}, "must be an array"},
		{"invalid proposal after recovery", &repository.Submission{Evidence: base.Evidence, Proposal: map[string]any{"entities": valid.EntityHints, "relationships": []map[string]any{}}}, "proposal.entities"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := submissionStageRememberRequest(testCase.item)
			require.ErrorContains(t, err, testCase.want)
		})
	}
}

func TestSubmissionProposalReconstructionHelpers(t *testing.T) {
	objects, err := submissionObjectArray([]map[string]any{{"ref": "one"}}, "proposal")
	require.NoError(t, err)
	require.Equal(t, []map[string]any{{"ref": "one"}}, objects)
	objects, err = submissionObjectArray([]any{map[string]any{"ref": "one"}}, "proposal")
	require.NoError(t, err)
	require.Equal(t, []map[string]any{{"ref": "one"}}, objects)
	_, err = submissionObjectArray([]map[string]any{nil}, "proposal")
	require.ErrorContains(t, err, "invalid object")
	_, err = submissionObjectArray([]any{"not an object"}, "proposal")
	require.ErrorContains(t, err, "invalid object")
	_, err = submissionObjectArray("not an array", "proposal")
	require.ErrorContains(t, err, "must be an array")

	require.Equal(t, "fallback", firstSubmissionProposalString(nil, "value", "fallback"))
	require.Equal(t, "value", firstSubmissionProposalString(map[string]any{"value": " value "}, "value", "fallback"))
	require.Equal(t, "evidence:7", submissionEvidenceID(7))

	metadata := map[string]any{"region": "team"}
	cloned := cloneSubmissionMetadata(metadata)
	metadata["region"] = "mutated"
	require.Equal(t, "team", cloned["region"])
	require.Empty(t, cloneSubmissionMetadata(nil))

	candidate := submissionAssessmentEntityCandidate(repository.SemanticReviewEntityCandidate{
		EntityID:        "entity-1",
		CanonicalName:   "Dense-Mem",
		EntityKind:      "project",
		IdentityContext: map[string]any{"region": "team"},
	})
	require.Equal(t, "entity-1", candidate.EntityID)
	require.Empty(t, candidate.IdentityContext)
}

func TestParseSubmissionAssessmentProposalRejectsInvalidRecoveredMaps(t *testing.T) {
	request := submissionContractValidRequest()
	request.EntityHints[0]["evidence"] = []any{}
	_, err := parseSubmissionAssessmentProposal(request)
	require.ErrorContains(t, err, "entity proposal is invalid")

	request = submissionContractValidRequest()
	delete(request.RelationshipHints[0], "predicate")
	_, err = parseSubmissionAssessmentProposal(request)
	require.ErrorContains(t, err, "relationship predicate is invalid")

	request = submissionContractValidRequest()
	request.RelationshipHints[0]["predicate"] = map[string]any{"evidence_index": 0, "start": 1, "end": 1}
	_, err = parseSubmissionAssessmentProposal(request)
	require.ErrorContains(t, err, "relationship predicate is invalid")

	request = submissionContractValidRequest()
	request.RelationshipHints[0]["evidence"] = []any{}
	_, err = parseSubmissionAssessmentProposal(request)
	require.ErrorContains(t, err, "relationship evidence is invalid")
}
