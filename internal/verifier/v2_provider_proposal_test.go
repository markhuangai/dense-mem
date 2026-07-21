package verifier

import (
	"strings"
	"testing"
)

func TestValidateV2ProviderProposalAcceptsGroundedExtraction(t *testing.T) {
	content := "Dense-Mem uses PostgreSQL."
	req := V2ProviderProposalRequest{
		RequestID:        "extract-1",
		PredicateOptions: []string{"uses"},
		Evidence: []V2SemanticReviewEvidence{{
			EvidenceID:    "evidence:0",
			EvidenceIndex: 0,
			Content:       content,
		}},
	}
	proposal := V2ProviderProposal{
		PredicateOptions: []string{"uses"},
		Evidence: []V2ProviderProposalEvidence{{
			EvidenceIndex: 0,
			EvidenceID:    "evidence:0",
			Content:       content,
		}},
		EntityProposals: []V2ProviderEntityProposal{
			{
				Ref:        "project_1",
				Name:       "Dense-Mem",
				EntityKind: "project",
				Evidence:   []V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: len([]rune("Dense-Mem"))}},
			},
			{
				Ref:        "db_1",
				Name:       "PostgreSQL",
				EntityKind: "project",
				Evidence: []V2ProviderEvidenceSpan{{
					EvidenceIndex: 0,
					Start:         len([]rune("Dense-Mem uses ")),
					End:           len([]rune("Dense-Mem uses PostgreSQL")),
				}},
			},
		},
		RelationshipProposals: []V2ProviderRelationshipProposal{{
			ProposalID:        "rel:uses",
			SubjectRef:        "project_1",
			OriginalPredicate: "uses",
			ObjectRef:         "db_1",
			Polarity:          "+",
			Modality:          "statement",
			ValidFrom:         v2ProviderStringPtr("2026-07-19T00:00:00Z"),
			ValidTo:           v2ProviderStringPtr("2026-07-20T00:00:00Z"),
			Evidence:          []V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: len([]rune(content))}},
		}},
	}

	if errs := ValidateV2ProviderProposal(req, proposal); len(errs) > 0 {
		t.Fatalf("ValidateV2ProviderProposal errors = %#v", errs)
	}
}

func TestDecodeV2ProviderProposalJSONRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	raw := []byte(`{"predicate_options":[],"evidence":[],"entity_proposals":[],"relationship_proposals":[],"unknown":true}`)
	if _, err := DecodeV2ProviderProposalJSON(raw); err == nil {
		t.Fatal("unknown provider proposal field accepted")
	}

	raw = []byte(`{"predicate_options":[],"evidence":[],"entity_proposals":[],"relationship_proposals":[]} {}`)
	if _, err := DecodeV2ProviderProposalJSON(raw); err == nil {
		t.Fatal("trailing provider proposal JSON accepted")
	}
}

func TestPrepareV2ProviderProposalRequestTrimsAndRejectsInvalidInputs(t *testing.T) {
	req, errs := PrepareV2ProviderProposalRequest(V2ProviderProposalRequest{
		RequestID:        " request-1 ",
		PredicateOptions: []string{" uses ", "uses", " "},
		Evidence: []V2SemanticReviewEvidence{{
			EvidenceID:    " evidence:0 ",
			FragmentID:    " fragment:0 ",
			EvidenceIndex: 0,
			Content:       "Dense-Mem uses PostgreSQL.",
		}},
	})
	if len(errs) > 0 {
		t.Fatalf("valid prepared request returned errors = %#v", errs)
	}
	if req.RequestID != "request-1" || req.Evidence[0].EvidenceID != "evidence:0" || req.Evidence[0].FragmentID != "fragment:0" {
		t.Fatalf("request was not trimmed: %#v", req)
	}
	if len(req.PredicateOptions) != 1 || req.PredicateOptions[0] != "uses" {
		t.Fatalf("predicate options = %#v", req.PredicateOptions)
	}

	_, errs = PrepareV2ProviderProposalRequest(V2ProviderProposalRequest{
		RequestID:        " ",
		PredicateOptions: []string{" "},
		Evidence: []V2SemanticReviewEvidence{{
			EvidenceID:    "dup",
			EvidenceIndex: 0,
			Content:       " ",
		}, {
			EvidenceID:    " dup ",
			EvidenceIndex: 0,
			Content:       "content",
		}, {
			EvidenceID:    " ",
			EvidenceIndex: 2,
			Content:       "content",
		}},
	})
	got := v2ProviderTestValidationMessages(errs)
	for _, want := range []string{
		"request_id: is required",
		"predicate_options: is required",
		"evidence[0].content: is required",
		"evidence[1].evidence_id: is duplicated",
		"evidence[1].evidence_index: is duplicated",
		"evidence[2].evidence_id: is required",
	} {
		if !v2TestContainsValidation(got, want) {
			t.Fatalf("validation errors = %#v, want %q", got, want)
		}
	}

	_, errs = PrepareV2ProviderProposalRequest(V2ProviderProposalRequest{RequestID: "request-1", PredicateOptions: []string{"uses"}})
	if !v2TestContainsValidation(v2ProviderTestValidationMessages(errs), "evidence: is required") {
		t.Fatalf("missing evidence errors = %#v", errs)
	}
}

func TestValidateV2ProviderProposalRejectsUnknownRefsAndBadSpans(t *testing.T) {
	req := V2ProviderProposalRequest{
		RequestID:        "extract-1",
		PredicateOptions: []string{"uses"},
		Evidence: []V2SemanticReviewEvidence{{
			EvidenceID:    "evidence:0",
			EvidenceIndex: 0,
			Content:       "Dense-Mem uses PostgreSQL.",
		}},
	}
	proposal := V2ProviderProposal{
		Evidence: []V2ProviderProposalEvidence{{
			EvidenceIndex: 0,
			EvidenceID:    "evidence:0",
			Content:       "Dense-Mem uses PostgreSQL.",
		}},
		EntityProposals: []V2ProviderEntityProposal{{
			Ref:        "project_1",
			Name:       "Dense-Mem",
			EntityKind: "project",
			Evidence:   []V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: 200}},
		}},
		RelationshipProposals: []V2ProviderRelationshipProposal{{
			ProposalID:        "rel:uses",
			SubjectRef:        "missing",
			OriginalPredicate: "uses",
			ObjectRef:         "project_1",
			ValidFrom:         v2ProviderStringPtr("2026-12-31T00:00:00Z"),
			ValidTo:           v2ProviderStringPtr("2026-07-01T00:00:00Z"),
			Evidence:          []V2ProviderEvidenceSpan{{EvidenceIndex: 2, Start: 0, End: 1}},
		}},
	}

	errs := ValidateV2ProviderProposal(req, proposal)
	got := v2ProviderTestValidationMessages(errs)
	if len(errs) < 3 ||
		!v2TestContainsValidation(got, "entity_proposals[0].evidence[0]: span is invalid") ||
		!v2TestContainsValidation(got, "relationship_proposals[0].subject_ref: is unknown") ||
		!v2TestContainsValidation(got, "relationship_proposals[0].valid_to: must not be before valid_from") ||
		!v2TestContainsValidation(got, "relationship_proposals[0].evidence[0]: evidence_index 2 is unknown") {
		t.Fatalf("validation errors = %#v", got)
	}
}

func TestValidateV2ProviderProposalRejectsEvidenceEchoMismatches(t *testing.T) {
	req := V2ProviderProposalRequest{
		RequestID:        "extract-1",
		PredicateOptions: []string{"uses"},
		Evidence: []V2SemanticReviewEvidence{{
			EvidenceID:    "evidence:0",
			EvidenceIndex: 0,
			Content:       "Dense-Mem uses PostgreSQL.",
		}, {
			EvidenceID:    "evidence:1",
			EvidenceIndex: 1,
			Content:       "Dense-Mem uses pgvector.",
		}},
	}
	proposal := V2ProviderProposal{
		Evidence: []V2ProviderProposalEvidence{
			{EvidenceIndex: 2, EvidenceID: "evidence:2", Content: "unknown"},
			{EvidenceIndex: 0, EvidenceID: "wrong", Content: "Dense-Mem uses PostgreSQL."},
			{EvidenceIndex: 0, EvidenceID: "evidence:0", Content: "wrong"},
		},
	}

	got := v2ProviderTestValidationMessages(ValidateV2ProviderProposal(req, proposal))
	for _, want := range []string{
		"evidence[0].evidence_index: is unknown",
		"evidence[1].evidence_id: does not match request evidence",
		"evidence[2].evidence_index: is duplicated",
		"evidence[2].content: does not match request evidence",
		"evidence: missing echo for evidence_index 1",
	} {
		if !v2TestContainsValidation(got, want) {
			t.Fatalf("validation errors = %#v, want %q", got, want)
		}
	}
}

func TestValidateV2ProviderProposalRejectsEntityShapeErrors(t *testing.T) {
	req := v2ProviderProposalTestRequest()
	proposal := V2ProviderProposal{
		Evidence: []V2ProviderProposalEvidence{{
			EvidenceIndex: 0,
			EvidenceID:    "evidence:0",
			Content:       req.Evidence[0].Content,
		}},
		EntityProposals: []V2ProviderEntityProposal{
			{Ref: " ", Name: " ", EntityKind: "unsupported"},
			{Ref: "dup", Name: "Dense-Mem", EntityKind: "project", Evidence: []V2ProviderEvidenceSpan{{EvidenceIndex: 3, Start: 0, End: 1}}},
			{Ref: " dup ", Name: "PostgreSQL", EntityKind: "project", Evidence: []V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: -1, End: 1}}},
		},
	}

	got := v2ProviderTestValidationMessages(ValidateV2ProviderProposal(req, proposal))
	for _, want := range []string{
		"entity_proposals[0].ref: is required",
		"entity_proposals[0].name: is required",
		"entity_proposals[0].entity_kind: is unsupported",
		"entity_proposals[0].evidence: is required",
		"entity_proposals[1].evidence[0]: evidence_index 3 is unknown",
		"entity_proposals[2].ref: is duplicated",
		"entity_proposals[2].evidence[0]: span is invalid",
	} {
		if !v2TestContainsValidation(got, want) {
			t.Fatalf("validation errors = %#v, want %q", got, want)
		}
	}
}

func TestValidateV2ProviderProposalRejectsRelationshipShapeErrors(t *testing.T) {
	req := v2ProviderProposalTestRequest()
	proposal := V2ProviderProposal{
		Evidence: []V2ProviderProposalEvidence{{
			EvidenceIndex: 0,
			EvidenceID:    "evidence:0",
			Content:       req.Evidence[0].Content,
		}},
		EntityProposals: []V2ProviderEntityProposal{
			{Ref: "project_1", Name: "Dense-Mem", EntityKind: "project", Evidence: []V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: 9}}},
			{Ref: "db_1", Name: "PostgreSQL", EntityKind: "project", Evidence: []V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 15, End: 25}}},
		},
		RelationshipProposals: []V2ProviderRelationshipProposal{
			{
				SubjectRef:        "missing",
				OriginalPredicate: " ",
				Polarity:          "?",
				Modality:          "guess",
				ValidFrom:         v2ProviderStringPtr("not-a-time"),
			},
			{
				ProposalID:        "rel:dup",
				SubjectRef:        "project_1",
				OriginalPredicate: "uses",
				ObjectRef:         "missing",
				ObjectValue:       &V2SemanticValueObservation{Type: "unsupported", Value: "value"},
				Evidence:          []V2ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: 9}},
			},
			{
				ProposalID:        " rel:dup ",
				SubjectRef:        "project_1",
				OriginalPredicate: "uses",
				ObjectRef:         "db_1",
				Evidence:          []V2ProviderEvidenceSpan{{EvidenceIndex: 4, Start: 0, End: 1}},
			},
		},
	}

	got := v2ProviderTestValidationMessages(ValidateV2ProviderProposal(req, proposal))
	for _, want := range []string{
		"relationship_proposals[0].proposal_id: is required",
		"relationship_proposals[0].subject_ref: is unknown",
		"relationship_proposals[0].original_predicate: is required",
		"relationship_proposals[0].object: requires exactly one object_ref or object_value",
		"relationship_proposals[0].polarity: is unsupported",
		"relationship_proposals[0].modality: is unsupported",
		"relationship_proposals[0].valid_from: must be RFC3339 timestamp",
		"relationship_proposals[0].evidence: is required",
		"relationship_proposals[1].object: requires exactly one object_ref or object_value",
		"relationship_proposals[1].object_ref: is unknown",
		"relationship_proposals[1].object_value.type: is unsupported",
		"relationship_proposals[2].proposal_id: is duplicated",
		"relationship_proposals[2].evidence[0]: evidence_index 4 is unknown",
	} {
		if !v2TestContainsValidation(got, want) {
			t.Fatalf("validation errors = %#v, want %q", got, want)
		}
	}
}

func v2ProviderProposalTestRequest() V2ProviderProposalRequest {
	return V2ProviderProposalRequest{
		RequestID:        "extract-1",
		PredicateOptions: []string{"uses"},
		Evidence: []V2SemanticReviewEvidence{{
			EvidenceID:    "evidence:0",
			EvidenceIndex: 0,
			Content:       "Dense-Mem uses PostgreSQL.",
		}},
	}
}

func v2ProviderStringPtr(value string) *string {
	return &value
}

func v2TestContainsValidation(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

func v2ProviderTestValidationMessages(errs []V2SemanticValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return out
}
