package dreamservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/dreamgeneration"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestProviderGeneratorBuildsBoundedRequestAndMapsProviderResponse(t *testing.T) {
	provider := &providerGeneratorStub{
		model: " dream-model ",
		response: dreamgeneration.DreamGenerationResponse{
			ProviderTurns: 2,
			InputTokens:   31,
			OutputTokens:  17,
			Proposals: []dreamgeneration.DreamGenerationProposal{{
				PathRef:         "path_1",
				PredicateRef:    "predicate_1",
				Statement:       "Dense-Mem may use PostgreSQL.",
				Rationale:       "The two supplied premises support a possible relationship.",
				WhatIf:          "What if independent evidence confirms the relationship?",
				PossibleOutcome: "Collect independent evidence before acceptance.",
				Likelihood:      0.44,
				Confidence:      0.62,
				EvidenceRefs:    []string{"evidence_1", "evidence_2"},
			}},
		},
	}
	generator := NewProviderGenerator(provider)

	generated, diagnostics, err := generator.GenerateWithDiagnostics(context.Background(), "ignored-team", providerGeneratorRequest())

	require.NoError(t, err)
	require.Len(t, generated, 1)
	assert.Equal(t, "dream-model", generator.Model())
	assert.Equal(t, "path_1", generated[0].PathRef)
	assert.Equal(t, "predicate_1", generated[0].PredicateRef)
	assert.Equal(t, "Dense-Mem may use PostgreSQL.", generated[0].Hypothesis)
	assert.Equal(t, []string{"evidence_1", "evidence_2"}, generated[0].EvidenceRefs)
	assert.Equal(t, GenerationDiagnostics{ProviderTurns: 2, ProviderInputTokens: 31, ProviderOutputTokens: 17, ProviderProposals: 1}, diagnostics)

	require.Len(t, provider.requests, 1)
	request := provider.requests[0]
	assert.True(t, strings.HasPrefix(request.RequestID, "dream_request_"))
	assert.Equal(t, DefaultMaxOutputs, request.MaxOutputs)
	require.Len(t, request.Paths, 1)
	path := request.Paths[0]
	assert.Equal(t, "node_1", path.Subject.Ref)
	assert.Equal(t, "node_2", path.Middle.Ref)
	assert.Equal(t, "node_3", path.Object.Ref)
	require.Len(t, path.Premises, 2)
	assert.Equal(t, "node_1", path.Premises[0].FromRef)
	assert.Equal(t, "node_2", path.Premises[0].ToRef)
	assert.Equal(t, "node_2", path.Premises[1].FromRef)
	assert.Equal(t, "node_3", path.Premises[1].ToRef)
	assert.Equal(t, "exact support one", path.Premises[0].Evidence[0].Content)
	assert.Equal(t, "primary", path.Premises[0].Evidence[0].Authority)
	assert.Equal(t, "predicate_1", path.AllowedPredicates[0].PredicateRef)
	payload, err := json.Marshal(request)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "durable-subject")
	assert.NotContains(t, string(payload), "durable-middle")
	assert.NotContains(t, string(payload), "durable-object")
	assert.NotContains(t, string(payload), "durable-relationship-1")
	assert.NotContains(t, string(payload), "internal-source-group")

	generated, err = generator.Generate(context.Background(), "ignored-team", providerGeneratorRequest())
	require.NoError(t, err)
	require.Len(t, generated, 1)
	assert.Len(t, provider.requests, 2)
}

func TestProviderGeneratorRejectsUnavailableAndInvalidRequests(t *testing.T) {
	var nilGenerator *ProviderGenerator
	assert.Empty(t, nilGenerator.Model())
	_, err := nilGenerator.Generate(context.Background(), "team", GenerateRequest{})
	require.ErrorIs(t, err, ErrDreamProviderUnavailable)

	generator := NewProviderGenerator(nil)
	assert.Empty(t, generator.Model())
	_, _, err = generator.GenerateWithDiagnostics(context.Background(), "team", GenerateRequest{})
	require.ErrorIs(t, err, ErrDreamProviderUnavailable)

	provider := &providerGeneratorStub{model: "provider"}
	generator = NewProviderGenerator(provider)
	generated, diagnostics, err := generator.GenerateWithDiagnostics(context.Background(), "team", GenerateRequest{})
	require.NoError(t, err)
	assert.Empty(t, generated)
	assert.Equal(t, GenerationDiagnostics{}, diagnostics)
	assert.Empty(t, provider.requests)

	invalid := providerGeneratorRequest()
	invalid.Paths[0].Premises = invalid.Paths[0].Premises[:1]
	_, _, err = generator.GenerateWithDiagnostics(context.Background(), "team", invalid)
	require.ErrorContains(t, err, "must have two premises")
	assert.Empty(t, provider.requests)

	provider.err = errors.New("provider unavailable")
	_, _, err = generator.GenerateWithDiagnostics(context.Background(), "team", providerGeneratorRequest())
	require.EqualError(t, err, "provider unavailable")
	require.Len(t, provider.requests, 1)

	assert.Empty(t, unavailableGenerator{}.Model())
	_, err = unavailableGenerator{}.Generate(context.Background(), "team", GenerateRequest{})
	require.ErrorIs(t, err, ErrDreamProviderUnavailable)
}

func providerGeneratorRequest() GenerateRequest {
	return GenerateRequest{
		Paths: []DreamPath{{
			PathRef: "path_1",
			Subject: DreamPathNode{Ref: "node_1", ID: "durable-subject", Display: "Dense-Mem", Kind: "project"},
			Middle:  DreamPathNode{Ref: "node_2", ID: "durable-middle", Display: "Runtime", Kind: "product"},
			Object:  DreamPathNode{Ref: "node_3", ID: "durable-object", Display: "PostgreSQL", Kind: "product"},
			Premises: []DreamPathPremise{
				{
					PremiseRef: "premise_1", RelationshipRef: "relationship_1",
					Input: repository.DreamInput{
						RelationshipID: "durable-relationship-1", Version: 3, Status: "active", PredicateKey: "uses",
						Evidence: []repository.DreamEvidence{{EvidenceRef: "evidence_1", Content: "exact support one", SourceGroupKey: "internal-source-group", Authority: "primary"}},
					},
				},
				{
					PremiseRef: "premise_2", RelationshipRef: "relationship_2",
					Input: repository.DreamInput{
						RelationshipID: "durable-relationship-2", Version: 4, Status: "pending_evidence", PredicateKey: "uses",
						Evidence: []repository.DreamEvidence{{EvidenceRef: "evidence_2", Content: "exact support two", SourceGroupKey: "internal-source-group", Authority: "secondary"}},
					},
				},
			},
			AllowedPredicates: []repository.DreamTargetPredicate{{
				PredicateRef: "predicate_1", PredicateKey: "uses", RelationshipKind: "state", CurrentCardinality: "many",
			}},
		}},
	}
}

type providerGeneratorStub struct {
	model    string
	response dreamgeneration.DreamGenerationResponse
	err      error
	requests []dreamgeneration.DreamGenerationRequest
}

func (s *providerGeneratorStub) GenerateDreams(_ context.Context, request dreamgeneration.DreamGenerationRequest) (dreamgeneration.DreamGenerationResponse, error) {
	s.requests = append(s.requests, request)
	return s.response, s.err
}

func (s *providerGeneratorStub) ModelName() string {
	return s.model
}
