package assessor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSemanticAssessmentKnownEvidenceIsReadOnlyContextWithSubmittedSupportFence(t *testing.T) {
	request, limits := semanticAssessmentSubmissionContractTestRequest(t)
	known := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{
		EvidenceID: "known-1", Content: "Known context confirms Mark works on Dense-Mem.",
	})
	request.KnownEvidence = []SemanticReviewEvidence{known}
	request.SubmissionContract.Relationships[0].KnownEvidenceIDs = []string{"known-1"}
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)
	require.Len(t, prepared.KnownEvidence, 1)

	response := semanticAssessmentTestResponse()
	submittedSupport := response.RelationshipResults[0].Splits[0].SupportRanges[0]
	knownSupport := semanticAssessmentTestRange(known, 0, len([]rune(known.Content)))
	response.RelationshipResults[0].Splits[0].SupportRanges = []SemanticAssessmentGroundedRange{submittedSupport, knownSupport}
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, limits)
	require.Empty(t, errs)

	response = semanticAssessmentTestResponse()
	response.EvidenceSecurityResults = append(response.EvidenceSecurityResults, SemanticAssessmentEvidenceSecurityResult{
		EvidenceID: "known-1", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{},
	})
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, limits)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "evidence_security_results[1].evidence_id: is unknown")

	response = semanticAssessmentTestResponse()
	response.RelationshipResults[0].Splits[0].SupportRanges = []SemanticAssessmentGroundedRange{
		submittedSupport,
		{EvidenceID: "known-other", StartRef: knownSupport.StartRef, EndRef: knownSupport.EndRef},
	}
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, limits)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "is outside the submitted or authorized known evidence allowlist")
}

func TestSemanticAssessmentKnownEvidenceRequiresSubmittedSupport(t *testing.T) {
	request, limits := semanticAssessmentSubmissionContractTestRequest(t)
	known := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{
		EvidenceID: "known-1", Content: "Known context confirms Mark works on Dense-Mem.",
	})
	request.KnownEvidence = []SemanticReviewEvidence{known}
	request.SubmissionContract.Relationships[0].KnownEvidenceIDs = []string{"known-1"}
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)

	response := semanticAssessmentTestResponse()
	knownSupport := semanticAssessmentTestRange(known, 0, len([]rune(known.Content)))
	response.RelationshipResults[0].Splits[0].SupportRanges = []SemanticAssessmentGroundedRange{knownSupport}
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, limits)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "must contain at least one submitted support range")
}

func TestSemanticAssessmentKnownEvidenceRequiresSubmittedSupportPerSplit(t *testing.T) {
	request, limits := semanticAssessmentSubmissionContractTestRequest(t)
	known := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{
		EvidenceID: "known-1", Content: "Known context confirms Mark works on Dense-Mem.",
	})
	request.KnownEvidence = []SemanticReviewEvidence{known}
	request.SubmissionContract.Relationships[0].KnownEvidenceIDs = []string{"known-1"}
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)

	response := semanticAssessmentTestResponse()
	first := response.RelationshipResults[0].Splits[0]
	second := first
	second.SplitIndex = 1
	knownRange := semanticAssessmentTestRange(known, 0, len([]rune(known.Content)))
	second.PredicateRange = knownRange
	second.SupportRanges = []SemanticAssessmentGroundedRange{knownRange}
	response.RelationshipResults[0].Splits = []SemanticAssessmentRelationshipSplit{first, second}
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, limits)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "relationship_results[0].splits[1].support_ranges: must contain at least one submitted support range")
}

func TestSemanticAssessmentUnavailableKnownEvidenceRequiresNotSupported(t *testing.T) {
	request, limits := semanticAssessmentSubmissionContractTestRequest(t)
	known := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{
		EvidenceID: "known-1", Content: "Known context confirms Mark works on Dense-Mem.",
	})
	request.KnownEvidence = []SemanticReviewEvidence{known}
	request.SubmissionContract.Relationships[0].KnownEvidenceIDs = []string{"known-1"}
	request.SubmissionContract.Relationships[0].KnownEvidenceUnavailable = true
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)

	response := semanticAssessmentTestResponse()
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, limits)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "must be not_supported when requested known evidence is unavailable")

	reason := "not_supported_by_evidence"
	response = semanticAssessmentTestResponse()
	response.RelationshipResults[0] = SemanticAssessmentRelationshipResult{
		Ref: "relationship-1", Disposition: "not_supported", Reason: &reason, Splits: []SemanticAssessmentRelationshipSplit{},
	}
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, limits)
	require.Empty(t, errs, strings.Join([]string{"unavailable known evidence should be a warning", semanticAssessmentJoinedErrors(errs)}, ": "))
}

func TestSemanticAssessmentAnchoredCoreferenceRequiresIssuedAnchorAndCandidate(t *testing.T) {
	request, limits := semanticAssessmentSubmissionContractTestRequest(t)
	request.SubmissionContract.Entities[0].KnownEntityID = "entity-mark"
	secondEvidence := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{
		EvidenceID: "ev-2", EvidenceIndex: 1, Content: "He works remotely.",
	})
	request.Evidence = append(request.Evidence, secondEvidence)
	request.SubmissionContract.Entities[0].EvidenceIDs = []string{"ev-1", "ev-2"}
	anchorStart, _ := SemanticAssessmentBoundaryRef(request.Evidence[0], 0)
	anchorEnd, _ := SemanticAssessmentBoundaryRef(request.Evidence[0], 4)
	pronounStart, _ := SemanticAssessmentBoundaryRef(secondEvidence, 0)
	pronounEnd, _ := SemanticAssessmentBoundaryRef(secondEvidence, 2)
	request.SubmissionContract.Entities[0].Anchors = []SemanticAssessmentEntityAnchor{{
		AnchorRef: "anchor-mark", EvidenceID: "ev-1", Surface: "Mark", StartRef: anchorStart, EndRef: anchorEnd,
		CandidateEntityIDs: []string{"entity-mark"},
	}}
	request.SubmissionContract.Entities[0].Groundings = append(request.SubmissionContract.Entities[0].Groundings, SemanticAssessmentEntityGrounding{
		GroundingRef: "grounding-he", EvidenceID: "ev-2", Surface: "He", StartRef: pronounStart, EndRef: pronounEnd, AnchorRef: "anchor-mark",
	})
	request.EntityCandidateGroups = append(request.EntityCandidateGroups, SemanticAssessmentEntityCandidateGroup{
		Surface: "He", EvidenceID: "ev-2", GroundingRef: "grounding-he", Start: 0, End: 2, Candidates: []SemanticAssessmentEntityCandidate{{
			EntityID: "entity-mark", CanonicalName: "Mark", Kind: "person",
		}},
	})
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)
	response := semanticAssessmentTestResponse()
	response.EvidenceSecurityResults = append(response.EvidenceSecurityResults, SemanticAssessmentEvidenceSecurityResult{
		EvidenceID: "ev-2", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{},
	})
	pronounGrounding := "grounding-he"
	anchorRef := "anchor-mark"
	candidate := "entity-mark"
	response.EntityResults[0] = SemanticAssessmentEntityResult{
		Ref: "person-1", GroundingRef: &pronounGrounding, AnchorRef: &anchorRef,
		Action: string(domain.EntityResolutionReuse), CandidateEntityID: &candidate,
	}
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, limits)
	require.Empty(t, errs)

	for _, mutate := range []func(*SemanticAssessmentResponse){
		func(response *SemanticAssessmentResponse) {
			invented := "anchor-invented"
			response.EntityResults[0].AnchorRef = &invented
		},
		func(response *SemanticAssessmentResponse) {
			response.EntityResults[0].Action = string(domain.EntityResolutionCreate)
		},
		func(response *SemanticAssessmentResponse) {
			wrong := "entity-other"
			response.EntityResults[0].CandidateEntityID = &wrong
		},
	} {
		response := semanticAssessmentTestResponse()
		response.EvidenceSecurityResults = append(response.EvidenceSecurityResults, SemanticAssessmentEvidenceSecurityResult{
			EvidenceID: "ev-2", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{},
		})
		response.EntityResults[0] = SemanticAssessmentEntityResult{
			Ref: "person-1", GroundingRef: &pronounGrounding, AnchorRef: &anchorRef,
			Action: string(domain.EntityResolutionReuse), CandidateEntityID: &candidate,
		}
		mutate(&response)
		_, errs := PrepareSemanticAssessmentResponse(prepared, response, limits)
		require.NotEmpty(t, errs)
	}
}

func TestSemanticAssessmentKnownEvidenceRequestValidationBranches(t *testing.T) {
	t.Run("duplicate and overlap are rejected", func(t *testing.T) {
		request, limits := semanticAssessmentTestRequest(t)
		request.KnownEvidence = []SemanticReviewEvidence{
			{EvidenceID: " ev-1 ", Content: "known context"},
			{EvidenceID: "ev-1", Content: ""},
		}
		_, errs := PrepareSemanticAssessmentRequest(request, limits)
		require.Contains(t, semanticAssessmentJoinedErrors(errs), "known_evidence[1].evidence_id: is duplicated")
		require.Contains(t, semanticAssessmentJoinedErrors(errs), "known_evidence[0].evidence_id: duplicates submitted evidence")
		require.Contains(t, semanticAssessmentJoinedErrors(errs), "known_evidence[1].content: is required")
	})

	t.Run("known evidence is bounded", func(t *testing.T) {
		request, limits := semanticAssessmentTestRequest(t)
		request.KnownEvidence = make([]SemanticReviewEvidence, SemanticAssessmentMaxKnownEvidence+1)
		for index := range request.KnownEvidence {
			request.KnownEvidence[index] = SemanticReviewEvidence{
				EvidenceID: "known-" + string(rune('a'+index%26)) + "-" + string(rune(index)),
				Content:    "known context",
			}
		}
		_, errs := PrepareSemanticAssessmentRequest(request, limits)
		require.Contains(t, semanticAssessmentJoinedErrors(errs), "known_evidence: must contain at most")
	})

	t.Run("contract references must be loaded known evidence", func(t *testing.T) {
		request, limits := semanticAssessmentSubmissionContractTestRequest(t)
		request.SubmissionContract.Relationships[0].KnownEvidenceIDs = []string{"known-missing"}
		_, errs := PrepareSemanticAssessmentRequest(request, limits)
		require.Contains(t, semanticAssessmentJoinedErrors(errs), "known_evidence_ids: contains unknown known evidence")
	})

	t.Run("trusted relationship refs accept known evidence", func(t *testing.T) {
		request, limits := semanticAssessmentTestRequest(t)
		known := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "known-1", Content: "known context"})
		request.KnownEvidence = []SemanticReviewEvidence{known}
		request.RequiredRelationshipRefs = []SemanticAssessmentRequiredRelationshipRef{{
			ProposalID: "relationship-1", EvidenceIDs: []string{"ev-1"}, KnownEvidenceIDs: []string{"known-1"},
		}}
		_, errs := PrepareSemanticAssessmentRequest(request, limits)
		require.Empty(t, errs)
	})

	t.Run("known evidence identifiers are validated", func(t *testing.T) {
		request, limits := semanticAssessmentTestRequest(t)
		request.KnownEvidence = []SemanticReviewEvidence{{EvidenceID: "", Content: "known context"}}
		_, errs := PrepareSemanticAssessmentRequest(request, limits)
		require.Contains(t, semanticAssessmentJoinedErrors(errs), "known_evidence[0].evidence_id: is required")
	})

	t.Run("anchor fields are closed and evidence-scoped", func(t *testing.T) {
		request, limits := semanticAssessmentSubmissionContractTestRequest(t)
		known := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{
			EvidenceID: "known-1", Content: "Mark confirms the system.",
		})
		request.KnownEvidence = []SemanticReviewEvidence{known}
		target := &request.SubmissionContract.Entities[0]
		target.KnownEvidenceIDs = []string{"known-1"}
		markRange := semanticAssessmentTestRange(known, 0, 4)
		target.Anchors = []SemanticAssessmentEntityAnchor{
			{AnchorRef: "anchor-mark", EvidenceID: "known-1", Surface: "Mark", StartRef: markRange.StartRef, EndRef: markRange.EndRef, CandidateEntityIDs: []string{"entity-mark"}},
			{AnchorRef: " anchor-mark ", EvidenceID: "ev-other", Surface: "It", CandidateEntityIDs: make([]string, SemanticAssessmentMaxEntityCandidatesPerSurface+1)},
		}
		for index := range target.Anchors[1].CandidateEntityIDs {
			target.Anchors[1].CandidateEntityIDs[index] = "entity-mark"
		}
		_, errs := PrepareSemanticAssessmentRequest(request, limits)
		joined := semanticAssessmentJoinedErrors(errs)
		require.Contains(t, joined, "anchors[1].anchor_ref: is duplicated")
		require.Contains(t, joined, "anchors[1].surface: must be a canonical or alias Entity mention")
		require.Contains(t, joined, "anchors[1].candidate_entity_ids: must contain between")
		require.Contains(t, joined, "anchors[1].candidate_entity_ids[1]: is duplicated")
		require.Contains(t, joined, "anchors[1].evidence_id: is outside the entity evidence allowlist")
	})

	t.Run("anchor structural errors are rejected", func(t *testing.T) {
		request, limits := semanticAssessmentSubmissionContractTestRequest(t)
		known := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{
			EvidenceID: "known-1", Content: "Mark confirms the system.",
		})
		request.KnownEvidence = []SemanticReviewEvidence{known}
		target := &request.SubmissionContract.Entities[0]
		target.KnownEvidenceIDs = []string{"known-1", "known-missing"}
		validRange := semanticAssessmentTestRange(known, 0, 4)
		target.Anchors = []SemanticAssessmentEntityAnchor{
			{AnchorRef: "", EvidenceID: "known-1", Surface: "Mark", StartRef: validRange.StartRef, EndRef: validRange.EndRef, CandidateEntityIDs: []string{"entity-mark"}},
			{AnchorRef: "anchor-invalid-candidate", EvidenceID: "known-1", Surface: "Mark", StartRef: validRange.StartRef, EndRef: validRange.EndRef, CandidateEntityIDs: []string{""}},
			{AnchorRef: "anchor-invalid-boundary", EvidenceID: "known-1", Surface: "Mark", StartRef: "missing", EndRef: validRange.EndRef, CandidateEntityIDs: []string{"entity-mark"}},
			{AnchorRef: "anchor-wrong-surface", EvidenceID: "known-1", Surface: "Wrong", StartRef: validRange.StartRef, EndRef: validRange.EndRef, CandidateEntityIDs: []string{"entity-mark"}},
		}
		_, errs := PrepareSemanticAssessmentRequest(request, limits)
		joined := semanticAssessmentJoinedErrors(errs)
		require.Contains(t, joined, "anchors[0].anchor_ref: is required and must be bounded")
		require.Contains(t, joined, "anchors[1].candidate_entity_ids[0]: is required and must be bounded")
		require.Contains(t, joined, "anchors[2]: contains invalid boundary references")
		require.Contains(t, joined, "anchors[3].surface: quote does not match the original evidence span")
		require.Contains(t, joined, "known_evidence_ids: contains unknown known evidence")
	})
}

func TestSemanticAssessmentGroundingHelperSemantics(t *testing.T) {
	require.Equal(t, "mark huang\x00person", semanticAssessmentEntityLogicalKey(" Mark   Huang ", "person"))

	grounding := "grounding"
	for _, test := range []struct {
		name   string
		result SemanticAssessmentEntityResult
		want   bool
	}{
		{name: "reused grounding", result: SemanticAssessmentEntityResult{Action: "reuse", GroundingRef: &grounding}, want: true},
		{name: "ambiguous action", result: SemanticAssessmentEntityResult{Action: string(domain.EntityResolutionAmbiguous), GroundingRef: &grounding}, want: false},
		{name: "missing grounding", result: SemanticAssessmentEntityResult{Action: "reuse"}, want: false},
		{name: "blank grounding", result: SemanticAssessmentEntityResult{Action: "reuse", GroundingRef: stringPointer(" ")}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, semanticAssessmentEntityResultGrounded(test.result))
		})
	}

	require.Equal(t, "is outside the submitted evidence allowlist", semanticAssessmentRelationshipEvidenceAllowlistMessage(SemanticAssessmentRequiredRelationshipRef{}))
	require.Equal(t, "is outside the submitted or authorized known evidence allowlist", semanticAssessmentRelationshipEvidenceAllowlistMessage(SemanticAssessmentRequiredRelationshipRef{KnownEvidenceIDs: []string{"known-1"}}))
	require.True(t, submissionAssessmentAnchorSurfaceAllowed("Mark"))
	require.False(t, submissionAssessmentAnchorSurfaceAllowed("It"))
	require.False(t, submissionAssessmentAnchorSurfaceAllowed(" "))

	earlier := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "earlier", EvidenceIndex: 0, Content: "Mark works."})
	later := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "later", EvidenceIndex: 1, Content: "He works."})
	earlierRange := semanticAssessmentTestRange(earlier, 0, 4)
	laterRange := semanticAssessmentTestRange(later, 0, 2)
	anchor := SemanticAssessmentEntityAnchor{EvidenceID: earlier.EvidenceID, Start: earlierRange.Start, End: earlierRange.End}
	require.True(t, semanticAssessmentAnchorPrecedes(anchor, SemanticAssessmentEntityGrounding{EvidenceID: later.EvidenceID, Start: laterRange.Start}, map[string]SemanticReviewEvidence{
		earlier.EvidenceID: earlier, later.EvidenceID: later,
	}))
	require.False(t, semanticAssessmentAnchorPrecedes(SemanticAssessmentEntityAnchor{EvidenceID: later.EvidenceID, Start: laterRange.Start, End: laterRange.End}, SemanticAssessmentEntityGrounding{EvidenceID: earlier.EvidenceID, Start: earlierRange.Start}, map[string]SemanticReviewEvidence{
		earlier.EvidenceID: earlier, later.EvidenceID: later,
	}))
	require.False(t, semanticAssessmentAnchorPrecedes(SemanticAssessmentEntityAnchor{EvidenceID: "missing", Start: 0, End: 1}, SemanticAssessmentEntityGrounding{EvidenceID: later.EvidenceID, Start: 0}, map[string]SemanticReviewEvidence{later.EvidenceID: later}))
	require.False(t, semanticAssessmentAnchorPrecedes(SemanticAssessmentEntityAnchor{EvidenceID: earlier.EvidenceID, Start: 0, End: 1}, SemanticAssessmentEntityGrounding{EvidenceID: "missing", Start: 0}, map[string]SemanticReviewEvidence{earlier.EvidenceID: earlier}))

	require.Equal(t, "not-a-time", *normalizeOptionalAssessmentTime(stringPointer(" not-a-time ")))
	require.True(t, semanticAssessmentValuesEqual(nil, nil))
	require.False(t, semanticAssessmentValuesEqual(nil, &SemanticAssessmentValue{ValueType: "number", CanonicalValue: "1"}))
	require.False(t, semanticAssessmentValuesEqual(&SemanticAssessmentValue{ValueType: "number", CanonicalValue: "1"}, &SemanticAssessmentValue{ValueType: "number", CanonicalValue: "2"}))
}
