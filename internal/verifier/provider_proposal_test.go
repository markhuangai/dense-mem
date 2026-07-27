package verifier

import (
	"strings"
	"testing"
)

func TestValidateProviderProposalAcceptsGroundedExtraction(t *testing.T) {
	content := "Dense-Mem uses PostgreSQL."
	req := ProviderProposalRequest{
		RequestID:        "extract-1",
		PredicateOptions: []string{"uses"},
		Evidence: []SemanticReviewEvidence{{
			EvidenceID:    "evidence:0",
			EvidenceIndex: 0,
			Content:       content,
		}},
	}
	proposal := ProviderProposal{
		PredicateOptions: []string{"uses"},
		EntityProposals: []ProviderEntityProposal{
			{
				Ref:        "project_1",
				Name:       "Dense-Mem",
				EntityKind: "project",
				Evidence:   []ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: len([]rune("Dense-Mem"))}},
			},
			{
				Ref:        "db_1",
				Name:       "PostgreSQL",
				EntityKind: "project",
				Evidence: []ProviderEvidenceSpan{{
					EvidenceIndex: 0,
					Start:         len([]rune("Dense-Mem uses ")),
					End:           len([]rune("Dense-Mem uses PostgreSQL")),
				}},
			},
		},
		RelationshipProposals: []ProviderRelationshipProposal{{
			ProposalID:        "rel:uses",
			SubjectRef:        "project_1",
			OriginalPredicate: "uses",
			PredicateCandidates: []string{
				"uses",
			},
			RelationshipKind: "state",
			ObjectRef:        "db_1",
			Polarity:         "+",
			Modality:         "statement",
			ValidFrom:        providerStringPtr("2026-07-19T00:00:00Z"),
			ValidTo:          providerStringPtr("2026-07-20T00:00:00Z"),
			Evidence:         []ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: len([]rune(content))}},
		}},
	}

	if errs := ValidateProviderProposal(req, proposal); len(errs) > 0 {
		t.Fatalf("ValidateProviderProposal errors = %#v", errs)
	}
}

func TestDecodeProviderProposalJSONRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	raw := []byte(`{"predicate_options":[],"evidence":[],"entity_proposals":[],"relationship_proposals":[]}`)
	if _, err := DecodeProviderProposalJSON(raw); err == nil {
		t.Fatal("unknown provider proposal field accepted")
	}

	raw = []byte(`{"predicate_options":[],"entity_proposals":[],"relationship_proposals":[]} {}`)
	if _, err := DecodeProviderProposalJSON(raw); err == nil {
		t.Fatal("trailing provider proposal JSON accepted")
	}
}

func TestDecodeProviderProposalJSONAcceptsTypedValueDisplayAndUnit(t *testing.T) {
	raw := []byte(`{
		"predicate_options":["scored"],
		"entity_proposals":[
			{"ref":"project","name":"Dense-Mem","entity_kind":"project","aliases":[],"known_entity_id":null,"evidence":[{"evidence_index":0,"start":0,"end":9}]}
		],
		"relationship_proposals":[
			{
				"proposal_id":"score",
				"subject_ref":"project",
				"original_predicate":"scored",
				"predicate_candidates":["scored"],
				"relationship_kind":"state",
				"object_ref":"",
				"object_value":{"ref":"score_value","type":"number","value":"42","display":"42%","unit":"percent"},
				"polarity":"+",
				"modality":"statement",
				"evidence":[{"evidence_index":0,"start":0,"end":9}],
				"valid_from":null,
				"valid_to":null,
				"client_comment":null
			}
		]
	}`)
	proposal, err := DecodeProviderProposalJSON(raw)
	if err != nil {
		t.Fatalf("DecodeProviderProposalJSON: %v", err)
	}
	value := proposal.RelationshipProposals[0].ObjectValue
	if value == nil || value.Display != "42%" || value.Unit != "percent" {
		t.Fatalf("object value = %#v", value)
	}
}

func TestPrepareProviderProposalRequestTrimsAndRejectsInvalidInputs(t *testing.T) {
	req, errs := PrepareProviderProposalRequest(ProviderProposalRequest{
		RequestID:        " request-1 ",
		PredicateOptions: []string{" uses ", "uses", " "},
		Evidence: []SemanticReviewEvidence{{
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

	_, errs = PrepareProviderProposalRequest(ProviderProposalRequest{
		RequestID:        " ",
		PredicateOptions: []string{" "},
		Evidence: []SemanticReviewEvidence{{
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
	got := providerTestValidationMessages(errs)
	for _, want := range []string{
		"request_id: is required",
		"predicate_options: is required",
		"evidence[0].content: is required",
		"evidence[1].evidence_id: is duplicated",
		"evidence[1].evidence_index: is duplicated",
		"evidence[2].evidence_id: is required",
	} {
		if !testContainsValidation(got, want) {
			t.Fatalf("validation errors = %#v, want %q", got, want)
		}
	}

	_, errs = PrepareProviderProposalRequest(ProviderProposalRequest{RequestID: "request-1", PredicateOptions: []string{"uses"}})
	if !testContainsValidation(providerTestValidationMessages(errs), "evidence: is required") {
		t.Fatalf("missing evidence errors = %#v", errs)
	}
}

func TestValidateProviderProposalRejectsUnknownRefsAndBadSpans(t *testing.T) {
	req := ProviderProposalRequest{
		RequestID:        "extract-1",
		PredicateOptions: []string{"uses"},
		Evidence: []SemanticReviewEvidence{{
			EvidenceID:    "evidence:0",
			EvidenceIndex: 0,
			Content:       "Dense-Mem uses PostgreSQL.",
		}},
	}
	proposal := ProviderProposal{
		EntityProposals: []ProviderEntityProposal{{
			Ref:        "project_1",
			Name:       "Dense-Mem",
			EntityKind: "project",
			Evidence:   []ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: 200}},
		}},
		RelationshipProposals: []ProviderRelationshipProposal{{
			ProposalID:        "rel:uses",
			SubjectRef:        "missing",
			OriginalPredicate: "uses",
			PredicateCandidates: []string{
				"uses",
			},
			RelationshipKind: "state",
			ObjectRef:        "project_1",
			ValidFrom:        providerStringPtr("2026-12-31T00:00:00Z"),
			ValidTo:          providerStringPtr("2026-07-01T00:00:00Z"),
			Evidence:         []ProviderEvidenceSpan{{EvidenceIndex: 2, Start: 0, End: 1}},
		}},
	}

	errs := ValidateProviderProposal(req, proposal)
	got := providerTestValidationMessages(errs)
	if len(errs) < 3 ||
		!testContainsValidation(got, "entity_proposals[0].evidence[0]: span is invalid") ||
		!testContainsValidation(got, "relationship_proposals[0].subject_ref: is unknown") ||
		!testContainsValidation(got, "relationship_proposals[0].valid_to: must not be before valid_from") ||
		!testContainsValidation(got, "relationship_proposals[0].evidence[0]: evidence_index 2 is unknown") {
		t.Fatalf("validation errors = %#v", got)
	}
}

func TestValidateProviderProposalRejectsEntityShapeErrors(t *testing.T) {
	req := providerProposalTestRequest()
	proposal := ProviderProposal{
		EntityProposals: []ProviderEntityProposal{
			{Ref: " ", Name: " ", EntityKind: "unsupported"},
			{Ref: "dup", Name: "Dense-Mem", EntityKind: "project", Evidence: []ProviderEvidenceSpan{{EvidenceIndex: 3, Start: 0, End: 1}}},
			{Ref: " dup ", Name: "PostgreSQL", EntityKind: "project", Evidence: []ProviderEvidenceSpan{{EvidenceIndex: 0, Start: -1, End: 1}}},
		},
	}

	got := providerTestValidationMessages(ValidateProviderProposal(req, proposal))
	for _, want := range []string{
		"entity_proposals[0].ref: is required",
		"entity_proposals[0].name: is required",
		"entity_proposals[0].entity_kind: is unsupported",
		"entity_proposals[0].evidence: is required",
		"entity_proposals[1].evidence[0]: evidence_index 3 is unknown",
		"entity_proposals[2].ref: is duplicated",
		"entity_proposals[2].evidence[0]: span is invalid",
	} {
		if !testContainsValidation(got, want) {
			t.Fatalf("validation errors = %#v, want %q", got, want)
		}
	}
}

func TestValidateProviderProposalRejectsRelationshipShapeErrors(t *testing.T) {
	req := providerProposalTestRequest()
	proposal := ProviderProposal{
		EntityProposals: []ProviderEntityProposal{
			{Ref: "project_1", Name: "Dense-Mem", EntityKind: "project", Evidence: []ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: 9}}},
			{Ref: "db_1", Name: "PostgreSQL", EntityKind: "project", Evidence: []ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 15, End: 25}}},
		},
		RelationshipProposals: []ProviderRelationshipProposal{
			{
				SubjectRef:        "missing",
				OriginalPredicate: " ",
				Polarity:          "?",
				Modality:          "guess",
				ValidFrom:         providerStringPtr("not-a-time"),
			},
			{
				ProposalID:        "rel:dup",
				SubjectRef:        "project_1",
				OriginalPredicate: "uses",
				PredicateCandidates: []string{
					"uses",
				},
				RelationshipKind: "state",
				ObjectRef:        "missing",
				ObjectValue:      &SemanticValueObservation{Type: "unsupported", Value: "value"},
				Evidence:         []ProviderEvidenceSpan{{EvidenceIndex: 0, Start: 0, End: 9}},
			},
			{
				ProposalID:        " rel:dup ",
				SubjectRef:        "project_1",
				OriginalPredicate: "uses",
				PredicateCandidates: []string{
					"uses",
				},
				RelationshipKind: "state",
				ObjectRef:        "db_1",
				Evidence:         []ProviderEvidenceSpan{{EvidenceIndex: 4, Start: 0, End: 1}},
			},
		},
	}

	got := providerTestValidationMessages(ValidateProviderProposal(req, proposal))
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
		if !testContainsValidation(got, want) {
			t.Fatalf("validation errors = %#v, want %q", got, want)
		}
	}
}

func providerProposalTestRequest() ProviderProposalRequest {
	return ProviderProposalRequest{
		RequestID:        "extract-1",
		PredicateOptions: []string{"uses"},
		Evidence: []SemanticReviewEvidence{{
			EvidenceID:    "evidence:0",
			EvidenceIndex: 0,
			Content:       "Dense-Mem uses PostgreSQL.",
		}},
	}
}

func providerStringPtr(value string) *string {
	return &value
}

func testContainsValidation(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

func providerTestValidationMessages(errs []SemanticValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return out
}
