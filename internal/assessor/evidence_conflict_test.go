package assessor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSemanticAssessmentEvidenceConflictCitationsResolveUnicodeRanges(t *testing.T) {
	submitted := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "submitted", Content: "naïve claim"})
	known := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "known", Content: "other claim"})
	startSubmitted, _ := SemanticAssessmentBoundaryRef(submitted, 0)
	endSubmitted, _ := SemanticAssessmentBoundaryRef(submitted, 5)
	startKnown, _ := SemanticAssessmentBoundaryRef(known, 0)
	endKnown, _ := SemanticAssessmentBoundaryRef(known, 5)
	request := SemanticAssessmentRequest{
		RequestID: "conflict-request", TeamID: "team-1", Evidence: []SemanticReviewEvidence{submitted}, KnownEvidence: []SemanticReviewEvidence{known},
	}
	prepared, errs := PrepareSemanticAssessmentRequest(request, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	response := SemanticAssessmentResponse{
		RequestID:                  prepared.RequestID,
		EvidenceSecurityResults:    []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "submitted", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}},
		EvidenceEquivalenceResults: []SemanticAssessmentEvidenceEquivalenceResult{},
		EvidenceConflictResults: []SemanticAssessmentEvidenceConflictResult{{Positions: []SemanticAssessmentEvidenceConflictPosition{
			{EvidenceID: "submitted", StartRef: startSubmitted, EndRef: endSubmitted},
			{EvidenceID: "known", StartRef: startKnown, EndRef: endKnown},
		}}},
		EntityResults: []SemanticAssessmentEntityResult{}, RelationshipResults: []SemanticAssessmentRelationshipResult{},
	}
	validated, errs := PrepareSemanticAssessmentResponse(prepared, response, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	require.Equal(t, 0, validated.EvidenceConflictResults[0].Positions[0].Start)
	require.Equal(t, 5, validated.EvidenceConflictResults[0].Positions[0].End)
}

func TestSemanticAssessmentEvidenceConflictRejectsUnsubmittedAndEquivalenceReuse(t *testing.T) {
	submitted := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "submitted", Content: "claim"})
	candidate := SemanticAssessmentEvidenceEquivalenceCandidate{EvidenceID: "candidate", Content: "other claim"}
	request := SemanticAssessmentRequest{
		RequestID: "conflict-request", TeamID: "team-1", Evidence: []SemanticReviewEvidence{submitted},
		EvidenceEquivalenceCandidates: []SemanticAssessmentEvidenceEquivalenceCandidateGroup{{EvidenceID: "submitted", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{candidate}}},
	}
	prepared, errs := PrepareSemanticAssessmentRequest(request, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	startSubmitted, _ := SemanticAssessmentBoundaryRef(submitted, 0)
	endSubmitted, _ := SemanticAssessmentBoundaryRef(submitted, 5)
	candidatePrepared := prepared.EvidenceEquivalenceCandidates[0].Candidates[0]
	startCandidate, _ := SemanticAssessmentBoundaryRef(SemanticReviewEvidence{EvidenceID: candidatePrepared.EvidenceID, Content: candidatePrepared.Content, BoundaryRefs: candidatePrepared.BoundaryRefs}, 0)
	endCandidate, _ := SemanticAssessmentBoundaryRef(SemanticReviewEvidence{EvidenceID: candidatePrepared.EvidenceID, Content: candidatePrepared.Content, BoundaryRefs: candidatePrepared.BoundaryRefs}, 5)
	response := SemanticAssessmentResponse{
		RequestID:                  prepared.RequestID,
		EvidenceSecurityResults:    []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "submitted", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}},
		EvidenceEquivalenceResults: []SemanticAssessmentEvidenceEquivalenceResult{{EvidenceID: "submitted", Action: "reuse", CandidateEvidenceID: stringPointer("candidate")}},
		EvidenceConflictResults: []SemanticAssessmentEvidenceConflictResult{{Positions: []SemanticAssessmentEvidenceConflictPosition{
			{EvidenceID: "submitted", StartRef: startSubmitted, EndRef: endSubmitted},
			{EvidenceID: "candidate", StartRef: startCandidate, EndRef: endCandidate},
		}}},
		EntityResults: []SemanticAssessmentEntityResult{}, RelationshipResults: []SemanticAssessmentRelationshipResult{},
	}
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, DefaultSemanticAssessmentLimits())
	joined := semanticAssessmentJoinedErrors(errs)
	require.Contains(t, joined, "candidate is also selected for equivalence")
	require.NotContains(t, joined, "must contain at least one submitted evidence position")
	require.False(t, strings.Contains(joined, "duplicates another position"))
}

func TestSemanticAssessmentEvidenceConflictRejectsMalformedCompleteResults(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SemanticAssessmentResponse, SemanticAssessmentRequest)
		want   string
	}{
		{name: "missing array", mutate: func(response *SemanticAssessmentResponse, _ SemanticAssessmentRequest) {
			response.EvidenceConflictResults = nil
		}, want: "evidence_conflict_results: is required"},
		{name: "too many results", mutate: func(response *SemanticAssessmentResponse, request SemanticAssessmentRequest) {
			valid := response.EvidenceConflictResults[0]
			response.EvidenceConflictResults = make([]SemanticAssessmentEvidenceConflictResult, SemanticAssessmentMaxEvidenceConflictResults+1)
			for index := range response.EvidenceConflictResults {
				response.EvidenceConflictResults[index] = valid
			}
			_ = request
		}, want: "must contain at most 20 entries"},
		{name: "too few positions", mutate: func(response *SemanticAssessmentResponse, _ SemanticAssessmentRequest) {
			response.EvidenceConflictResults[0].Positions = response.EvidenceConflictResults[0].Positions[:1]
		}, want: "must contain between 2 and 10 entries"},
		{name: "unknown evidence", mutate: func(response *SemanticAssessmentResponse, _ SemanticAssessmentRequest) {
			response.EvidenceConflictResults[0].Positions[1].EvidenceID = "unknown"
		}, want: "is unknown or not citable"},
		{name: "invalid boundary", mutate: func(response *SemanticAssessmentResponse, _ SemanticAssessmentRequest) {
			response.EvidenceConflictResults[0].Positions[0].StartRef = "invented"
		}, want: "contains invalid boundary references"},
		{name: "duplicate position", mutate: func(response *SemanticAssessmentResponse, _ SemanticAssessmentRequest) {
			response.EvidenceConflictResults[0].Positions[1] = response.EvidenceConflictResults[0].Positions[0]
		}, want: "duplicates another position"},
		{name: "no submitted position", mutate: func(response *SemanticAssessmentResponse, _ SemanticAssessmentRequest) {
			response.EvidenceConflictResults[0].Positions[0].EvidenceID = response.EvidenceConflictResults[0].Positions[1].EvidenceID
		}, want: "must contain at least one submitted evidence position"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			prepared, response := validEvidenceConflictAssessment(t)
			testCase.mutate(&response, prepared)
			_, errs := PrepareSemanticAssessmentResponse(prepared, response, DefaultSemanticAssessmentLimits())
			require.Contains(t, semanticAssessmentJoinedErrors(errs), testCase.want)
		})
	}
}

func TestSemanticAssessmentEvidenceConflictRejectsPaddedCitationIdentifiers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SemanticAssessmentEvidenceConflictPosition)
		want   string
	}{
		{name: "evidence id", mutate: func(position *SemanticAssessmentEvidenceConflictPosition) { position.EvidenceID = " submitted " }, want: "must copy the supplied evidence_id exactly"},
		{name: "start ref", mutate: func(position *SemanticAssessmentEvidenceConflictPosition) {
			position.StartRef = " " + position.StartRef + " "
		}, want: "must copy supplied boundary references exactly"},
		{name: "end ref", mutate: func(position *SemanticAssessmentEvidenceConflictPosition) {
			position.EndRef = " " + position.EndRef + " "
		}, want: "must copy supplied boundary references exactly"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			prepared, response := validEvidenceConflictAssessment(t)
			testCase.mutate(&response.EvidenceConflictResults[0].Positions[0])
			validated, errs := PrepareSemanticAssessmentResponse(prepared, response, DefaultSemanticAssessmentLimits())
			require.Contains(t, semanticAssessmentJoinedErrors(errs), testCase.want)
			require.Equal(t, response.EvidenceConflictResults[0].Positions[0].EvidenceID, validated.EvidenceConflictResults[0].Positions[0].EvidenceID)
		})
	}
}

func TestSemanticAssessmentEvidenceConflictRequiresCandidateAssociation(t *testing.T) {
	prepared, response := validEvidenceConflictAssessment(t)
	candidate := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "candidate", Content: "candidate claim"})
	prepared.EvidenceEquivalenceCandidates = []SemanticAssessmentEvidenceEquivalenceCandidateGroup{{EvidenceID: "submitted-2", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: candidate.EvidenceID, Content: candidate.Content, BoundaryRefs: candidate.BoundaryRefs, BoundaryText: candidate.BoundaryText, BoundaryPrefix: candidate.BoundaryPrefix}}}}
	start, _ := SemanticAssessmentBoundaryRef(candidate, 0)
	end, _ := SemanticAssessmentBoundaryRef(candidate, len([]rune(candidate.Content)))
	response.EvidenceConflictResults[0].Positions = append(response.EvidenceConflictResults[0].Positions, SemanticAssessmentEvidenceConflictPosition{EvidenceID: candidate.EvidenceID, StartRef: start, EndRef: end})
	_, errs := PrepareSemanticAssessmentResponse(prepared, response, DefaultSemanticAssessmentLimits())
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "candidate is not associated with a submitted position in this result")
}

func TestSemanticAssessmentEvidenceConflictCitableEvidenceDeduplicatesSources(t *testing.T) {
	req := SemanticAssessmentRequest{
		Evidence:      []SemanticReviewEvidence{{EvidenceID: "same", Content: "submitted"}},
		KnownEvidence: []SemanticReviewEvidence{{EvidenceID: "same", Content: "known"}},
		EvidenceEquivalenceCandidates: []SemanticAssessmentEvidenceEquivalenceCandidateGroup{
			{EvidenceID: "other", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "candidate", Content: "candidate"}}},
			{EvidenceID: "same", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "candidate", Content: "candidate"}}},
		},
	}
	citable := semanticAssessmentConflictCitableEvidence(req)
	require.True(t, citable["same"].submitted)
	require.Len(t, citable["candidate"].candidateFor, 2)
	errs := resolveSemanticAssessmentEvidenceConflictResults(req, nil)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "evidence_conflict_results: is required")
}

func TestSemanticAssessmentEvidenceConflictRejectsOversizedQuoteAndDuplicateCase(t *testing.T) {
	large := strings.Repeat("x", SemanticAssessmentMaxEvidenceConflictQuoteRunes+1)
	submitted := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "submitted", Content: large})
	known := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "known", Content: "known"})
	request := SemanticAssessmentRequest{RequestID: "large-conflict", TeamID: "team-1", Evidence: []SemanticReviewEvidence{submitted}, KnownEvidence: []SemanticReviewEvidence{known}}
	prepared, errs := PrepareSemanticAssessmentRequest(request, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	startSubmitted, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 0)
	endSubmitted, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], SemanticAssessmentMaxEvidenceConflictQuoteRunes+1)
	startKnown, _ := SemanticAssessmentBoundaryRef(prepared.KnownEvidence[0], 0)
	endKnown, _ := SemanticAssessmentBoundaryRef(prepared.KnownEvidence[0], 5)
	base := SemanticAssessmentEvidenceConflictResult{Positions: []SemanticAssessmentEvidenceConflictPosition{{EvidenceID: "submitted", StartRef: startSubmitted, EndRef: endSubmitted}, {EvidenceID: "known", StartRef: startKnown, EndRef: endKnown}}}
	response := SemanticAssessmentResponse{RequestID: prepared.RequestID, EvidenceSecurityResults: []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "submitted", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}}, EvidenceEquivalenceResults: []SemanticAssessmentEvidenceEquivalenceResult{}, EvidenceConflictResults: []SemanticAssessmentEvidenceConflictResult{base, base}, EntityResults: []SemanticAssessmentEntityResult{}, RelationshipResults: []SemanticAssessmentRelationshipResult{}}
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, DefaultSemanticAssessmentLimits())
	joined := semanticAssessmentJoinedErrors(errs)
	require.Contains(t, joined, "quote must contain at most 4000 runes")
	require.Contains(t, joined, "duplicates another conflict result")
}

func validEvidenceConflictAssessment(t *testing.T) (SemanticAssessmentRequest, SemanticAssessmentResponse) {
	t.Helper()
	submitted := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "submitted", Content: "submitted claim"})
	submittedTwo := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "submitted-2", Content: "another submitted claim"})
	known := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "known", Content: "known claim"})
	request := SemanticAssessmentRequest{RequestID: "conflict-request", TeamID: "team-1", Evidence: []SemanticReviewEvidence{submitted, submittedTwo}, KnownEvidence: []SemanticReviewEvidence{known}}
	prepared, errs := PrepareSemanticAssessmentRequest(request, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	startSubmitted, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 0)
	endSubmitted, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 8)
	startKnown, _ := SemanticAssessmentBoundaryRef(prepared.KnownEvidence[0], 0)
	endKnown, _ := SemanticAssessmentBoundaryRef(prepared.KnownEvidence[0], 5)
	return prepared, SemanticAssessmentResponse{
		RequestID:                  prepared.RequestID,
		EvidenceSecurityResults:    []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "submitted", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}, {EvidenceID: "submitted-2", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}},
		EvidenceEquivalenceResults: []SemanticAssessmentEvidenceEquivalenceResult{},
		EvidenceConflictResults: []SemanticAssessmentEvidenceConflictResult{{Positions: []SemanticAssessmentEvidenceConflictPosition{
			{EvidenceID: "submitted", StartRef: startSubmitted, EndRef: endSubmitted},
			{EvidenceID: "known", StartRef: startKnown, EndRef: endKnown},
		}}},
		EntityResults: []SemanticAssessmentEntityResult{}, RelationshipResults: []SemanticAssessmentRelationshipResult{},
	}
}
