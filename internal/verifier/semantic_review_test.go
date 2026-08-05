package verifier

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSemanticReviewPrepareFiltersProviderEgressAndMapsWhitespaceQuote(t *testing.T) {
	req := semanticReviewTestRequest()
	req.EntityMentions[0].Candidates = append(req.EntityMentions[0].Candidates,
		SemanticEntityCandidate{
			EntityID:      "ent-other-team",
			CanonicalName: "Mark Other",
			Kind:          "person",
			TeamID:        "team-b",
			Status:        "active",
		},
		SemanticEntityCandidate{
			EntityID:      "ent-retired",
			CanonicalName: "Retired Mark",
			Kind:          "person",
			TeamID:        "team-a",
			Status:        "retired",
		},
	)
	req.RelationshipObservations[0].Quote = "Mark works on Dense-Mem."
	req.RelationshipObservations[0].CorrectionTarget = &RelationshipCorrectionTarget{
		RelationshipID:  " rel-target ",
		ExpectedVersion: 4,
	}
	validFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	validTo := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	req.RelationshipObservations[0].ValidFrom = &validFrom
	req.RelationshipObservations[0].ValidTo = &validTo
	req.RelationshipObservations[0].PredicateCandidates = append(req.RelationshipObservations[0].PredicateCandidates,
		SemanticPredicateCandidate{
			PredicateKey:        "retired_predicate",
			Version:             1,
			AllowedSubjectKinds: []string{"person"},
			AllowedObjectKinds:  []string{"project"},
			RelationshipKind:    "state",
			CurrentCardinality:  "many",
			LifecycleState:      "retired",
		},
	)

	prepared, errs := PrepareSemanticReviewRequest(req)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticReviewRequest errors = %#v", errs)
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
	if got := prepared.RelationshipObservations[0].Polarity; got != "+" {
		t.Fatalf("default polarity = %q, want +", got)
	}
	if got := prepared.RelationshipObservations[0].CorrectionTarget; got == nil || got.RelationshipID != "rel-target" || got.ExpectedVersion != 4 {
		t.Fatalf("correction target = %#v", got)
	}
	raw, err := json.Marshal(prepared)
	if err != nil {
		t.Fatalf("marshal prepared request: %v", err)
	}
	if strings.Contains(string(raw), "correction_target") || strings.Contains(string(raw), "rel-target") {
		t.Fatalf("provider request leaked correction target JSON: %s", raw)
	}
	if !strings.Contains(string(raw), `"valid_from":"2026-07-01T00:00:00Z"`) ||
		!strings.Contains(string(raw), `"valid_to":"2026-12-31T00:00:00Z"`) {
		t.Fatalf("provider request dropped validity bounds JSON: %s", raw)
	}
}

func TestSemanticReviewPrepareUsesCodePointSpans(t *testing.T) {
	req := semanticReviewTestRequest()
	content := "Renée works\non Dense-Mem."
	req.Evidence[0].Content = content
	req.EntityMentions[0].Surface = "Renée"
	req.EntityMentions[0].Start = 0
	req.EntityMentions[0].End = 5
	projectStart := semanticTestRuneIndex(content, "Dense-Mem")
	req.EntityMentions[1].Start = projectStart
	req.EntityMentions[1].End = projectStart + len([]rune("Dense-Mem"))
	req.RelationshipObservations[0].Quote = content
	req.RelationshipObservations[0].Start = 0
	req.RelationshipObservations[0].End = len([]rune(content))

	prepared, errs := PrepareSemanticReviewRequest(req)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticReviewRequest errors = %#v", errs)
	}
	if got := prepared.EntityMentions[0].Surface; got != "Renée" {
		t.Fatalf("surface = %q", got)
	}
	if got := prepared.RelationshipObservations[0].Quote; got != content {
		t.Fatalf("quote = %q", got)
	}
}

func TestSemanticReviewPrepareRejectsStaleSourceAndQuoteMismatch(t *testing.T) {
	req := semanticReviewTestRequest()
	req.Evidence[0].SourceRevisionID = "rev-old"
	req.Evidence[0].CurrentSourceRevisionID = "rev-new"

	_, errs := PrepareSemanticReviewRequest(req)
	joined := semanticJoinedErrors(errs)
	if !strings.Contains(joined, "is not current") {
		t.Fatalf("missing source currentness error: %s", joined)
	}

	req = semanticReviewTestRequest()
	req.RelationshipObservations[0].Quote = "unrelated"
	_, errs = PrepareSemanticReviewRequest(req)
	joined = semanticJoinedErrors(errs)
	if !strings.Contains(joined, "quote does not match") {
		t.Fatalf("missing quote mismatch error: %s", joined)
	}
}

func TestSemanticReviewPrepareRejectsShapeErrors(t *testing.T) {
	req := semanticReviewTestRequest()
	req.RequestID = ""
	req.Evidence = append(req.Evidence, req.Evidence[0])
	req.Evidence[1].Content = " "
	req.EntityMentions = append(req.EntityMentions, req.EntityMentions[0])
	req.EntityMentions[1].Kind = "unsupported"
	req.RelationshipObservations[0].SubjectRef = "missing"
	req.RelationshipObservations[0].ObjectRef = ""
	req.RelationshipObservations[0].ObjectValue = nil
	req.RelationshipObservations[0].Polarity = "?"
	validFrom := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	validTo := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	req.RelationshipObservations[0].ValidFrom = &validFrom
	req.RelationshipObservations[0].ValidTo = &validTo

	_, errs := PrepareSemanticReviewRequest(req)
	joined := semanticJoinedErrors(errs)
	for _, want := range []string{
		"request_id: is required",
		"evidence[1].evidence_id: is duplicated",
		"evidence[1].content: is required",
		"entity_mentions[2].ref: is duplicated",
		"entity_mentions[1].kind: is unsupported",
		"relationship_observations[0].subject_ref: is unknown",
		"relationship_observations[0].object: requires exactly one object_ref or object_value",
		"relationship_observations[0].polarity: is unsupported",
		"relationship_observations[0].valid_to: must not be before valid_from",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestSemanticReviewPrepareSupportsTypedValueEndpointKinds(t *testing.T) {
	req := semanticReviewTestRequest()
	req.RelationshipObservations[0].ObjectRef = ""
	req.RelationshipObservations[0].ObjectValue = &SemanticValueObservation{
		Ref:   "value_1",
		Type:  "string",
		Value: "Dense-Mem",
	}
	req.RelationshipObservations[0].PredicateCandidates[0].AllowedObjectKinds = []string{"string"}

	prepared, errs := PrepareSemanticReviewRequest(req)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticReviewRequest errors = %#v", errs)
	}
	if got := prepared.RelationshipObservations[0].PredicateCandidates; len(got) != 1 || got[0].PredicateKey != "works_on" {
		t.Fatalf("typed value predicate candidates = %#v", got)
	}
}

func TestDecodeSemanticReviewResponseJSONRejectsUnknownFields(t *testing.T) {
	raw := []byte(`{
		"request_id":"verify-1",
		"security_signals":[],
		"entity_results":[],
		"relationship_results":[],
		"unexpected":"nope"
	}`)
	if _, err := DecodeSemanticReviewResponseJSON(raw); err == nil {
		t.Fatal("DecodeSemanticReviewResponseJSON accepted unknown field")
	}
}

func TestDecodeSemanticReviewResponseJSONRejectsTrailingJSON(t *testing.T) {
	raw := []byte(`{"request_id":"verify-1","security_signals":[],"entity_results":[],"relationship_results":[]} {}`)
	if _, err := DecodeSemanticReviewResponseJSON(raw); err == nil {
		t.Fatal("DecodeSemanticReviewResponseJSON accepted trailing JSON")
	}
}

func TestDecodeSemanticReviewResponseJSONAcceptsClosedResponse(t *testing.T) {
	raw := []byte(`{"request_id":"verify-1","security_signals":[],"entity_results":[],"relationship_results":[]}`)
	resp, err := DecodeSemanticReviewResponseJSON(raw)
	if err != nil {
		t.Fatalf("DecodeSemanticReviewResponseJSON returned error: %v", err)
	}
	if resp.RequestID != "verify-1" {
		t.Fatalf("request_id = %q", resp.RequestID)
	}
	if got := (SemanticValidationError{Message: "plain"}).Error(); got != "plain" {
		t.Fatalf("validation error = %q", got)
	}
}

func TestValidateSemanticReviewResponseRejectsMissingRequiredArrays(t *testing.T) {
	req, errs := PrepareSemanticReviewRequest(semanticReviewTestRequest())
	if len(errs) != 0 {
		t.Fatalf("request errors = %#v", errs)
	}
	joined := semanticJoinedErrors(ValidateSemanticReviewResponse(req, SemanticReviewResponse{
		RequestID: req.RequestID,
	}))
	for _, want := range []string{"security_signals: is required", "entity_results: is required", "relationship_results: is required"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestValidateSemanticReviewResponseAcceptsCompleteResponseAndSecurityQuarantine(t *testing.T) {
	req, errs := PrepareSemanticReviewRequest(semanticReviewTestRequest())
	if len(errs) != 0 {
		t.Fatalf("request errors = %#v", errs)
	}
	entMark := "ent-mark"
	entProject := "ent-dense-mem"
	predicate := "works_on"
	resp := SemanticReviewResponse{
		RequestID: req.RequestID,
		SecuritySignals: []SemanticSecuritySignal{{
			EvidenceID: "ev_1",
			Kind:       "instruction_override",
			Start:      0,
			End:        4,
		}},
		EntityResults: []SemanticEntityResult{
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &entMark, Confidence: 0.9, Rationale: "allowed candidate"},
			{Ref: "project_1", Action: "reuse", CandidateEntityID: &entProject, Confidence: 0.9, Rationale: "allowed candidate"},
		},
		RelationshipResults: []SemanticRelationshipReviewResult{
			{Ref: "rel_1", PredicateStatus: "resolved", PredicateKey: &predicate, EvidenceVerdict: string(domain.VerificationEntailed), Confidence: 0.9, Rationale: "allowed predicate"},
		},
	}
	if errs := ValidateSemanticReviewResponse(req, resp); len(errs) != 0 {
		t.Fatalf("valid response errors = %#v", errs)
	}
}

func TestValidateSemanticReviewResponseAcceptsReviewRequiredOutcomes(t *testing.T) {
	req, errs := PrepareSemanticReviewRequest(semanticReviewTestRequest())
	if len(errs) != 0 {
		t.Fatalf("request errors = %#v", errs)
	}
	entMark := "ent-mark"
	resp := SemanticReviewResponse{
		RequestID:       req.RequestID,
		SecuritySignals: []SemanticSecuritySignal{},
		EntityResults: []SemanticEntityResult{
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &entMark, Confidence: 0.9, Rationale: "allowed candidate"},
			{Ref: "project_1", Action: "ambiguous", CandidateEntityID: nil, Confidence: 0.5, Rationale: "not enough identity context"},
		},
		RelationshipResults: []SemanticRelationshipReviewResult{
			{Ref: "rel_1", PredicateStatus: "needs_review", PredicateKey: nil, EvidenceVerdict: string(domain.VerificationInsufficient), Confidence: 0.5, Rationale: "predicate requires review"},
		},
	}
	if errs := ValidateSemanticReviewResponse(req, resp); len(errs) != 0 {
		t.Fatalf("review-required response errors = %#v", errs)
	}
}

func TestValidateSemanticReviewResponseRejectsIncompleteDuplicateAndOutOfAllowlist(t *testing.T) {
	req, errs := PrepareSemanticReviewRequest(semanticReviewTestRequest())
	if len(errs) != 0 {
		t.Fatalf("request errors = %#v", errs)
	}
	badPredicate := "invented_predicate"
	entMark := "ent-mark"
	resp := SemanticReviewResponse{
		RequestID:       req.RequestID,
		SecuritySignals: []SemanticSecuritySignal{},
		EntityResults: []SemanticEntityResult{
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &entMark, Confidence: 0.9, Rationale: "allowed candidate"},
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &entMark, Confidence: 0.9, Rationale: "duplicate"},
			{Ref: "unknown", Action: "ambiguous", CandidateEntityID: nil, Confidence: 0.5, Rationale: "unknown ref"},
		},
		RelationshipResults: []SemanticRelationshipReviewResult{
			{Ref: "rel_1", PredicateStatus: "resolved", PredicateKey: &badPredicate, EvidenceVerdict: string(domain.VerificationEntailed), Confidence: 0.9, Rationale: "bad predicate"},
		},
	}

	errs = ValidateSemanticReviewResponse(req, resp)
	joined := semanticJoinedErrors(errs)
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

func TestValidateSemanticReviewResponseRejectsMalformedResultFields(t *testing.T) {
	req, errs := PrepareSemanticReviewRequest(semanticReviewTestRequest())
	if len(errs) != 0 {
		t.Fatalf("request errors = %#v", errs)
	}
	outsideEntity := "ent-outside"
	projectEntity := "ent-dense-mem"
	predicate := "works_on"
	resp := SemanticReviewResponse{
		RequestID:       req.RequestID,
		SecuritySignals: []SemanticSecuritySignal{},
		EntityResults: []SemanticEntityResult{
			{Ref: "person_1", Action: "reuse", CandidateEntityID: nil, Confidence: -0.1, Rationale: ""},
			{Ref: "project_1", Action: "create", CandidateEntityID: &projectEntity, Confidence: 1.1, Rationale: strings.Repeat("x", 1001)},
			{Ref: "project_1", Action: "merge", CandidateEntityID: nil, Confidence: 0.5, Rationale: "unsupported action"},
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &outsideEntity, Confidence: 0.6, Rationale: "outside allowlist"},
		},
		RelationshipResults: []SemanticRelationshipReviewResult{
			{Ref: "rel_1", PredicateStatus: "resolved", PredicateKey: nil, EvidenceVerdict: "invented", Confidence: -0.2, Rationale: ""},
			{Ref: "rel_1", PredicateStatus: "needs_review", PredicateKey: &predicate, EvidenceVerdict: string(domain.VerificationEntailed), Confidence: 1.2, Rationale: strings.Repeat("r", 1001)},
			{Ref: "rel_1", PredicateStatus: "invented", PredicateKey: nil, EvidenceVerdict: string(domain.VerificationEntailed), Confidence: 0.5, Rationale: "unsupported predicate status"},
			{Ref: "unknown", PredicateStatus: "needs_review", PredicateKey: nil, EvidenceVerdict: string(domain.VerificationEntailed), Confidence: 0.5, Rationale: "unknown relationship"},
		},
	}

	joined := semanticJoinedErrors(ValidateSemanticReviewResponse(req, resp))
	for _, want := range []string{
		"entity_results[0].confidence: must be between 0 and 1",
		"entity_results[0].rationale: is required and must be bounded",
		"entity_results[0].candidate_entity_id: is required for reuse",
		"entity_results[1].candidate_entity_id: must be null",
		"entity_results[2].action: is unsupported",
		"entity_results[3].candidate_entity_id: is outside candidate allowlist",
		"relationship_results[0].confidence: must be between 0 and 1",
		"relationship_results[0].rationale: is required and must be bounded",
		"relationship_results[0].evidence_verdict: is unsupported",
		"relationship_results[0].predicate_key: is required for resolved predicate",
		"relationship_results[1].predicate_key: must be null when predicate_status is needs_review",
		"relationship_results[2].predicate_status: is unsupported",
		"relationship_results[3].ref: is unknown",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestValidateSemanticReviewResponseRejectsInvalidSecuritySignal(t *testing.T) {
	req, errs := PrepareSemanticReviewRequest(semanticReviewTestRequest())
	if len(errs) != 0 {
		t.Fatalf("request errors = %#v", errs)
	}
	entMark := "ent-mark"
	entProject := "ent-dense-mem"
	predicate := "works_on"
	resp := SemanticReviewResponse{
		RequestID: req.RequestID,
		SecuritySignals: []SemanticSecuritySignal{{
			EvidenceID: "unknown",
			Kind:       "instruction_override",
			Start:      0,
			End:        4,
		}, {
			EvidenceID: "ev_1",
			Kind:       "bad_kind",
			Start:      10,
			End:        4,
		}, {
			EvidenceID: "ev_1",
			Kind:       "hidden_control_markup",
			Start:      0,
			End:        4,
		}},
		EntityResults: []SemanticEntityResult{
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &entMark, Confidence: 0.9, Rationale: "allowed candidate"},
			{Ref: "project_1", Action: "reuse", CandidateEntityID: &entProject, Confidence: 0.9, Rationale: "allowed candidate"},
		},
		RelationshipResults: []SemanticRelationshipReviewResult{
			{Ref: "rel_1", PredicateStatus: "resolved", PredicateKey: &predicate, EvidenceVerdict: string(domain.VerificationEntailed), Confidence: 0.9, Rationale: "allowed predicate"},
		},
	}
	joined := semanticJoinedErrors(ValidateSemanticReviewResponse(req, resp))
	if !strings.Contains(joined, "unknown evidence_id") || !strings.Contains(joined, "unsupported security signal kind") || !strings.Contains(joined, "hidden_control_markup requires a hidden control or active markup") {
		t.Fatalf("security signal errors = %s", joined)
	}
}

func TestSemanticHiddenControlMarkupSignalRequiresMatchingSpan(t *testing.T) {
	testCases := []struct {
		name  string
		quote string
		want  bool
	}{
		{name: "visible text", quote: "ordinary evidence", want: false},
		{name: "active markup", quote: "<script", want: true},
		{name: "hidden control", quote: "safe\u200btext", want: true},
		{name: "line break", quote: "line\nbreak", want: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := semanticSecuritySignalSpanMatchesKind("hidden_control_markup", testCase.quote); got != testCase.want {
				t.Fatalf("semanticSecuritySignalSpanMatchesKind() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestSemanticReviewResponseSchemaIsClosed(t *testing.T) {
	data, err := json.Marshal(SemanticReviewResponseSchema())
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

func TestProviderProposalSchemaExposesPredicateAndProposalContract(t *testing.T) {
	schema := ProviderProposalSchema()
	properties := schema["properties"].(map[string]any)
	for _, key := range []string{"predicate_options", "entity_proposals", "relationship_proposals"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("ProviderProposalSchema missing %s", key)
		}
	}
	if _, ok := properties["evidence"]; ok {
		t.Fatal("ProviderProposalSchema should not require evidence echo")
	}
}

func semanticReviewTestRequest() SemanticReviewRequest {
	content := "Mark works\non Dense-Mem."
	return SemanticReviewRequest{
		RequestID:      "verify-1",
		TeamID:         "team-a",
		OwnerProfileID: "owner-a",
		Evidence: []SemanticReviewEvidence{{
			EvidenceID:    "ev_1",
			FragmentID:    "frag-1",
			EvidenceIndex: 0,
			Content:       content,
		}},
		EntityMentions: []SemanticEntityMention{
			{
				Ref:        "person_1",
				Surface:    "Mark",
				Kind:       "person",
				EvidenceID: "ev_1",
				Start:      0,
				End:        4,
				Candidates: []SemanticEntityCandidate{{
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
				Candidates: []SemanticEntityCandidate{{
					EntityID:      "ent-dense-mem",
					CanonicalName: "Dense-Mem",
					Kind:          "project",
					TeamID:        "team-a",
					Status:        "active",
				}},
			},
		},
		RelationshipObservations: []SemanticRelationshipObservation{{
			Ref:               "rel_1",
			SubjectRef:        "person_1",
			OriginalPredicate: "works on",
			ObjectRef:         "project_1",
			EvidenceID:        "ev_1",
			Quote:             content,
			Start:             0,
			End:               len(content),
			PredicateCandidates: []SemanticPredicateCandidate{{
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

func semanticTestRuneIndex(content string, substring string) int {
	index := strings.Index(content, substring)
	if index < 0 {
		return -1
	}
	return len([]rune(content[:index]))
}

func semanticJoinedErrors(errs []SemanticValidationError) string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return strings.Join(out, "\n")
}
