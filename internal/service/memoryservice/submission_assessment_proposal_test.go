package memoryservice

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
)

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
