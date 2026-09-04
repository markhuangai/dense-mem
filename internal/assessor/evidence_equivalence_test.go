package assessor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeSemanticAssessmentSecurityResultsSupportsLegacyAliases(t *testing.T) {
	normalizeSemanticAssessmentSecurityResults(nil)
	response := &SemanticAssessmentResponse{
		SecurityResults: []SemanticAssessmentSecurityResult{
			{EvidenceID: " ev-1 ", Decision: " pass "},
			{EvidenceID: " ev-2 ", Decision: " reject ", Signals: []SemanticAssessmentSecuritySignal{{
				EvidenceID: " ", Kind: " instruction_override ", StartRef: " start ", EndRef: " end ",
			}}},
		},
		SecuritySignals: []SemanticAssessmentSecuritySignal{{
			EvidenceID: "ev-1", Kind: "prompt_secret_extraction", StartRef: "s", EndRef: "e",
		}},
	}

	normalizeSemanticAssessmentSecurityResults(response)
	require.Nil(t, response.SecurityResults)
	require.Nil(t, response.SecuritySignals)
	require.Len(t, response.EvidenceSecurityResults, 2)
	require.Equal(t, SemanticAssessmentEvidenceSecurityResult{
		EvidenceID: "ev-1", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{{
			EvidenceID: "ev-1", Kind: "prompt_secret_extraction", StartRef: "s", EndRef: "e",
		}},
	}, response.EvidenceSecurityResults[0])
	require.Equal(t, "ev-2", response.EvidenceSecurityResults[1].Signals[0].EvidenceID)
	require.Equal(t, "instruction_override", response.EvidenceSecurityResults[1].Signals[0].Kind)
	require.Equal(t, "start", response.EvidenceSecurityResults[1].Signals[0].StartRef)
	require.Equal(t, "end", response.EvidenceSecurityResults[1].Signals[0].EndRef)
}

func TestSemanticAssessmentEvidenceEquivalenceRequiresCompleteAllowlistedResults(t *testing.T) {
	request := SemanticAssessmentRequest{
		RequestID: "equivalence-request",
		TeamID:    "team",
		Evidence:  []SemanticReviewEvidence{{EvidenceID: "submitted", Content: "The service uses PostgreSQL."}},
		EvidenceEquivalenceCandidates: []SemanticAssessmentEvidenceEquivalenceCandidateGroup{
			{
				EvidenceID: "submitted",
				Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{
					{EvidenceID: "candidate-1", Content: "The service uses Postgres."},
				},
			},
		},
	}
	limits := DefaultSemanticAssessmentLimits()
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)

	valid := SemanticAssessmentResponse{
		RequestID:                  request.RequestID,
		EvidenceSecurityResults:    []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "submitted", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}},
		EvidenceEquivalenceResults: []SemanticAssessmentEvidenceEquivalenceResult{{EvidenceID: "submitted", Action: "reuse", CandidateEvidenceID: stringPointer("candidate-1")}},
		EvidenceConflictResults:    []SemanticAssessmentEvidenceConflictResult{},
		EntityResults:              []SemanticAssessmentEntityResult{},
		RelationshipResults:        []SemanticAssessmentRelationshipResult{},
	}
	_, errs = PrepareSemanticAssessmentResponse(prepared, valid, limits)
	require.Empty(t, errs)

	cases := []struct {
		name   string
		mutate func(*SemanticAssessmentResponse)
		want   string
	}{
		{
			name: "missing",
			mutate: func(response *SemanticAssessmentResponse) {
				response.EvidenceEquivalenceResults = nil
			},
			want: "evidence_equivalence_results: is required",
		},
		{
			name: "unknown candidate",
			mutate: func(response *SemanticAssessmentResponse) {
				response.EvidenceEquivalenceResults[0].CandidateEvidenceID = stringPointer("outside-allowlist")
			},
			want: "candidate_evidence_id: is outside the candidate allowlist",
		},
		{
			name: "duplicate result",
			mutate: func(response *SemanticAssessmentResponse) {
				response.EvidenceEquivalenceResults = append(response.EvidenceEquivalenceResults, response.EvidenceEquivalenceResults[0])
			},
			want: "evidence_equivalence_results[1].evidence_id: is duplicated",
		},
		{
			name: "new with candidate",
			mutate: func(response *SemanticAssessmentResponse) {
				response.EvidenceEquivalenceResults[0].Action = "new"
			},
			want: "candidate_evidence_id: must be null for new",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := valid
			response.EvidenceEquivalenceResults = append([]SemanticAssessmentEvidenceEquivalenceResult(nil), valid.EvidenceEquivalenceResults...)
			testCase.mutate(&response)
			_, errs := PrepareSemanticAssessmentResponse(prepared, response, limits)
			require.NotEmpty(t, errs)
			require.Contains(t, semanticAssessmentJoinedErrors(errs), testCase.want)
		})
	}
}

func TestSemanticAssessmentEvidenceEquivalenceBoundsCandidateAllowlist(t *testing.T) {
	candidates := make([]SemanticAssessmentEvidenceEquivalenceCandidate, 0, SemanticAssessmentMaxEvidenceEquivalenceCandidates+1)
	for index := 0; index <= SemanticAssessmentMaxEvidenceEquivalenceCandidates; index++ {
		candidates = append(candidates, SemanticAssessmentEvidenceEquivalenceCandidate{
			EvidenceID: fmt.Sprintf("candidate-%d", index), Content: fmt.Sprintf("candidate content %d", index),
		})
	}
	request := SemanticAssessmentRequest{
		RequestID: "equivalence-bound-request",
		TeamID:    "team",
		Evidence:  []SemanticReviewEvidence{{EvidenceID: "submitted", Content: "submitted content"}},
		EvidenceEquivalenceCandidates: []SemanticAssessmentEvidenceEquivalenceCandidateGroup{{
			EvidenceID: "submitted", Candidates: candidates,
		}},
	}
	_, errs := PrepareSemanticAssessmentRequest(request, DefaultSemanticAssessmentLimits())
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "evidence_equivalence_candidates[0].candidates: must contain at most 10 candidates")
}

func TestValidateSemanticAssessmentEvidenceEquivalenceCandidatesRejectsMalformedGroups(t *testing.T) {
	longID := strings.Repeat("x", 129)
	request := SemanticAssessmentRequest{
		Evidence: []SemanticReviewEvidence{{EvidenceID: "submitted", Content: "submitted content"}},
		EvidenceEquivalenceCandidates: []SemanticAssessmentEvidenceEquivalenceCandidateGroup{
			{EvidenceID: "", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "", Content: ""}}},
			{EvidenceID: "submitted", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{
				{EvidenceID: "duplicate", Content: "first"},
				{EvidenceID: "duplicate", Content: "second"},
				{EvidenceID: longID, Content: "bounded"},
			}},
			{EvidenceID: "submitted", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{}},
			{EvidenceID: "unknown", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "candidate", Content: "content"}}},
		},
	}
	errs := validateSemanticAssessmentEvidenceEquivalenceCandidates(request)
	joined := semanticAssessmentJoinedErrors(errs)
	require.Contains(t, joined, "evidence_equivalence_candidates[0].evidence_id: is required")
	require.Contains(t, joined, "evidence_equivalence_candidates[0].candidates[0].evidence_id: is required and must be bounded")
	require.Contains(t, joined, "evidence_equivalence_candidates[0].candidates[0].content: is required")
	require.Contains(t, joined, "evidence_equivalence_candidates[1].candidates[1].evidence_id: is duplicated")
	require.Contains(t, joined, "evidence_equivalence_candidates[1].candidates[2].evidence_id: is required and must be bounded")
	require.Contains(t, joined, "evidence_equivalence_candidates[2].evidence_id: is duplicated")
	require.Contains(t, joined, "evidence_equivalence_candidates[3].evidence_id: is unknown")
}

func TestValidateSemanticAssessmentEvidenceEquivalenceResultsRejectsMalformedResponses(t *testing.T) {
	request := SemanticAssessmentRequest{
		Evidence: []SemanticReviewEvidence{{EvidenceID: "submitted", Content: "submitted content"}},
		EvidenceEquivalenceCandidates: []SemanticAssessmentEvidenceEquivalenceCandidateGroup{
			{EvidenceID: "", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{}},
			{EvidenceID: "submitted", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{
				{EvidenceID: "candidate", Content: "candidate content"},
				{EvidenceID: "candidate", Content: "duplicate candidate"},
				{EvidenceID: "", Content: ""},
			}},
			{EvidenceID: "submitted", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{}},
			{EvidenceID: "other", Candidates: make([]SemanticAssessmentEvidenceEquivalenceCandidate, SemanticAssessmentMaxEvidenceEquivalenceCandidates+1)},
		},
	}
	errs := validateSemanticAssessmentEvidenceEquivalenceResults(request, []SemanticAssessmentEvidenceEquivalenceResult{
		{EvidenceID: "submitted", Action: "new", CandidateEvidenceID: stringPointer("candidate")},
		{EvidenceID: "submitted", Action: "reuse"},
		{EvidenceID: "submitted", Action: "reuse", CandidateEvidenceID: stringPointer("outside")},
		{EvidenceID: "submitted", Action: "invalid"},
		{EvidenceID: "unknown", Action: "new"},
	})
	joined := semanticAssessmentJoinedErrors(errs)
	require.Contains(t, joined, "evidence_equivalence_candidates[0].evidence_id: is required")
	require.Contains(t, joined, "evidence_equivalence_candidates[1].candidates[1].evidence_id: is duplicated")
	require.Contains(t, joined, "evidence_equivalence_candidates[1].candidates[2].evidence_id: is required and must be bounded")
	require.Contains(t, joined, "evidence_equivalence_candidates[1].candidates[2].content: is required")
	require.Contains(t, joined, "evidence_equivalence_candidates[2].evidence_id: is duplicated")
	require.Contains(t, joined, "evidence_equivalence_candidates[3].evidence_id: is unknown")
	require.Contains(t, joined, "evidence_equivalence_candidates[3].candidates: must contain at most 10 candidates")
	require.Contains(t, joined, "evidence_equivalence_results[1].evidence_id: is duplicated")
	require.Contains(t, joined, "evidence_equivalence_results[0].candidate_evidence_id: must be null for new")
	require.Contains(t, joined, "evidence_equivalence_results[1].candidate_evidence_id: is required for reuse")
	require.Contains(t, joined, "evidence_equivalence_results[2].candidate_evidence_id: is outside the candidate allowlist")
	require.Contains(t, joined, "evidence_equivalence_results[3].action: must be new or reuse")
	require.Contains(t, joined, "evidence_equivalence_results[4].evidence_id: is not an allowlisted non-exact evidence item")
	require.Contains(t, joined, "evidence_equivalence_results: must contain exactly one result per non-exact evidence item")
	require.Contains(t, joined, "evidence_equivalence_results: is missing a non-exact evidence result")
}
