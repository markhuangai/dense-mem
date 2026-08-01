package verifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareDreamGenerationRequestRequiresEvidenceGroundedDirectedPath(t *testing.T) {
	request := dreamGenerationTestRequest(t)
	prepared, errs := PrepareDreamGenerationRequest(request, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	assert.Equal(t, "dream-request-1", prepared.RequestID)
	assert.Equal(t, "path-1", prepared.Paths[0].PathRef)

	request.Paths[0].Premises[1].FromRef = "node-a"
	_, errs = PrepareDreamGenerationRequest(request, DefaultSemanticAssessmentLimits())
	require.NotEmpty(t, errs)
	assert.Contains(t, openAIValidationSummary(errs), "A -> B then B -> C")

	request = dreamGenerationTestRequest(t)
	request.Paths[0].Premises[0].Evidence = nil
	_, errs = PrepareDreamGenerationRequest(request, DefaultSemanticAssessmentLimits())
	require.NotEmpty(t, errs)
	assert.Contains(t, openAIValidationSummary(errs), "complete excerpts")
}

func TestPrepareDreamGenerationRequestNormalizesBoundsAndRejectsDurableReferences(t *testing.T) {
	request := dreamGenerationTestRequest(t)
	request.RequestID = "  dream-request-1  "
	request.MaxOutputs = 0
	request.Paths[0].AllowedPredicates = append(request.Paths[0].AllowedPredicates,
		DreamGenerationPredicate{
			PredicateRef:       "predicate-a",
			Label:              "depends on",
			RelationshipKind:   "state",
			CurrentCardinality: "many",
		},
	)

	prepared, errs := PrepareDreamGenerationRequest(request, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	assert.Equal(t, DreamGenerationMaxOutputs, prepared.MaxOutputs)
	assert.Equal(t, "dream-request-1", prepared.RequestID)
	assert.Equal(t, "predicate-a", prepared.Paths[0].AllowedPredicates[0].PredicateRef)

	for name, mutate := range map[string]func(*DreamGenerationRequest){
		"rejects a durable request reference": func(req *DreamGenerationRequest) {
			req.RequestID = "11111111-1111-1111-1111-111111111111"
		},
		"rejects duplicate evidence references": func(req *DreamGenerationRequest) {
			req.Paths[0].Premises[1].Evidence[0].EvidenceRef = "evidence-a"
		},
		"requires an active anchor": func(req *DreamGenerationRequest) {
			req.Paths[0].Premises[0].Status = "pending_evidence"
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := dreamGenerationTestRequest(t)
			mutate(&invalid)

			_, validationErrors := PrepareDreamGenerationRequest(invalid, DefaultSemanticAssessmentLimits())
			require.NotEmpty(t, validationErrors)
		})
	}
}

func TestPrepareDreamGenerationRequestRejectsMalformedPathFieldsAndBudgets(t *testing.T) {
	request := dreamGenerationTestRequest(t)
	request.MaxOutputs = DreamGenerationMaxOutputs + 1
	path := &request.Paths[0]
	path.Subject.Ref = " "
	path.Middle.Ref = " "
	path.Object.Ref = " "
	path.Subject.Display = " "
	path.Object.Kind = " "
	path.Premises[0].PremiseRef = " "
	path.Premises[1].PremiseRef = " "
	path.Premises[0].RelationshipRef = " "
	path.Premises[1].RelationshipRef = " "
	path.Premises[0].PredicateLabel = " "
	path.Premises[0].RelationshipVersion = 0
	path.Premises[0].Status = "retired"
	path.Premises[0].FromRef = "unknown-node"
	path.Premises[0].ToRef = "unknown-node"
	path.Premises[1].Evidence[0].EvidenceRef = "evidence-a"
	path.Premises[0].Evidence = append(
		path.Premises[0].Evidence,
		DreamGenerationEvidence{EvidenceRef: " ", Content: " ", Authority: " "},
		DreamGenerationEvidence{EvidenceRef: " ", Content: " ", Authority: " "},
	)
	path.AllowedPredicates[0] = DreamGenerationPredicate{}
	path.AllowedPredicates = append(path.AllowedPredicates, DreamGenerationPredicate{
		RelationshipKind: "unknown", CurrentCardinality: "unknown",
	})
	request.Paths = append(request.Paths, request.Paths[0])

	prepared, errs := PrepareDreamGenerationRequest(request, DefaultSemanticAssessmentLimits())
	require.NotEmpty(t, errs)
	assert.Equal(t, DreamGenerationMaxOutputs, prepared.MaxOutputs)
	summary := semanticAssessmentJoinedErrors(errs)
	assert.Contains(t, summary, "must be unique")
	assert.Contains(t, summary, "must reference a path node")
	assert.Contains(t, summary, "must contain between 1 and 2 complete excerpts")
	assert.Contains(t, summary, "must include an active anchor")
	assert.Contains(t, summary, "relationship_kind")
	assert.Contains(t, summary, "current_cardinality")

	_, emptyErrs := PrepareDreamGenerationRequest(
		DreamGenerationRequest{RequestID: "dream-request-empty"},
		DefaultSemanticAssessmentLimits(),
	)
	require.NotEmpty(t, emptyErrs)

	limited := DefaultSemanticAssessmentLimits()
	limited.MaxInputTokens = 1
	_, budgetErrs := PrepareDreamGenerationRequest(dreamGenerationTestRequest(t), limited)
	require.NotEmpty(t, budgetErrs)
	assert.Contains(t, openAIValidationSummary(budgetErrs), "input_tokens")
}

func TestPrepareDreamGenerationResponseRequiresAllowedTargetAndEvidenceFromBothPremises(t *testing.T) {
	request := dreamGenerationTestRequest(t)
	response := DreamGenerationResponse{
		RequestID: request.RequestID,
		Proposals: []DreamGenerationProposal{{
			PathRef:         "path-1",
			PredicateRef:    "predicate-uses",
			Statement:       "A may use C.",
			Rationale:       "Both supplied premises support a possible connection.",
			WhatIf:          "What if the connection needs independent confirmation?",
			PossibleOutcome: "Collect new evidence before treating it as knowledge.",
			Likelihood:      0.45,
			Confidence:      0.51,
			EvidenceRefs:    []string{"evidence-a", "evidence-b"},
		}},
	}
	_, errs := PrepareDreamGenerationResponse(request, response)
	require.Empty(t, errs)

	response.Proposals[0].PredicateRef = "unknown-predicate"
	_, errs = PrepareDreamGenerationResponse(request, response)
	require.NotEmpty(t, errs)
	assert.Contains(t, openAIValidationSummary(errs), "predicate_ref")

	response = DreamGenerationResponse{
		RequestID: request.RequestID,
		Proposals: []DreamGenerationProposal{{
			PathRef:         "path-1",
			PredicateRef:    "predicate-uses",
			Statement:       "A may use C.",
			Rationale:       "Both supplied premises support a possible connection.",
			WhatIf:          "What if the connection needs independent confirmation?",
			PossibleOutcome: "Collect new evidence before treating it as knowledge.",
			Likelihood:      0.45,
			Confidence:      0.51,
			EvidenceRefs:    []string{"evidence-a", "evidence-a"},
		}},
	}
	_, errs = PrepareDreamGenerationResponse(request, response)
	require.NotEmpty(t, errs)
	assert.Contains(t, openAIValidationSummary(errs), "evidence_refs")
}

func TestPrepareDreamGenerationResponseRejectsMalformedAndDuplicateProposals(t *testing.T) {
	request := dreamGenerationTestRequest(t)
	request.MaxOutputs = 1
	proposal := validDreamGenerationProposal()
	proposal.Statement = ""
	proposal.Likelihood = 1.1
	proposal.Confidence = -0.1
	proposal.EvidenceRefs = []string{"evidence-a", "unknown-evidence"}

	response, errs := PrepareDreamGenerationResponse(request, DreamGenerationResponse{
		RequestID: "wrong-request",
		Proposals: []DreamGenerationProposal{
			proposal,
			validDreamGenerationProposal(),
		},
	})
	require.NotEmpty(t, errs)
	assert.Equal(t, "wrong-request", response.RequestID)
	summary := openAIValidationSummary(errs)
	assert.Contains(t, summary, "request_id")
	assert.Contains(t, summary, "no more than 1 proposals")
	assert.Contains(t, summary, "duplicates a proposed target")
	assert.Contains(t, summary, "statement")
	assert.Contains(t, summary, "likelihood")
	assert.Contains(t, summary, "confidence")
	assert.Contains(t, summary, "must reference supplied evidence")

	normalized, normalizedErrs := PrepareDreamGenerationResponse(request, DreamGenerationResponse{
		RequestID: request.RequestID,
	})
	require.Empty(t, normalizedErrs)
	assert.NotNil(t, normalized.Proposals)
}

func TestDecodeDreamGenerationResponseRejectsUnknownAndDuplicateFields(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	for _, payload := range []string{
		`{"request_id":"dream-request-1","proposals":[],"unexpected":true}`,
		`{"request_id":"dream-request-1","request_id":"different","proposals":[]}`,
		`{"request_id":"dream-request-1","proposals":[{"path_ref":"path-1","path_ref":"other"}]}`,
	} {
		_, err := DecodeDreamGenerationResponseJSON([]byte(payload), limits)
		require.Error(t, err, payload)
	}
}

func TestDecodeDreamGenerationResponseRequiresCompleteBoundedJSON(t *testing.T) {
	raw := []byte(`{"request_id":"dream-request-1","proposals":[]}`)
	decoded, err := DecodeDreamGenerationResponseJSON(raw, DefaultSemanticAssessmentLimits())
	require.NoError(t, err)
	assert.Equal(t, "dream-request-1", decoded.RequestID)
	assert.NotNil(t, decoded.Proposals)

	_, err = DecodeDreamGenerationResponseJSON(
		append(append([]byte(nil), raw...), []byte(` {}`)...),
		DefaultSemanticAssessmentLimits(),
	)
	require.ErrorContains(t, err, "must be an object")

	limited := DefaultSemanticAssessmentLimits()
	limited.MaxOutputTokens = 1
	_, err = DecodeDreamGenerationResponseJSON(raw, limited)
	require.ErrorContains(t, err, "token limit")
}

func TestOpenAIVerifierGeneratesEvidenceGroundedDream(t *testing.T) {
	var received openAIVerifierRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.NoError(t, json.NewDecoder(request.Body).Decode(&received))
		assert.Equal(t, DreamGenerationSchemaName, received.ResponseFormat.JSONSchema.Name)
		assert.Contains(t, received.Messages[0].Content, "evidence-grounded relationship hypothesis generator")
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"request_id":"dream-request-1","proposals":[{"path_ref":"path-1","predicate_ref":"predicate-uses","statement":"A may use C.","rationale":"The two supplied premises make this a possibility.","what_if":"What if independent evidence confirms the connection?","possible_outcome":"Collect independent evidence before accepting it.","likelihood":0.45,"confidence":0.51,"evidence_refs":["evidence-a","evidence-b"]}]}`}}},
			"usage":   map[string]any{"prompt_tokens": 42, "completion_tokens": 18},
		}))
	}))
	defer srv.Close()

	provider := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "dream-model"), srv.Client())
	response, err := provider.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	require.NoError(t, err)
	require.Len(t, response.Proposals, 1)
	assert.Equal(t, 1, response.ProviderTurns)
	assert.Equal(t, 42, response.InputTokens)
	assert.Equal(t, "path-1", response.Proposals[0].PathRef)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(received.Messages[1].Content), &payload))
	assert.NotContains(t, received.Messages[1].Content, "11111111-1111-1111-1111-111111111111")
	assert.Contains(t, received.Messages[1].Content, "A relates to B.")
	assert.NotContains(t, payload, "team_id")
}

func TestOpenAIVerifierRegeneratesCompleteDreamResponseAfterValidationFailure(t *testing.T) {
	calls := 0
	var secondRequest openAIVerifierRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		if calls == 2 {
			require.NoError(t, json.NewDecoder(request.Body).Decode(&secondRequest))
		}
		content := `{"request_id":"dream-request-1","proposals":[{"path_ref":"path-1","predicate_ref":"predicate-uses","statement":"A may use C.","rationale":"The two supplied premises make this a possibility.","what_if":"What if independent evidence confirms the connection?","possible_outcome":"Collect independent evidence before accepting it.","likelihood":0.45,"confidence":0.51,"evidence_refs":["evidence-a","unknown-evidence"]}]}`
		if calls == 2 {
			content = `{"request_id":"dream-request-1","proposals":[{"path_ref":"path-1","predicate_ref":"predicate-uses","statement":"A may use C.","rationale":"The two supplied premises make this a possibility.","what_if":"What if independent evidence confirms the connection?","possible_outcome":"Collect independent evidence before accepting it.","likelihood":0.45,"confidence":0.51,"evidence_refs":["evidence-a","evidence-b"]}]}`
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
			"usage":   map[string]any{"prompt_tokens": 42, "completion_tokens": 18},
		}))
	}))
	defer srv.Close()

	provider := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "dream-model"), srv.Client())
	response, err := provider.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, 2, response.ProviderTurns)
	assert.Equal(t, []string{"evidence-a", "evidence-b"}, response.Proposals[0].EvidenceRefs)
	require.Len(t, secondRequest.Messages, 4)
	assert.Contains(t, secondRequest.Messages[3].Content, "validation_errors")
	assert.Contains(t, secondRequest.Messages[3].Content, "complete replacement")
}

func TestOpenAIVerifierRejectsDreamResponseAfterBoundedRegeneration(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": `{"request_id":"dream-request-1","proposals":[{"path_ref":"path-1","predicate_ref":"predicate-uses","statement":"A may use C.","rationale":"The two supplied premises make this a possibility.","what_if":"What if independent evidence confirms the connection?","possible_outcome":"Collect independent evidence before accepting it.","likelihood":0.45,"confidence":0.51,"evidence_refs":["evidence-a","unknown-evidence"]}]}`,
			}}},
		}))
	}))
	defer srv.Close()

	provider := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "dream-model"), srv.Client())
	_, err := provider.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	require.Error(t, err)
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	assert.Equal(t, SemanticAssessmentMaxProviderTurns, malformed.Attempts)
	assert.Equal(t, SemanticAssessmentMaxProviderTurns, calls)
}

func validDreamGenerationProposal() DreamGenerationProposal {
	return DreamGenerationProposal{
		PathRef:         "path-1",
		PredicateRef:    "predicate-uses",
		Statement:       "A may use C.",
		Rationale:       "Both supplied premises support a possible connection.",
		WhatIf:          "What if the connection needs independent confirmation?",
		PossibleOutcome: "Collect new evidence before treating it as knowledge.",
		Likelihood:      0.45,
		Confidence:      0.51,
		EvidenceRefs:    []string{"evidence-a", "evidence-b"},
	}
}

func dreamGenerationTestRequest(t *testing.T) DreamGenerationRequest {
	t.Helper()
	request, errs := PrepareDreamGenerationRequest(DreamGenerationRequest{
		RequestID:  "dream-request-1",
		MaxOutputs: 3,
		Paths: []DreamGenerationPath{{
			PathRef: "path-1",
			Subject: DreamGenerationNode{Ref: "node-a", Display: "A", Kind: "project"},
			Middle:  DreamGenerationNode{Ref: "node-b", Display: "B", Kind: "product"},
			Object:  DreamGenerationNode{Ref: "node-c", Display: "C", Kind: "concept"},
			Premises: []DreamGenerationPremise{
				{PremiseRef: "premise-1", RelationshipRef: "relationship-1", PredicateLabel: "relates to", RelationshipVersion: 2, Status: "active", FromRef: "node-a", ToRef: "node-b", Evidence: []DreamGenerationEvidence{{EvidenceRef: "evidence-a", Content: "A relates to B.", SourceGroupKey: "source-a", Authority: "primary"}}},
				{PremiseRef: "premise-2", RelationshipRef: "relationship-2", PredicateLabel: "informs", RelationshipVersion: 4, Status: "pending_evidence", FromRef: "node-b", ToRef: "node-c", Evidence: []DreamGenerationEvidence{{EvidenceRef: "evidence-b", Content: "B informs C.", SourceGroupKey: "source-b", Authority: "primary"}}},
			},
			AllowedPredicates: []DreamGenerationPredicate{{PredicateRef: "predicate-uses", Label: "uses", RelationshipKind: "state", CurrentCardinality: "many"}},
		}},
	}, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	return request
}
