package dreamgeneration

import (
	"strings"
	"testing"
)

func TestDreamGenerationRequestRejectsDurableReferences(t *testing.T) {
	_, errs := PrepareDreamGenerationRequest(DreamGenerationRequest{
		RequestID: "request",
		Paths: []DreamGenerationPath{{
			PathRef: "550e8400-e29b-41d4-a716-446655440000",
		}},
	}, DefaultSemanticAssessmentLimits())
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "opaque") {
		t.Fatalf("PrepareDreamGenerationRequest() errors = %#v", errs)
	}
}

func TestDreamGenerationResponseRequiresCompleteEvidence(t *testing.T) {
	request := DreamGenerationRequest{
		RequestID:  "request",
		MaxOutputs: 1,
		Paths: []DreamGenerationPath{{
			PathRef: "path",
			Subject: DreamGenerationNode{Ref: "a", Display: "A", Kind: "concept"},
			Middle:  DreamGenerationNode{Ref: "b", Display: "B", Kind: "concept"},
			Object:  DreamGenerationNode{Ref: "c", Display: "C", Kind: "concept"},
			Premises: []DreamGenerationPremise{
				{PremiseRef: "p1", RelationshipRef: "r1", PredicateLabel: "uses", RelationshipVersion: 1, Status: "active", FromRef: "a", ToRef: "b", Evidence: []DreamGenerationEvidence{{EvidenceRef: "e1", Content: "A uses B", Authority: "primary"}}},
				{PremiseRef: "p2", RelationshipRef: "r2", PredicateLabel: "has", RelationshipVersion: 1, Status: "active", FromRef: "b", ToRef: "c", Evidence: []DreamGenerationEvidence{{EvidenceRef: "e2", Content: "B has C", Authority: "primary"}}},
			},
			AllowedPredicates: []DreamGenerationPredicate{{PredicateRef: "predicate", Label: "relates", RelationshipKind: "state", CurrentCardinality: "many"}},
		}},
	}
	prepared, errs := PrepareDreamGenerationRequest(request, DefaultSemanticAssessmentLimits())
	if len(errs) != 0 {
		t.Fatalf("PrepareDreamGenerationRequest() errors = %#v", errs)
	}
	response := DreamGenerationResponse{RequestID: "request", Proposals: []DreamGenerationProposal{{
		PathRef: "path", PredicateRef: "predicate", Statement: "A relates C", Rationale: "supported",
		WhatIf: "If true", PossibleOutcome: "C follows", Likelihood: 0.5, Confidence: 0.5, EvidenceRefs: []string{"e1"},
	}}}
	_, errs = PrepareDreamGenerationResponse(prepared, response)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "each premise") {
		t.Fatalf("PrepareDreamGenerationResponse() errors = %#v", errs)
	}
}
