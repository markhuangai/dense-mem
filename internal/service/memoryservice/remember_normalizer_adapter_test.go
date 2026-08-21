package memoryservice

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestRememberNormalizerResponseToSemanticAssessmentConvertsStructure(t *testing.T) {
	_, _, _, request, semantic, _ := semanticAssessmentConfidenceFixture(t)
	request.SubmissionContract = adapterTestSubmissionContract()
	semanticRelationship := semantic.RelationshipResults[0]
	startRef := semanticRelationship.PredicateRange.StartRef
	endRef := semanticRelationship.PredicateRange.EndRef
	supportStartRef := semanticRelationship.SupportRanges[0].StartRef
	supportEndRef := semanticRelationship.SupportRanges[0].EndRef
	markGrounding := request.SubmissionContract.Entities[0].Groundings[0].GroundingRef
	denseGrounding := request.SubmissionContract.Entities[1].Groundings[0].GroundingRef
	markID := request.EntityCandidateGroups[0].Candidates[0].EntityID
	denseID := request.EntityCandidateGroups[1].Candidates[0].EntityID
	predicateKey := "works_on"
	predicateVersion := 1
	scopeKey := "production"

	normalized := verifier.RememberNormalizerResponse{
		RequestID: request.RequestID,
		SecuritySignals: []verifier.RememberNormalizerSecuritySignal{{
			EvidenceID: "evidence:0", Kind: "prompt_injection", StartRef: supportStartRef, EndRef: supportEndRef,
			Start: semanticRelationship.SupportRanges[0].Start, End: semanticRelationship.SupportRanges[0].End,
		}},
		EntityResults: []verifier.RememberNormalizerEntityResult{
			{Ref: "mark", GroundingRef: &markGrounding, Action: "reuse", CandidateEntityID: &markID},
			{Ref: "dense-mem", GroundingRef: &denseGrounding, Action: "reuse", CandidateEntityID: &denseID},
		},
		RelationshipResults: []verifier.RememberNormalizerRelationshipResult{{
			Ref: semanticRelationship.Ref, SubjectRef: semanticRelationship.SubjectRef,
			PredicateRange: verifier.RememberNormalizerRange{
				EvidenceID: "evidence:0", StartRef: startRef, EndRef: endRef,
				Start: semanticRelationship.PredicateRange.Start, End: semanticRelationship.PredicateRange.End,
			},
			PredicateStatus: "resolved", PredicateKey: &predicateKey, PredicateVersion: &predicateVersion,
			ObjectRef: stringPointer("dense-mem"), Polarity: semanticRelationship.Polarity, Modality: semanticRelationship.Modality,
			SupportRanges: []verifier.RememberNormalizerRange{{
				EvidenceID: "evidence:0", StartRef: supportStartRef, EndRef: supportEndRef,
				Start: semanticRelationship.SupportRanges[0].Start, End: semanticRelationship.SupportRanges[0].End,
			}},
			ScopeStatus: "resolved", ScopeKey: &scopeKey,
		}},
	}

	converted, err := rememberNormalizerResponseToSemanticAssessment(request, normalized)
	require.NoError(t, err)
	require.Len(t, converted.SecuritySignals, 1)
	require.Equal(t, normalized.SecuritySignals[0].EvidenceID, converted.SecuritySignals[0].EvidenceID)
	require.Len(t, converted.EntityResults, 2)
	require.Equal(t, "person", converted.EntityResults[0].Kind)
	require.Equal(t, "Mark", converted.EntityResults[0].Surface)
	require.Equal(t, markID, *converted.EntityResults[0].CandidateEntityID)
	require.Len(t, converted.RelationshipResults, 1)
	relationship := converted.RelationshipResults[0]
	require.Equal(t, float64(1), relationship.Confidence)
	require.Equal(t, "normalized structure", relationship.Rationale)
	require.Equal(t, "entailed", relationship.EvidenceVerdict)
	require.Equal(t, "absent", relationship.TemporalVerdict)
	require.Equal(t, "works on", relationship.OriginalPredicate)
	require.Len(t, relationship.SupportRanges, 1)
	require.Len(t, relationship.Evidence, 1)
}

func TestRememberNormalizerResponseToSemanticAssessmentRejectsUngroundedTemporalValidity(t *testing.T) {
	_, _, _, request, semantic, _ := semanticAssessmentConfidenceFixture(t)
	validFrom := "2026-01-01T00:00:00Z"
	normalized := verifier.RememberNormalizerResponse{
		RequestID: request.RequestID,
		RelationshipResults: []verifier.RememberNormalizerRelationshipResult{{
			Ref: semantic.RelationshipResults[0].Ref, SubjectRef: semantic.RelationshipResults[0].SubjectRef,
			ValidFrom: &validFrom,
		}},
	}

	_, err := rememberNormalizerResponseToSemanticAssessment(request, normalized)
	require.EqualError(t, err, "normalizer temporal validity requires an evidence-backed workflow")
}

func TestRememberNormalizerResponseToSemanticAssessmentSelectsGroundingNearEndpointPredicate(t *testing.T) {
	content := "Project Aurora uses LedgerDB. Project Aurora uses Atlas."
	evidence := verifier.PrepareSemanticAssessmentEvidence(verifier.SemanticReviewEvidence{
		EvidenceID: "evidence:0",
		Content:    content,
	})
	grounding := func(ref string, start, end int) verifier.SemanticAssessmentEntityGrounding {
		startRef, startOK := verifier.SemanticAssessmentBoundaryRef(evidence, start)
		endRef, endOK := verifier.SemanticAssessmentBoundaryRef(evidence, end)
		require.True(t, startOK)
		require.True(t, endOK)
		return verifier.SemanticAssessmentEntityGrounding{
			GroundingRef: ref,
			EvidenceID:   evidence.EvidenceID,
			Surface:      string([]rune(content)[start:end]),
			StartRef:     startRef,
			EndRef:       endRef,
			Start:        start,
			End:          end,
		}
	}
	rangeValue := func(start, end int) verifier.RememberNormalizerRange {
		startRef, startOK := verifier.SemanticAssessmentBoundaryRef(evidence, start)
		endRef, endOK := verifier.SemanticAssessmentBoundaryRef(evidence, end)
		require.True(t, startOK)
		require.True(t, endOK)
		return verifier.RememberNormalizerRange{
			EvidenceID: evidence.EvidenceID,
			StartRef:   startRef,
			EndRef:     endRef,
			Start:      start,
			End:        end,
		}
	}
	first, second := grounding("g-first", 0, 14), grounding("g-second", 30, 44)
	request := verifier.SemanticAssessmentRequest{
		RequestID: "request:repeated-grounding",
		Evidence:  []verifier.SemanticReviewEvidence{evidence},
		EntityCandidateGroups: []verifier.SemanticAssessmentEntityCandidateGroup{
			{GroundingRef: first.GroundingRef, EvidenceID: evidence.EvidenceID, Start: first.Start, End: first.End, Candidates: []verifier.SemanticAssessmentEntityCandidate{{EntityID: "entity:aurora", Kind: "project"}}},
			{GroundingRef: second.GroundingRef, EvidenceID: evidence.EvidenceID, Start: second.Start, End: second.End, Candidates: []verifier.SemanticAssessmentEntityCandidate{{EntityID: "entity:aurora", Kind: "project"}}},
		},
		SubmissionContract: &verifier.SemanticAssessmentSubmissionContract{
			Entities: []verifier.SemanticAssessmentRequiredEntityRef{
				{Ref: "subject:first", Name: "Project Aurora", Kind: "project", EvidenceIDs: []string{evidence.EvidenceID}, Groundings: []verifier.SemanticAssessmentEntityGrounding{first, second}},
				{Ref: "subject:second", Name: "Project Aurora", Kind: "project", EvidenceIDs: []string{evidence.EvidenceID}, Groundings: []verifier.SemanticAssessmentEntityGrounding{first, second}},
			},
			Relationships: []verifier.SemanticAssessmentRequiredRelationshipRef{
				{ProposalID: "r:first", SubjectRef: "subject:first", EvidenceIDs: []string{evidence.EvidenceID}, ObjectValue: &verifier.SemanticAssessmentValue{ValueType: "string", CanonicalValue: "LedgerDB"}, Polarity: "+", Modality: "statement"},
				{ProposalID: "r:second", SubjectRef: "subject:second", EvidenceIDs: []string{evidence.EvidenceID}, ObjectValue: &verifier.SemanticAssessmentValue{ValueType: "string", CanonicalValue: "Atlas"}, Polarity: "+", Modality: "statement"},
			},
		},
	}
	entityID := "entity:aurora"
	firstRef, secondRef := first.GroundingRef, first.GroundingRef
	normalized := verifier.RememberNormalizerResponse{
		RequestID: request.RequestID,
		EntityResults: []verifier.RememberNormalizerEntityResult{
			{Ref: "subject:first", GroundingRef: &firstRef, Action: "reuse", CandidateEntityID: &entityID},
			{Ref: "subject:second", GroundingRef: &secondRef, Action: "reuse", CandidateEntityID: &entityID},
		},
		RelationshipResults: []verifier.RememberNormalizerRelationshipResult{
			{Ref: "r:first", SubjectRef: "subject:first", PredicateRange: rangeValue(15, 19)},
			{Ref: "r:second", SubjectRef: "subject:second", PredicateRange: rangeValue(45, 49)},
		},
	}

	converted, err := rememberNormalizerResponseToSemanticAssessment(request, normalized)
	require.NoError(t, err)
	require.Len(t, converted.EntityResults, 2)
	assert.Equal(t, first.GroundingRef, *converted.EntityResults[0].GroundingRef)
	assert.Equal(t, 0, converted.EntityResults[0].Start)
	assert.Equal(t, second.GroundingRef, *converted.EntityResults[1].GroundingRef)
	assert.Equal(t, 30, converted.EntityResults[1].Start)
}

func TestRememberNormalizerEntityGroundingPreservesProviderChoiceOnTie(t *testing.T) {
	grounding := func(ref string, start, end int) verifier.SemanticAssessmentEntityGrounding {
		return verifier.SemanticAssessmentEntityGrounding{GroundingRef: ref, EvidenceID: "evidence:0", Start: start, End: end}
	}
	first, second := grounding("g-first", 0, 14), grounding("g-second", 30, 44)
	entityID := "entity:aurora"
	request := verifier.RememberNormalizerRequest{EntityCandidateGroups: []verifier.SemanticAssessmentEntityCandidateGroup{
		{GroundingRef: first.GroundingRef, Candidates: []verifier.SemanticAssessmentEntityCandidate{{EntityID: entityID, Kind: "project"}}},
		{GroundingRef: second.GroundingRef, Candidates: []verifier.SemanticAssessmentEntityCandidate{{EntityID: entityID, Kind: "project"}}},
	}}
	groundingRef := second.GroundingRef
	selected, ok := rememberNormalizerEntityGrounding(
		request,
		verifier.SemanticAssessmentRequiredEntityRef{
			Name: "Project Aurora", Kind: "project", Groundings: []verifier.SemanticAssessmentEntityGrounding{first, second},
		},
		verifier.RememberNormalizerEntityResult{GroundingRef: &groundingRef, Action: "reuse", CandidateEntityID: &entityID},
		[]verifier.RememberNormalizerRange{{EvidenceID: "evidence:0", Start: 20, End: 24}},
	)
	require.True(t, ok)
	assert.Equal(t, second.GroundingRef, selected.GroundingRef)
}

func TestRememberNormalizerResponsePreservesGroundingsWhenNearestRemapCollides(t *testing.T) {
	content := "Jordan mentors Jordan"
	evidence := verifier.PrepareSemanticAssessmentEvidence(verifier.SemanticReviewEvidence{
		EvidenceID: "evidence:collision",
		Content:    content,
	})
	grounding := func(ref string, start, end int) verifier.SemanticAssessmentEntityGrounding {
		startRef, startOK := verifier.SemanticAssessmentBoundaryRef(evidence, start)
		endRef, endOK := verifier.SemanticAssessmentBoundaryRef(evidence, end)
		require.True(t, startOK && endOK)
		return verifier.SemanticAssessmentEntityGrounding{
			GroundingRef: ref,
			EvidenceID:   evidence.EvidenceID,
			Surface:      string([]rune(content)[start:end]),
			StartRef:     startRef,
			EndRef:       endRef,
			Start:        start,
			End:          end,
		}
	}
	first := grounding("grounding:first", 0, 6)
	second := grounding("grounding:second", 15, 21)
	firstRef, secondRef := first.GroundingRef, second.GroundingRef
	personID, organizationID := "entity:jordan-person", "entity:jordan-organization"
	request := verifier.SemanticAssessmentRequest{
		RequestID: "request:grounding-collision",
		Evidence:  []verifier.SemanticReviewEvidence{evidence},
		EntityCandidateGroups: []verifier.SemanticAssessmentEntityCandidateGroup{
			{GroundingRef: first.GroundingRef, EvidenceID: evidence.EvidenceID, Start: first.Start, End: first.End, Candidates: []verifier.SemanticAssessmentEntityCandidate{
				{EntityID: personID, Kind: "person"}, {EntityID: organizationID, Kind: "organization"},
			}},
			{GroundingRef: second.GroundingRef, EvidenceID: evidence.EvidenceID, Start: second.Start, End: second.End, Candidates: []verifier.SemanticAssessmentEntityCandidate{
				{EntityID: personID, Kind: "person"}, {EntityID: organizationID, Kind: "organization"},
			}},
		},
		SubmissionContract: &verifier.SemanticAssessmentSubmissionContract{
			Entities: []verifier.SemanticAssessmentRequiredEntityRef{
				{Ref: "person", Name: "Jordan", Kind: "person", Groundings: []verifier.SemanticAssessmentEntityGrounding{first, second}},
				{Ref: "organization", Name: "Jordan", Kind: "organization", Groundings: []verifier.SemanticAssessmentEntityGrounding{first, second}},
			},
			Relationships: []verifier.SemanticAssessmentRequiredRelationshipRef{
				{ProposalID: "r-person", SubjectRef: "person", EvidenceIDs: []string{evidence.EvidenceID}, Polarity: "+", Modality: "statement"},
				{ProposalID: "r-organization", SubjectRef: "organization", EvidenceIDs: []string{evidence.EvidenceID}, Polarity: "+", Modality: "statement"},
			},
		},
	}
	predicateStartRef, predicateStartOK := verifier.SemanticAssessmentBoundaryRef(evidence, 12)
	predicateEndRef, predicateEndOK := verifier.SemanticAssessmentBoundaryRef(evidence, 15)
	require.True(t, predicateStartOK && predicateEndOK)
	firstPredicate := verifier.RememberNormalizerRange{EvidenceID: evidence.EvidenceID, StartRef: predicateStartRef, EndRef: predicateEndRef, Start: 12, End: 15}
	normalized := verifier.RememberNormalizerResponse{
		RequestID: request.RequestID,
		EntityResults: []verifier.RememberNormalizerEntityResult{
			{Ref: "person", GroundingRef: &firstRef, Action: "reuse", CandidateEntityID: &personID},
			{Ref: "organization", GroundingRef: &secondRef, Action: "reuse", CandidateEntityID: &organizationID},
		},
		RelationshipResults: []verifier.RememberNormalizerRelationshipResult{
			{Ref: "r-person", SubjectRef: "person", PredicateRange: firstPredicate},
			{Ref: "r-organization", SubjectRef: "organization", PredicateRange: firstPredicate},
		},
	}

	converted, err := rememberNormalizerResponseToSemanticAssessment(request, normalized)
	require.NoError(t, err)
	require.Len(t, converted.EntityResults, 2)
	assert.Equal(t, first.GroundingRef, *converted.EntityResults[0].GroundingRef)
	assert.Equal(t, second.GroundingRef, *converted.EntityResults[1].GroundingRef)
}

func adapterTestSubmissionContract() *verifier.SemanticAssessmentSubmissionContract {
	return &verifier.SemanticAssessmentSubmissionContract{
		Entities: []verifier.SemanticAssessmentRequiredEntityRef{
			{Ref: "mark", Name: "Mark", Kind: "person", Groundings: []verifier.SemanticAssessmentEntityGrounding{{GroundingRef: "grounding-mark", EvidenceID: "evidence:0", Surface: "Mark"}}},
			{Ref: "dense-mem", Name: "Dense-Mem", Kind: "product", Groundings: []verifier.SemanticAssessmentEntityGrounding{{GroundingRef: "grounding-dense-mem", EvidenceID: "evidence:0", Surface: "Dense-Mem"}}},
		},
		Relationships: []verifier.SemanticAssessmentRequiredRelationshipRef{{
			ProposalID: "works-on", SubjectRef: "mark", EvidenceIDs: []string{"evidence:0"}, ObjectRef: stringPointer("dense-mem"), Polarity: "+", Modality: "statement",
		}},
	}
}

func TestRememberNormalizerResponseToSemanticAssessmentConvertsValueObject(t *testing.T) {
	_, _, _, request, semantic, _ := semanticAssessmentConfidenceFixture(t)
	request.SubmissionContract = adapterTestSubmissionContract()
	target := request.SubmissionContract.Relationships[0]
	target.ObjectRef = nil
	target.ObjectValue = &verifier.SemanticAssessmentValue{ValueType: "string", CanonicalValue: "typed"}
	request.SubmissionContract.Relationships[0] = target
	valueRange := verifier.RememberNormalizerRange{EvidenceID: "evidence:0", StartRef: semantic.RelationshipResults[0].SupportRanges[0].StartRef, EndRef: semantic.RelationshipResults[0].SupportRanges[0].EndRef, Start: 0, End: 24}
	predicateKey := "works_on"
	predicateVersion := 1
	normalized := verifier.RememberNormalizerResponse{
		RequestID:     request.RequestID,
		EntityResults: []verifier.RememberNormalizerEntityResult{},
		RelationshipResults: []verifier.RememberNormalizerRelationshipResult{{
			Ref: target.ProposalID, SubjectRef: target.SubjectRef,
			PredicateRange:  verifier.RememberNormalizerRange{EvidenceID: "evidence:0", StartRef: semantic.RelationshipResults[0].PredicateRange.StartRef, EndRef: semantic.RelationshipResults[0].PredicateRange.EndRef, Start: 5, End: 13},
			PredicateStatus: "resolved", PredicateKey: &predicateKey, PredicateVersion: &predicateVersion,
			ObjectValue: target.ObjectValue, ValueRange: &valueRange, Polarity: target.Polarity, Modality: target.Modality,
			SupportRanges: []verifier.RememberNormalizerRange{valueRange}, ScopeStatus: "absent",
		}},
	}

	converted, err := rememberNormalizerResponseToSemanticAssessment(request, normalized)
	require.NoError(t, err)
	require.Equal(t, target.ObjectValue, converted.RelationshipResults[0].ObjectValue)
	require.NotNil(t, converted.RelationshipResults[0].ValueRange)
	require.Equal(t, 1.0, converted.RelationshipResults[0].ValueRange.Confidence)
}

func TestRememberNormalizerResponsePreservesRegistrationRequired(t *testing.T) {
	_, _, _, request, semantic, _ := semanticAssessmentConfidenceFixture(t)
	request.SubmissionContract = adapterTestSubmissionContract()
	relationship := semantic.RelationshipResults[0]
	normalized := verifier.RememberNormalizerResponse{
		RequestID: request.RequestID,
		RelationshipResults: []verifier.RememberNormalizerRelationshipResult{{
			Ref: relationship.Ref, SubjectRef: relationship.SubjectRef,
			PredicateRange: verifier.RememberNormalizerRange{
				EvidenceID: relationship.PredicateRange.EvidenceID,
				StartRef:   relationship.PredicateRange.StartRef,
				EndRef:     relationship.PredicateRange.EndRef,
				Start:      relationship.PredicateRange.Start,
				End:        relationship.PredicateRange.End,
			},
			PredicateStatus: "registration_required",
			ObjectRef:       stringPointer("dense-mem"),
			Polarity:        relationship.Polarity,
			Modality:        relationship.Modality,
			SupportRanges: []verifier.RememberNormalizerRange{{
				EvidenceID: relationship.SupportRanges[0].EvidenceID,
				StartRef:   relationship.SupportRanges[0].StartRef,
				EndRef:     relationship.SupportRanges[0].EndRef,
				Start:      relationship.SupportRanges[0].Start,
				End:        relationship.SupportRanges[0].End,
			}},
			ScopeStatus: "absent",
		}},
	}

	converted, err := rememberNormalizerResponseToSemanticAssessment(request, normalized)
	require.NoError(t, err)
	require.Equal(t, "registration_required", converted.RelationshipResults[0].PredicateStatus)
}

func TestRememberNormalizerResponseToSemanticAssessmentRejectsInvalidPredicateSpan(t *testing.T) {
	_, _, _, request, semantic, _ := semanticAssessmentConfidenceFixture(t)
	predicateKey := "works_on"
	predicateVersion := 1
	normalized := verifier.RememberNormalizerResponse{
		RequestID: request.RequestID,
		RelationshipResults: []verifier.RememberNormalizerRelationshipResult{{
			Ref: semantic.RelationshipResults[0].Ref, SubjectRef: semantic.RelationshipResults[0].SubjectRef,
			PredicateRange:  verifier.RememberNormalizerRange{EvidenceID: "evidence:0", StartRef: semantic.RelationshipResults[0].PredicateRange.StartRef, EndRef: semantic.RelationshipResults[0].PredicateRange.EndRef, Start: -1, End: 999},
			PredicateStatus: "resolved", PredicateKey: &predicateKey, PredicateVersion: &predicateVersion,
			ObjectRef: stringPointer("dense-mem"), Polarity: "+", Modality: "statement", ScopeStatus: "absent",
			SupportRanges: []verifier.RememberNormalizerRange{{EvidenceID: "evidence:0", Start: 0, End: 24}},
		}},
	}

	_, err := rememberNormalizerResponseToSemanticAssessment(request, normalized)
	require.Error(t, err)
	require.Contains(t, err.Error(), "predicate span")
}

func TestNormalizerRangeToAssessmentRangePreservesBounds(t *testing.T) {
	converted := normalizerRangeToAssessmentRange(verifier.RememberNormalizerRange{
		EvidenceID: "evidence:0", StartRef: "b0", EndRef: "b1", Start: 2, End: 7,
	})
	require.Equal(t, "evidence:0", converted.EvidenceID)
	require.Equal(t, "b0", converted.StartRef)
	require.Equal(t, "b1", converted.EndRef)
	require.Equal(t, 1.0, converted.Confidence)
	require.Equal(t, 2, converted.Start)
	require.Equal(t, 7, converted.End)
}
