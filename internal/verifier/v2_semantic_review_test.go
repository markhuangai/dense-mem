package verifier

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestV2SemanticReviewPrepareFiltersProviderEgressAndMapsWhitespaceQuote(t *testing.T) {
	req := v2SemanticReviewTestRequest()
	req.EntityMentions[0].Candidates = append(req.EntityMentions[0].Candidates,
		V2SemanticEntityCandidate{
			EntityID:      "ent-other-team",
			CanonicalName: "Mark Other",
			Kind:          "person",
			TeamID:        "team-b",
			Status:        "active",
		},
		V2SemanticEntityCandidate{
			EntityID:      "ent-retired",
			CanonicalName: "Retired Mark",
			Kind:          "person",
			TeamID:        "team-a",
			Status:        "retired",
		},
	)
	req.RelationshipObservations[0].Quote = "Mark works on Dense-Mem."
	req.RelationshipObservations[0].PredicateCandidates = append(req.RelationshipObservations[0].PredicateCandidates,
		V2SemanticPredicateCandidate{
			PredicateKey:        "retired_predicate",
			Version:             1,
			AllowedSubjectKinds: []string{"person"},
			AllowedObjectKinds:  []string{"project"},
			RelationshipKind:    "state",
			CurrentCardinality:  "many",
			LifecycleState:      "retired",
		},
	)

	prepared, errs := PrepareV2SemanticReviewRequest(req)
	if len(errs) != 0 {
		t.Fatalf("PrepareV2SemanticReviewRequest errors = %#v", errs)
	}
	if got := prepared.RelationshipObservations[0].Quote; got != "Mark works\non Dense-Mem." {
		t.Fatalf("quote = %q", got)
	}
	if got := prepared.EntityMentions[0].Candidates; len(got) != 1 || got[0].EntityID != "ent-mark" {
		t.Fatalf("candidates leaked unauthorized IDs: %#v", got)
	}
	if got := prepared.RelationshipObservations[0].PredicateCandidates; len(got) != 1 || got[0].PredicateKey != "works_on" {
		t.Fatalf("predicate candidates = %#v", got)
	}
}

func TestV2SemanticReviewPrepareUsesCodePointSpans(t *testing.T) {
	req := v2SemanticReviewTestRequest()
	content := "Renée works\non Dense-Mem."
	req.Evidence[0].Content = content
	req.EntityMentions[0].Surface = "Renée"
	req.EntityMentions[0].Start = 0
	req.EntityMentions[0].End = 5
	projectStart := v2SemanticTestRuneIndex(content, "Dense-Mem")
	req.EntityMentions[1].Start = projectStart
	req.EntityMentions[1].End = projectStart + len([]rune("Dense-Mem"))
	req.RelationshipObservations[0].Quote = content
	req.RelationshipObservations[0].Start = 0
	req.RelationshipObservations[0].End = len([]rune(content))

	prepared, errs := PrepareV2SemanticReviewRequest(req)
	if len(errs) != 0 {
		t.Fatalf("PrepareV2SemanticReviewRequest errors = %#v", errs)
	}
	if got := prepared.EntityMentions[0].Surface; got != "Renée" {
		t.Fatalf("surface = %q", got)
	}
	if got := prepared.RelationshipObservations[0].Quote; got != content {
		t.Fatalf("quote = %q", got)
	}
}

func TestV2SemanticReviewPrepareRejectsStaleSourceAndQuoteMismatch(t *testing.T) {
	req := v2SemanticReviewTestRequest()
	req.Evidence[0].SourceRevisionID = "rev-old"
	req.Evidence[0].CurrentSourceRevisionID = "rev-new"

	_, errs := PrepareV2SemanticReviewRequest(req)
	joined := v2SemanticJoinedErrors(errs)
	if !strings.Contains(joined, "is not current") {
		t.Fatalf("missing source currentness error: %s", joined)
	}

	req = v2SemanticReviewTestRequest()
	req.RelationshipObservations[0].Quote = "unrelated"
	_, errs = PrepareV2SemanticReviewRequest(req)
	joined = v2SemanticJoinedErrors(errs)
	if !strings.Contains(joined, "quote does not match") {
		t.Fatalf("missing quote mismatch error: %s", joined)
	}
}

func TestV2SemanticReviewPrepareRejectsShapeErrors(t *testing.T) {
	req := v2SemanticReviewTestRequest()
	req.RequestID = ""
	req.Evidence = append(req.Evidence, req.Evidence[0])
	req.Evidence[1].Content = " "
	req.EntityMentions = append(req.EntityMentions, req.EntityMentions[0])
	req.EntityMentions[1].Kind = "unsupported"
	req.RelationshipObservations[0].SubjectRef = "missing"
	req.RelationshipObservations[0].ObjectRef = ""
	req.RelationshipObservations[0].ObjectValue = nil

	_, errs := PrepareV2SemanticReviewRequest(req)
	joined := v2SemanticJoinedErrors(errs)
	for _, want := range []string{
		"request_id: is required",
		"evidence[1].evidence_id: is duplicated",
		"evidence[1].content: is required",
		"entity_mentions[2].ref: is duplicated",
		"entity_mentions[1].kind: is unsupported",
		"relationship_observations[0].subject_ref: is unknown",
		"relationship_observations[0].object: requires exactly one object_ref or object_value",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestV2SemanticReviewPrepareSupportsTypedValueEndpointKinds(t *testing.T) {
	req := v2SemanticReviewTestRequest()
	req.RelationshipObservations[0].ObjectRef = ""
	req.RelationshipObservations[0].ObjectValue = &V2SemanticValueObservation{
		Ref:   "value_1",
		Type:  "string",
		Value: "Dense-Mem",
	}
	req.RelationshipObservations[0].PredicateCandidates[0].AllowedObjectKinds = []string{"string"}

	prepared, errs := PrepareV2SemanticReviewRequest(req)
	if len(errs) != 0 {
		t.Fatalf("PrepareV2SemanticReviewRequest errors = %#v", errs)
	}
	if got := prepared.RelationshipObservations[0].PredicateCandidates; len(got) != 1 || got[0].PredicateKey != "works_on" {
		t.Fatalf("typed value predicate candidates = %#v", got)
	}
}

func TestDecodeV2SemanticReviewResponseJSONRejectsUnknownFields(t *testing.T) {
	raw := []byte(`{
		"request_id":"verify-1",
		"security_signals":[],
		"entity_results":[],
		"relationship_results":[],
		"unexpected":"nope"
	}`)
	if _, err := DecodeV2SemanticReviewResponseJSON(raw); err == nil {
		t.Fatal("DecodeV2SemanticReviewResponseJSON accepted unknown field")
	}
}

func TestDecodeV2SemanticReviewResponseJSONRejectsTrailingJSON(t *testing.T) {
	raw := []byte(`{"request_id":"verify-1","security_signals":[],"entity_results":[],"relationship_results":[]} {}`)
	if _, err := DecodeV2SemanticReviewResponseJSON(raw); err == nil {
		t.Fatal("DecodeV2SemanticReviewResponseJSON accepted trailing JSON")
	}
}

func TestValidateV2SemanticReviewResponseRejectsMissingRequiredArrays(t *testing.T) {
	req, errs := PrepareV2SemanticReviewRequest(v2SemanticReviewTestRequest())
	if len(errs) != 0 {
		t.Fatalf("request errors = %#v", errs)
	}
	joined := v2SemanticJoinedErrors(ValidateV2SemanticReviewResponse(req, V2SemanticReviewResponse{
		RequestID: req.RequestID,
	}))
	for _, want := range []string{"security_signals: is required", "entity_results: is required", "relationship_results: is required"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestValidateV2SemanticReviewResponseAcceptsCompleteResponseAndSecurityQuarantine(t *testing.T) {
	req, errs := PrepareV2SemanticReviewRequest(v2SemanticReviewTestRequest())
	if len(errs) != 0 {
		t.Fatalf("request errors = %#v", errs)
	}
	entMark := "ent-mark"
	entProject := "ent-dense-mem"
	predicate := "works_on"
	resp := V2SemanticReviewResponse{
		RequestID: req.RequestID,
		SecuritySignals: []V2SemanticSecuritySignal{{
			EvidenceID: "ev_1",
			Kind:       "instruction_override",
			Start:      0,
			End:        4,
		}},
		EntityResults: []V2SemanticEntityResult{
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &entMark, Confidence: 0.9, Rationale: "allowed candidate"},
			{Ref: "project_1", Action: "reuse", CandidateEntityID: &entProject, Confidence: 0.9, Rationale: "allowed candidate"},
		},
		RelationshipResults: []V2SemanticRelationshipReviewResult{
			{Ref: "rel_1", PredicateStatus: "resolved", PredicateKey: &predicate, EvidenceVerdict: string(domain.V2VerificationEntailed), Confidence: 0.9, Rationale: "allowed predicate"},
		},
	}
	if errs := ValidateV2SemanticReviewResponse(req, resp); len(errs) != 0 {
		t.Fatalf("valid response errors = %#v", errs)
	}
}

func TestValidateV2SemanticReviewResponseAcceptsReviewRequiredOutcomes(t *testing.T) {
	req, errs := PrepareV2SemanticReviewRequest(v2SemanticReviewTestRequest())
	if len(errs) != 0 {
		t.Fatalf("request errors = %#v", errs)
	}
	entMark := "ent-mark"
	resp := V2SemanticReviewResponse{
		RequestID:       req.RequestID,
		SecuritySignals: []V2SemanticSecuritySignal{},
		EntityResults: []V2SemanticEntityResult{
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &entMark, Confidence: 0.9, Rationale: "allowed candidate"},
			{Ref: "project_1", Action: "ambiguous", CandidateEntityID: nil, Confidence: 0.5, Rationale: "not enough identity context"},
		},
		RelationshipResults: []V2SemanticRelationshipReviewResult{
			{Ref: "rel_1", PredicateStatus: "needs_review", PredicateKey: nil, EvidenceVerdict: string(domain.V2VerificationInsufficient), Confidence: 0.5, Rationale: "predicate requires review"},
		},
	}
	if errs := ValidateV2SemanticReviewResponse(req, resp); len(errs) != 0 {
		t.Fatalf("review-required response errors = %#v", errs)
	}
}

func TestValidateV2SemanticReviewResponseRejectsIncompleteDuplicateAndOutOfAllowlist(t *testing.T) {
	req, errs := PrepareV2SemanticReviewRequest(v2SemanticReviewTestRequest())
	if len(errs) != 0 {
		t.Fatalf("request errors = %#v", errs)
	}
	badPredicate := "invented_predicate"
	entMark := "ent-mark"
	resp := V2SemanticReviewResponse{
		RequestID:       req.RequestID,
		SecuritySignals: []V2SemanticSecuritySignal{},
		EntityResults: []V2SemanticEntityResult{
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &entMark, Confidence: 0.9, Rationale: "allowed candidate"},
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &entMark, Confidence: 0.9, Rationale: "duplicate"},
			{Ref: "unknown", Action: "ambiguous", CandidateEntityID: nil, Confidence: 0.5, Rationale: "unknown ref"},
		},
		RelationshipResults: []V2SemanticRelationshipReviewResult{
			{Ref: "rel_1", PredicateStatus: "resolved", PredicateKey: &badPredicate, EvidenceVerdict: string(domain.V2VerificationEntailed), Confidence: 0.9, Rationale: "bad predicate"},
		},
	}

	errs = ValidateV2SemanticReviewResponse(req, resp)
	joined := v2SemanticJoinedErrors(errs)
	for _, want := range []string{
		"entity_results[1].ref: is duplicated",
		"entity_results[2].ref: is unknown",
		"entity_results: missing result for project_1",
		"relationship_results[0].predicate_key: is outside predicate allowlist",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestValidateV2SemanticReviewResponseRejectsInvalidSecuritySignal(t *testing.T) {
	req, errs := PrepareV2SemanticReviewRequest(v2SemanticReviewTestRequest())
	if len(errs) != 0 {
		t.Fatalf("request errors = %#v", errs)
	}
	entMark := "ent-mark"
	entProject := "ent-dense-mem"
	predicate := "works_on"
	resp := V2SemanticReviewResponse{
		RequestID: req.RequestID,
		SecuritySignals: []V2SemanticSecuritySignal{{
			EvidenceID: "unknown",
			Kind:       "instruction_override",
			Start:      0,
			End:        4,
		}, {
			EvidenceID: "ev_1",
			Kind:       "bad_kind",
			Start:      10,
			End:        4,
		}},
		EntityResults: []V2SemanticEntityResult{
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &entMark, Confidence: 0.9, Rationale: "allowed candidate"},
			{Ref: "project_1", Action: "reuse", CandidateEntityID: &entProject, Confidence: 0.9, Rationale: "allowed candidate"},
		},
		RelationshipResults: []V2SemanticRelationshipReviewResult{
			{Ref: "rel_1", PredicateStatus: "resolved", PredicateKey: &predicate, EvidenceVerdict: string(domain.V2VerificationEntailed), Confidence: 0.9, Rationale: "allowed predicate"},
		},
	}
	joined := v2SemanticJoinedErrors(ValidateV2SemanticReviewResponse(req, resp))
	if !strings.Contains(joined, "unknown evidence_id") || !strings.Contains(joined, "unsupported security signal kind") {
		t.Fatalf("security signal errors = %s", joined)
	}
}

func TestV2SemanticReviewResponseSchemaIsClosed(t *testing.T) {
	data, err := json.Marshal(V2SemanticReviewResponseSchema())
	if err != nil {
		t.Fatalf("schema marshal failed: %v", err)
	}
	schema := string(data)
	if !strings.Contains(schema, `"additionalProperties":false`) {
		t.Fatalf("schema is not closed: %s", schema)
	}
	if !strings.Contains(schema, `"relationship_results"`) {
		t.Fatalf("schema missing relationship results: %s", schema)
	}
}

func TestV2ProviderProposalSchemaExposesPredicateAndEvidenceContract(t *testing.T) {
	schema := V2ProviderProposalSchema()
	properties := schema["properties"].(map[string]any)
	for _, key := range []string{"predicate_options", "evidence", "entity_proposals", "relationship_proposals"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("V2ProviderProposalSchema missing %s", key)
		}
	}
}

func v2SemanticReviewTestRequest() V2SemanticReviewRequest {
	content := "Mark works\non Dense-Mem."
	return V2SemanticReviewRequest{
		RequestID:      "verify-1",
		TeamID:         "team-a",
		OwnerProfileID: "owner-a",
		Evidence: []V2SemanticReviewEvidence{{
			EvidenceID:    "ev_1",
			FragmentID:    "frag-1",
			EvidenceIndex: 0,
			Content:       content,
		}},
		EntityMentions: []V2SemanticEntityMention{
			{
				Ref:        "person_1",
				Surface:    "Mark",
				Kind:       "person",
				EvidenceID: "ev_1",
				Start:      0,
				End:        4,
				Candidates: []V2SemanticEntityCandidate{{
					EntityID:      "ent-mark",
					CanonicalName: "Mark Huang",
					Kind:          "person",
					TeamID:        "team-a",
					Status:        "active",
				}},
			},
			{
				Ref:        "project_1",
				Surface:    "Dense-Mem",
				Kind:       "project",
				EvidenceID: "ev_1",
				Start:      strings.Index(content, "Dense-Mem"),
				End:        strings.Index(content, "Dense-Mem") + len("Dense-Mem"),
				Candidates: []V2SemanticEntityCandidate{{
					EntityID:      "ent-dense-mem",
					CanonicalName: "Dense-Mem",
					Kind:          "project",
					TeamID:        "team-a",
					Status:        "active",
				}},
			},
		},
		RelationshipObservations: []V2SemanticRelationshipObservation{{
			Ref:               "rel_1",
			SubjectRef:        "person_1",
			OriginalPredicate: "works on",
			ObjectRef:         "project_1",
			EvidenceID:        "ev_1",
			Quote:             content,
			Start:             0,
			End:               len(content),
			PredicateCandidates: []V2SemanticPredicateCandidate{{
				PredicateKey:        "works_on",
				Version:             1,
				AllowedSubjectKinds: []string{"person"},
				AllowedObjectKinds:  []string{"project"},
				RelationshipKind:    "state",
				CurrentCardinality:  "many",
				LifecycleState:      "active",
			}},
		}},
	}
}

func v2SemanticTestRuneIndex(content string, substring string) int {
	index := strings.Index(content, substring)
	if index < 0 {
		return -1
	}
	return len([]rune(content[:index]))
}

func v2SemanticJoinedErrors(errs []V2SemanticValidationError) string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return strings.Join(out, "\n")
}
