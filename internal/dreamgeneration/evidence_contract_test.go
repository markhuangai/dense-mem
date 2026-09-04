package dreamgeneration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

func TestEvidenceDiscoveryRequestAndResponseRequireTargetGrounding(t *testing.T) {
	request := evidenceDiscoveryTestRequest(t)
	start := evidenceDiscoveryBoundaryRef(request.Contexts[0], 0)
	end := evidenceDiscoveryBoundaryRef(request.Contexts[0], len([]rune(request.Contexts[0].Content)))
	valid := EvidenceDiscoveryResponse{
		RequestID: request.RequestID,
		Proposals: []EvidenceDiscoveryProposal{{
			SubjectRef: "node_subject", PredicateRef: "predicate_uses", ObjectRef: "node_object",
			Statement: "A may use B.", Rationale: "The target evidence names the connection.",
			WhatIf: "What if the connection is confirmed?", PossibleOutcome: "Review the proposed relationship.",
			Likelihood: 0.5, Confidence: 0.6,
			Derivations: []EvidenceDiscoveryDerivation{{EvidenceRef: request.TargetRef, StartRef: start, EndRef: end}},
		}},
	}
	_, errs := PrepareEvidenceDiscoveryResponse(request, valid)
	require.Empty(t, errs)

	missingTarget := valid
	missingTarget.Proposals = append([]EvidenceDiscoveryProposal(nil), valid.Proposals...)
	missingTarget.Proposals[0].Derivations = []EvidenceDiscoveryDerivation{{
		EvidenceRef: "evidence_context_1",
		StartRef:    evidenceDiscoveryBoundaryRef(request.Contexts[1], 0),
		EndRef:      evidenceDiscoveryBoundaryRef(request.Contexts[1], len([]rune(request.Contexts[1].Content))),
	}}
	_, errs = PrepareEvidenceDiscoveryResponse(request, missingTarget)
	require.NotEmpty(t, errs)
	require.Contains(t, joinedErrors(errs), "must cite the target evidence")

	badEndpoint := valid
	badEndpoint.Proposals = append([]EvidenceDiscoveryProposal(nil), valid.Proposals...)
	badEndpoint.Proposals[0].ObjectRef = "node_value"
	_, errs = PrepareEvidenceDiscoveryResponse(request, badEndpoint)
	require.NotEmpty(t, errs)
	require.Contains(t, joinedErrors(errs), "predicate does not allow the supplied object kind")

	duplicate := valid
	duplicate.Proposals = append([]EvidenceDiscoveryProposal(nil), valid.Proposals...)
	duplicate.Proposals[0].Derivations = append([]EvidenceDiscoveryDerivation(nil), valid.Proposals[0].Derivations[0], valid.Proposals[0].Derivations[0])
	_, errs = PrepareEvidenceDiscoveryResponse(request, duplicate)
	require.Contains(t, joinedErrors(errs), "duplicates a cited evidence span")
}

func TestEvidenceDiscoveryResponseDecodeRejectsUnknownDuplicateAndTrailingJSON(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	for _, raw := range []string{
		`{"request_id":"r","proposals":[],"extra":true}`,
		`{"request_id":"r","request_id":"r","proposals":[]}`,
		`{"request_id":"r","proposals":[]} {}`,
	} {
		_, err := DecodeEvidenceDiscoveryResponseJSON([]byte(raw), limits)
		require.Error(t, err, "raw response %q", raw)
	}
}

func TestEvidenceDiscoveryProviderRepairsOneCompleteMalformedResponse(t *testing.T) {
	transport := &evidenceDiscoveryTransportStub{}
	provider := NewProvider(transport, "dream-model", DefaultSemanticAssessmentLimits())
	request := evidenceDiscoveryTestRequest(t)

	response, err := provider.GenerateEvidenceDiscoveries(context.Background(), request)
	require.NoError(t, err)
	require.Empty(t, response.Proposals)
	require.Equal(t, 2, response.ProviderTurns)
	require.Len(t, transport.requests, 2)
	require.Contains(t, transport.requests[1].Messages[3].Content, "complete replacement JSON object")
}

func TestEvidenceDiscoveryProviderExhaustsMalformedResponsesWithoutPartialResult(t *testing.T) {
	transport := &evidenceDiscoveryTransportStub{alwaysInvalid: true}
	provider := NewProvider(transport, "dream-model", DefaultSemanticAssessmentLimits())
	_, err := provider.GenerateEvidenceDiscoveries(context.Background(), evidenceDiscoveryTestRequest(t))
	var malformed *modelprovider.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Equal(t, "malformed_exhausted", malformed.FailureClass)
	require.Equal(t, DreamGenerationMaxProviderTurns, malformed.Attempts)
}

func TestEvidenceDiscoveryRequestPreservesExactWhitespaceAndConcreteNodeKinds(t *testing.T) {
	request, errs := PrepareEvidenceDiscoveryRequest(EvidenceDiscoveryRequest{
		RequestID: "evidence-request-whitespace", MaxOutputs: 1, TargetRef: "evidence_target",
		Contexts: []EvidenceDiscoveryContext{{EvidenceRef: "evidence_target", Content: "  Alice works.  ", Authority: "primary"}},
		Nodes: []EvidenceDiscoveryNode{
			{Ref: "node_subject", Display: "Alice", Kind: "person"},
			{Ref: "node_object", Display: "Project", Kind: "project"},
		},
		AllowedPredicates: []EvidenceDiscoveryPredicate{{
			Ref: "predicate_works_on", Label: "works_on", Version: 1,
			AllowedSubjectKinds: []string{"person"}, AllowedObjectKinds: []string{"project"},
		}},
	}, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	require.Equal(t, "  Alice works.  ", request.Contexts[0].Content)
	start := evidenceDiscoveryBoundaryRef(request.Contexts[0], 2)
	end := evidenceDiscoveryBoundaryRef(request.Contexts[0], 7)
	response := EvidenceDiscoveryResponse{
		RequestID: request.RequestID,
		Proposals: []EvidenceDiscoveryProposal{{
			SubjectRef: "node_subject", PredicateRef: "predicate_works_on", ObjectRef: "node_object",
			Statement: "Alice may work on Project.", Rationale: "The evidence names the connection.",
			WhatIf: "What if the assignment changes?", PossibleOutcome: "Review the assignment.",
			Likelihood: 0.5, Confidence: 0.5,
			Derivations: []EvidenceDiscoveryDerivation{{EvidenceRef: request.TargetRef, StartRef: start, EndRef: end}},
		}},
	}
	prepared, responseErrs := PrepareEvidenceDiscoveryResponse(request, response)
	require.Empty(t, responseErrs)
	require.Equal(t, 2, prepared.Proposals[0].Derivations[0].Start)
	require.Equal(t, 7, prepared.Proposals[0].Derivations[0].End)
}

func TestEvidenceDiscoveryRequestRejectsInvalidBoundsAndDuplicateRefs(t *testing.T) {
	base := EvidenceDiscoveryRequest{
		RequestID: "request", TargetRef: "target", Contexts: []EvidenceDiscoveryContext{{EvidenceRef: "wrong", Content: "content"}},
		Nodes:                []EvidenceDiscoveryNode{{Ref: "node", Display: "Node", Kind: "unsupported"}, {Ref: "node", Display: "", Kind: "entity"}},
		AllowedPredicates:    []EvidenceDiscoveryPredicate{{Ref: "predicate", Label: "", Version: 0}, {Ref: "predicate", Label: "uses", Version: 1}},
		RelatedRelationships: make([]EvidenceDiscoveryRelationship, EvidenceDiscoveryMaxRelated+1),
	}
	_, errs := PrepareEvidenceDiscoveryRequest(base, DefaultSemanticAssessmentLimits())
	require.NotEmpty(t, errs)
	require.Contains(t, joinedErrors(errs), "contexts[0].evidence_ref")
	require.Contains(t, joinedErrors(errs), "nodes[0].kind")
	require.Contains(t, joinedErrors(errs), "allowed_predicates[0].version")
	require.Contains(t, joinedErrors(errs), "related")

	tooLarge := EvidenceDiscoveryRequest{RequestID: "request", TargetRef: "target", Contexts: []EvidenceDiscoveryContext{{EvidenceRef: "target", Content: strings.Repeat("x", EvidenceDiscoveryMaxContentRunes+1), Authority: "primary"}}, AllowedPredicates: []EvidenceDiscoveryPredicate{{Ref: "predicate", Label: "uses", Version: 1}}}
	_, errs = PrepareEvidenceDiscoveryRequest(tooLarge, DefaultSemanticAssessmentLimits())
	require.Contains(t, joinedErrors(errs), "contexts[0].content")
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1
	_, errs = PrepareEvidenceDiscoveryRequest(EvidenceDiscoveryRequest{RequestID: "request", TargetRef: "target", Contexts: []EvidenceDiscoveryContext{{EvidenceRef: "target", Content: "content", Authority: "primary"}}, AllowedPredicates: []EvidenceDiscoveryPredicate{{Ref: "predicate", Label: "uses", Version: 1}}}, limits)
	require.Contains(t, joinedErrors(errs), "input_tokens")
}

func TestEvidenceDiscoveryRequestAppliesBoundsAndRejectsTokenizerInput(t *testing.T) {
	request := evidenceDiscoveryTestRequest(t)
	request.MaxOutputs = 0
	prepared, errs := PrepareEvidenceDiscoveryRequest(request, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	require.Equal(t, EvidenceDiscoveryMaxOutputs, prepared.MaxOutputs)

	request.MaxOutputs = EvidenceDiscoveryMaxOutputs + 1
	prepared, errs = PrepareEvidenceDiscoveryRequest(request, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	require.Equal(t, EvidenceDiscoveryMaxOutputs, prepared.MaxOutputs)

	limits := DefaultSemanticAssessmentLimits()
	limits.Tokenizer = "not-a-tokenizer"
	_, errs = PrepareEvidenceDiscoveryRequest(evidenceDiscoveryTestRequest(t), limits)
	require.Contains(t, joinedErrors(errs), "tokenizer")
}

func TestEvidenceDiscoveryRequestRejectsEmptyAndOversizedCollections(t *testing.T) {
	nodes := make([]EvidenceDiscoveryNode, EvidenceDiscoveryMaxNodes+1)
	for index := range nodes {
		nodes[index] = EvidenceDiscoveryNode{Ref: fmt.Sprintf("node_%d", index), Display: "node", Kind: "entity"}
	}
	predicates := make([]EvidenceDiscoveryPredicate, EvidenceDiscoveryMaxPredicates+1)
	for index := range predicates {
		predicates[index] = EvidenceDiscoveryPredicate{Ref: fmt.Sprintf("predicate_%d", index), Label: "uses", Version: 1}
	}
	_, errs := PrepareEvidenceDiscoveryRequest(EvidenceDiscoveryRequest{
		RequestID: "request", TargetRef: "target",
		Contexts:          []EvidenceDiscoveryContext{{EvidenceRef: "target", Content: "", Authority: ""}},
		Nodes:             nodes,
		AllowedPredicates: predicates,
	}, DefaultSemanticAssessmentLimits())
	joined := joinedErrors(errs)
	require.Contains(t, joined, "contexts[0].content")
	require.Contains(t, joined, "contexts[0].authority")
	require.Contains(t, joined, "nodes")
	require.Contains(t, joined, "allowed_predicates")
}

func TestEvidenceDiscoveryRequestRejectsInvalidOpaqueReferences(t *testing.T) {
	request := evidenceDiscoveryTestRequest(t)
	request.Contexts[1].EvidenceRef = ""
	_, errs := PrepareEvidenceDiscoveryRequest(request, DefaultSemanticAssessmentLimits())
	require.Contains(t, joinedErrors(errs), "contexts[1].evidence_ref")

	request = evidenceDiscoveryTestRequest(t)
	request.Contexts[1].EvidenceRef = request.Contexts[0].EvidenceRef
	_, errs = PrepareEvidenceDiscoveryRequest(request, DefaultSemanticAssessmentLimits())
	require.Contains(t, joinedErrors(errs), "must be unique")

	request = evidenceDiscoveryTestRequest(t)
	request.Nodes[0].Ref = ""
	_, errs = PrepareEvidenceDiscoveryRequest(request, DefaultSemanticAssessmentLimits())
	require.Contains(t, joinedErrors(errs), "nodes[0].ref")

	request = evidenceDiscoveryTestRequest(t)
	request.AllowedPredicates[0].Ref = ""
	_, errs = PrepareEvidenceDiscoveryRequest(request, DefaultSemanticAssessmentLimits())
	require.Contains(t, joinedErrors(errs), "allowed_predicates[0].ref")
}

func TestEvidenceDiscoveryDecodeEnforcesTokenizerAndOutputBudget(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	limits.Tokenizer = "not-a-tokenizer"
	_, err := DecodeEvidenceDiscoveryResponseJSON([]byte(`{"request_id":"r","proposals":[]}`), limits)
	require.Error(t, err)

	limits = DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 1
	_, err = DecodeEvidenceDiscoveryResponseJSON([]byte(`{"request_id":"r","proposals":[]}`), limits)
	require.ErrorContains(t, err, "exceeds 1 token limit")

	_, err = DecodeEvidenceDiscoveryResponseJSON([]byte(`{"request_id":1,"proposals":[]}`), DefaultSemanticAssessmentLimits())
	require.Error(t, err)
	_, err = DecodeEvidenceDiscoveryResponseJSON([]byte(`{"request_id":"r","proposals":[]} null`), DefaultSemanticAssessmentLimits())
	require.Error(t, err)
}

func TestEvidenceDiscoveryResponseValidationRejectsInvalidTargetAndProposalShapes(t *testing.T) {
	request := evidenceDiscoveryTestRequest(t)
	prepared, errs := PrepareEvidenceDiscoveryResponse(request, EvidenceDiscoveryResponse{RequestID: request.RequestID})
	require.Empty(t, errs)
	require.NotNil(t, prepared.Proposals)

	request.MaxOutputs = 1
	start := evidenceDiscoveryBoundaryRef(request.Contexts[0], 0)
	end := evidenceDiscoveryBoundaryRef(request.Contexts[0], len([]rune(request.Contexts[0].Content)))
	proposal := EvidenceDiscoveryProposal{
		SubjectRef: "node_subject", PredicateRef: "predicate_uses", ObjectRef: "node_object",
		Statement: "statement", Rationale: "rationale", WhatIf: "what if", PossibleOutcome: "outcome",
		Likelihood: 0.5, Confidence: 0.5,
		Derivations: []EvidenceDiscoveryDerivation{
			{EvidenceRef: "unknown", StartRef: start, EndRef: end},
			{EvidenceRef: request.TargetRef, StartRef: "missing", EndRef: end},
			{EvidenceRef: request.TargetRef, StartRef: end, EndRef: start},
		},
	}
	response := EvidenceDiscoveryResponse{RequestID: request.RequestID, Proposals: []EvidenceDiscoveryProposal{proposal, proposal}}
	_, errs = PrepareEvidenceDiscoveryResponse(request, response)
	joined := joinedErrors(errs)
	require.Contains(t, joined, "must contain no more than 1 proposals")
	require.Contains(t, joined, "must reference supplied evidence")
	require.Contains(t, joined, "must use valid ordered evidence boundaries")
	require.Contains(t, joined, "duplicates a proposed target")
	require.Contains(t, joined, "must cite the target evidence")
}

func TestEvidenceDiscoveryResponseValidationCoversInvalidFieldsAndValueKinds(t *testing.T) {
	request := evidenceDiscoveryTestRequest(t)
	response := EvidenceDiscoveryResponse{RequestID: "wrong", Proposals: []EvidenceDiscoveryProposal{{
		SubjectRef: "missing", PredicateRef: "missing", ObjectRef: "missing", Statement: "", Rationale: "", WhatIf: "", PossibleOutcome: "",
		Likelihood: math.NaN(), Confidence: math.Inf(1), Derivations: []EvidenceDiscoveryDerivation{{EvidenceRef: "missing", StartRef: "missing", EndRef: "missing"}},
	}}}
	prepared, errs := PrepareEvidenceDiscoveryResponse(request, response)
	require.Equal(t, "wrong", prepared.RequestID)
	require.NotEmpty(t, errs)
	require.Contains(t, joinedErrors(errs), "request_id")
	require.Contains(t, joinedErrors(errs), "subject_ref")
	require.Contains(t, joinedErrors(errs), "likelihood")

	require.True(t, evidenceDiscoveryNodeIsValue("value"))
	require.True(t, evidenceDiscoveryNodeIsValue("string"))
	require.False(t, evidenceDiscoveryNodeIsValue("person"))
	require.True(t, evidenceDiscoveryKindAllowed([]string{"entity"}, "person"))
	require.True(t, evidenceDiscoveryKindAllowed([]string{"value"}, "string"))
	require.False(t, evidenceDiscoveryKindAllowed([]string{"person"}, "project"))
	require.True(t, evidenceDiscoveryKindAllowed(nil, "project"))

	request.Nodes[0].Kind = "value"
	proposal := EvidenceDiscoveryProposal{
		SubjectRef: "node_subject", PredicateRef: "predicate_uses", ObjectRef: "node_object",
		Statement: "statement", Rationale: "rationale", WhatIf: "what if", PossibleOutcome: "outcome",
		Likelihood: 0.5, Confidence: 0.5,
		Derivations: []EvidenceDiscoveryDerivation{{
			EvidenceRef: request.TargetRef,
			StartRef:    evidenceDiscoveryBoundaryRef(request.Contexts[0], 0),
			EndRef:      evidenceDiscoveryBoundaryRef(request.Contexts[0], len([]rune(request.Contexts[0].Content))),
		}},
	}
	_, errs = PrepareEvidenceDiscoveryResponse(request, EvidenceDiscoveryResponse{RequestID: request.RequestID, Proposals: []EvidenceDiscoveryProposal{proposal}})
	require.Contains(t, joinedErrors(errs), "predicate does not allow the supplied subject kind")
}

func TestEvidenceDiscoveryRawResponseErrorsRejectMalformedNestedArrays(t *testing.T) {
	for _, raw := range []string{
		`{"request_id":"r","proposals":{}}`,
		`{"request_id":"r","proposals":[{"subject_ref":"s","predicate_ref":"p","object_ref":"o","statement":"s","rationale":"r","what_if":"w","possible_outcome":"o","likelihood":0.5,"confidence":0.5,"derivations":{}}]}`,
		`{"request_id":"r","proposals":[{"subject_ref":"s","predicate_ref":"p","object_ref":"o","statement":"s","rationale":"r","what_if":"w","possible_outcome":"o","likelihood":0.5,"confidence":0.5,"derivations":[{"evidence_ref":"e","start_ref":"s","extra":true}]}]}`,
	} {
		require.NotEmpty(t, evidenceDiscoveryResponseRawErrors([]byte(raw)), "raw response %q", raw)
	}
}

func evidenceDiscoveryTestRequest(t *testing.T) EvidenceDiscoveryRequest {
	t.Helper()
	request, errs := PrepareEvidenceDiscoveryRequest(EvidenceDiscoveryRequest{
		RequestID: "evidence-request-1", MaxOutputs: 2, TargetRef: "evidence_target",
		Contexts: []EvidenceDiscoveryContext{
			{EvidenceRef: "evidence_target", Content: "A uses B.", Authority: "primary", SourceGroupKey: "source-a"},
			{EvidenceRef: "evidence_context_1", Content: "A and B share a system.", Authority: "secondary", SourceGroupKey: "source-b"},
		},
		Nodes: []EvidenceDiscoveryNode{
			{Ref: "node_subject", Display: "A", Kind: "entity"},
			{Ref: "node_object", Display: "B", Kind: "entity"},
			{Ref: "node_value", Display: "42", Kind: "value"},
		},
		AllowedPredicates: []EvidenceDiscoveryPredicate{{
			Ref: "predicate_uses", Label: "uses", Version: 1,
			AllowedSubjectKinds: []string{"entity"}, AllowedObjectKinds: []string{"entity"},
			RelationshipKind: "state", CurrentCardinality: "many",
		}},
	}, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	return request
}

func evidenceDiscoveryBoundaryRef(context EvidenceDiscoveryContext, offset int) string {
	for ref, candidate := range context.BoundaryRefs {
		if candidate == offset {
			return ref
		}
	}
	return ""
}

type evidenceDiscoveryTransportStub struct {
	requests       []modelprovider.StructuredRequest
	alwaysInvalid  bool
	completedCalls int
}

func (s *evidenceDiscoveryTransportStub) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	s.requests = append(s.requests, request)
	s.completedCalls++
	var payload struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	if s.alwaysInvalid || s.completedCalls == 1 {
		return modelprovider.StructuredResult{Content: `{"request_id":"` + payload.RequestID + `","proposals":[{"subject_ref":"missing","predicate_ref":"predicate_uses","object_ref":"node_object","statement":"A may use B.","rationale":"reason","what_if":"what if","possible_outcome":"outcome","likelihood":0.5,"confidence":0.6,"derivations":[]}]}`}, nil
	}
	return modelprovider.StructuredResult{Content: validEvidenceDiscoveryResponseJSON(payload.RequestID)}, nil
}

func validEvidenceDiscoveryResponseJSON(requestID string) string {
	return `{"request_id":"` + requestID + `","proposals":[]}`
}
