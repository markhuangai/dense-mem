package dreamservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
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

func TestProviderEvidenceDiscoveryMapsConcreteNodesAndExactSpans(t *testing.T) {
	targetID := "11111111-1111-4111-8111-111111111111"
	request, mappings, err := providerEvidenceDiscoveryRequest(EvidenceGenerationRequest{
		Target: repository.EvidenceTarget{
			EvidenceID: targetID, FragmentID: targetID, Content: "  Alice works.  ",
			Authority: "primary", SourceGroupKey: "ingest:test",
		},
		Nodes: []repository.EvidenceNode{
			{ID: "22222222-2222-4222-8222-222222222222", Display: "Alice", Kind: "person"},
			{ID: "33333333-3333-4333-8333-333333333333", Display: "Project", Kind: "project"},
		},
		AllowedPredicates: []repository.DreamTargetPredicate{{
			PredicateKey: "works_on", Version: 1,
			AllowedSubjectKinds: []string{"person"}, AllowedObjectKinds: []string{"project"},
		}},
	})
	require.NoError(t, err)
	prepared, validationErrs := dreamgeneration.PrepareEvidenceDiscoveryRequest(request, assessor.DefaultSemanticAssessmentLimits())
	require.Empty(t, validationErrs)
	startRef := providerEvidenceBoundaryRef(prepared.Contexts[0], 2)
	endRef := providerEvidenceBoundaryRef(prepared.Contexts[0], 7)
	response, responseErrs := dreamgeneration.PrepareEvidenceDiscoveryResponse(prepared, dreamgeneration.EvidenceDiscoveryResponse{
		RequestID: prepared.RequestID,
		Proposals: []dreamgeneration.EvidenceDiscoveryProposal{{
			SubjectRef: "node_1", PredicateRef: "predicate_1", ObjectRef: "node_2",
			Statement: "Alice may work on Project.", Rationale: "The target names the assignment.",
			WhatIf: "What if the assignment changes?", PossibleOutcome: "Review the assignment.",
			Likelihood: 0.5, Confidence: 0.5,
			Derivations: []dreamgeneration.EvidenceDiscoveryDerivation{{
				EvidenceRef: prepared.TargetRef, StartRef: startRef, EndRef: endRef,
			}},
		}},
	})
	require.Empty(t, responseErrs)
	dream, ok := mapEvidenceDiscoveryProposal(response.Proposals[0], mappings)
	require.True(t, ok)
	require.Equal(t, "Alice", dream.EvidenceDerivations[0].Quote)
	require.Equal(t, 2, dream.EvidenceDerivations[0].SpanStart)
	require.Equal(t, 7, dream.EvidenceDerivations[0].SpanEnd)
	require.Equal(t, "22222222-2222-4222-8222-222222222222", dream.SubjectEntityID)
	require.Equal(t, "33333333-3333-4333-8333-333333333333", dream.ObjectEntityID)
}

func TestEvidenceProviderGeneratorDispatchesAndMapsDiagnostics(t *testing.T) {
	provider := &evidenceProviderGeneratorStub{
		model: " evidence-model ",
		response: dreamgeneration.EvidenceDiscoveryResponse{
			ProviderTurns: 2, InputTokens: 11, OutputTokens: 7,
			Proposals: []dreamgeneration.EvidenceDiscoveryProposal{{
				SubjectRef: "node_1", PredicateRef: "predicate_1", ObjectRef: "node_2",
				Statement: "Alice may use Project.", Rationale: "The target names the connection.",
				WhatIf: "What if the connection changes?", PossibleOutcome: "Review the proposal.",
				Likelihood: 0.4, Confidence: 0.7,
				Derivations: []dreamgeneration.EvidenceDiscoveryDerivation{{EvidenceRef: "evidence_target", Start: 0, End: 5}},
			}},
		},
	}
	generator := &EvidenceProviderGenerator{provider: provider}
	targetID := "11111111-1111-4111-8111-111111111111"
	generated, diagnostics, err := generator.GenerateEvidence(context.Background(), "team", EvidenceGenerationRequest{
		Target:            repository.EvidenceTarget{EvidenceID: targetID, FragmentID: targetID, Content: "Alice uses Project.", Authority: "primary", SourceGroupKey: "ingest:test"},
		Contexts:          []repository.EvidenceContext{{EvidenceID: targetID, FragmentID: targetID, Content: "Alice uses Project.", Authority: "primary", SourceGroupKey: "ingest:test"}},
		Nodes:             []repository.EvidenceNode{{ID: "entity-a", Display: "Alice", Kind: "entity"}, {ID: "entity-b", Display: "Project", Kind: "entity"}},
		AllowedPredicates: []repository.DreamTargetPredicate{{PredicateKey: "uses", Version: 1, AllowedSubjectKinds: []string{"entity"}, AllowedObjectKinds: []string{"entity"}}},
		MaxOutputs:        2,
	})
	require.NoError(t, err)
	require.Len(t, generated, 1)
	require.Equal(t, "evidence-model", generator.Model())
	require.Equal(t, GenerationDiagnostics{ProviderTurns: 2, ProviderInputTokens: 11, ProviderOutputTokens: 7, ProviderProposals: 1}, diagnostics)
	require.Len(t, provider.requests, 1)
	require.Equal(t, "evidence_target", provider.requests[0].TargetRef)
	require.Equal(t, targetID, generated[0].EvidenceDerivations[0].EvidenceID)
}

func TestEvidenceProviderGeneratorRejectsUnavailableAndInvalidProviderResponses(t *testing.T) {
	var nilGenerator *EvidenceProviderGenerator
	require.Empty(t, nilGenerator.Model())
	_, _, err := nilGenerator.GenerateEvidence(context.Background(), "team", EvidenceGenerationRequest{})
	require.ErrorIs(t, err, ErrDreamProviderUnavailable)

	provider := &evidenceProviderGeneratorStub{model: "provider", err: errors.New("transport failed")}
	generator := &EvidenceProviderGenerator{provider: provider}
	_, _, err = generator.GenerateEvidence(context.Background(), "team", EvidenceGenerationRequest{Target: repository.EvidenceTarget{EvidenceID: "target"}})
	require.ErrorContains(t, err, "transport failed")

	_, _, err = generator.GenerateEvidence(context.Background(), "team", EvidenceGenerationRequest{Target: repository.EvidenceTarget{EvidenceID: "target"}, Contexts: []repository.EvidenceContext{{EvidenceID: "target", Content: "content"}}})
	require.Error(t, err)

	provider.err = nil
	provider.response = dreamgeneration.EvidenceDiscoveryResponse{Proposals: []dreamgeneration.EvidenceDiscoveryProposal{{SubjectRef: "unknown", PredicateRef: "unknown", ObjectRef: "unknown"}}}
	generated, _, err := generator.GenerateEvidence(context.Background(), "team", EvidenceGenerationRequest{
		Target:            repository.EvidenceTarget{EvidenceID: "target", FragmentID: "target", Content: "content"},
		Contexts:          []repository.EvidenceContext{{EvidenceID: "target", FragmentID: "target", Content: "content"}},
		Nodes:             []repository.EvidenceNode{{ID: "node", Kind: "entity"}},
		AllowedPredicates: []repository.DreamTargetPredicate{{PredicateKey: "uses", Version: 1}},
	})
	require.NoError(t, err)
	require.Empty(t, generated)
	constructed := NewEvidenceProviderGenerator(nil, "constructed-model", assessor.DefaultSemanticAssessmentLimits())
	require.Equal(t, "constructed-model", constructed.Model())
	_, _, err = constructed.GenerateEvidence(context.Background(), "team", EvidenceGenerationRequest{
		Target:            repository.EvidenceTarget{EvidenceID: "target", FragmentID: "target", Content: "content"},
		Contexts:          []repository.EvidenceContext{{EvidenceID: "target", FragmentID: "target", Content: "content"}},
		AllowedPredicates: []repository.DreamTargetPredicate{{PredicateKey: "uses", Version: 1}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "transport is unavailable")
}

func TestEvidenceProviderMappingsCoverRecordAndReferenceVariants(t *testing.T) {
	relationships := []repository.DreamInput{
		{SubjectEntityID: "subject", SubjectName: "Subject", SubjectKind: "person", PredicateKey: "uses", ObjectEntityID: "object", ObjectName: "Object", ObjectKind: "project"},
		{SubjectEntityID: "subject", SubjectKind: "person", PredicateKey: "uses", ObjectValueID: "value", ObjectName: "Value"},
		{SubjectEntityID: "missing", SubjectKind: "person", PredicateKey: "uses", ObjectEntityID: "object", ObjectKind: "project"},
	}
	hypotheses := []repository.HypothesisRecord{
		{SubjectEntityID: "subject", PredicateKey: "uses", ObjectEntityID: "object"},
		{SubjectEntityID: "subject", PredicateKey: "uses", ObjectValueID: "value"},
	}
	nodes := evidenceNodesFromRecords(relationships, hypotheses)
	require.Len(t, nodes, 6)
	refs := map[string]string{"subject\x00person": "node_subject", "object\x00project": "node_object", "value\x00value": "node_value", "subject\x00entity": "node_subject_entity", "object\x00entity": "node_object_entity"}
	require.Equal(t, "node_subject", providerNodeRef(refs, "subject", "person"))
	require.Equal(t, "node_subject", providerNodeRef(map[string]string{"subject\x00person": "node_subject"}, "subject", "entity"))
	require.Equal(t, "node_value", providerNodeRef(refs, "value", "value"))
	require.Empty(t, providerNodeRef(refs, "missing", "entity"))
	predicates := map[string]string{"uses": "predicate_uses"}
	relatedRelationships := providerRelatedRelationships(relationships, refs, predicates)
	require.Len(t, relatedRelationships, 2)
	relatedHypotheses := providerRelatedHypotheses(hypotheses, refs, predicates)
	require.Len(t, relatedHypotheses, 2)
	require.True(t, evidenceNodeIsValue("value"))
	require.True(t, evidenceNodeIsValue("string"))
	require.False(t, evidenceNodeIsValue("person"))
	require.Equal(t, "node_value_type", providerNodeRef(map[string]string{"value\x00string": "node_value_type"}, "value", "value"))

	request, _, err := providerEvidenceDiscoveryRequest(EvidenceGenerationRequest{
		Target:               repository.EvidenceTarget{EvidenceID: "target", FragmentID: "target", Content: "content"},
		Contexts:             []repository.EvidenceContext{{EvidenceID: "target", FragmentID: "target", Content: "content"}},
		RelatedRelationships: relationships[:1], RelatedHypotheses: hypotheses[:1],
		AllowedPredicates: []repository.DreamTargetPredicate{{PredicateKey: "uses", Version: 1}},
	})
	require.NoError(t, err)
	require.Len(t, request.Nodes, 4)
	mappings := evidenceProviderMappings{
		nodes:      map[string]repository.EvidenceNode{"subject": {ID: "subject", Kind: "entity"}, "value": {ID: "value", Kind: "value"}},
		predicates: map[string]repository.DreamTargetPredicate{"predicate": {PredicateKey: "uses", Version: 1}},
		contexts:   map[string]repository.EvidenceContext{"evidence": {EvidenceID: "evidence", FragmentID: "evidence", Content: "quoted"}},
	}
	valueDream, ok := mapEvidenceDiscoveryProposal(dreamgeneration.EvidenceDiscoveryProposal{
		SubjectRef: "subject", PredicateRef: "predicate", ObjectRef: "value", Statement: "statement",
		Derivations: []dreamgeneration.EvidenceDiscoveryDerivation{{EvidenceRef: "evidence", Start: 0, End: 6}},
	}, mappings)
	require.True(t, ok)
	require.Equal(t, "value", valueDream.ObjectValueID)
	_, ok = mapEvidenceDiscoveryProposal(dreamgeneration.EvidenceDiscoveryProposal{SubjectRef: "subject", PredicateRef: "predicate", ObjectRef: "value", Derivations: []dreamgeneration.EvidenceDiscoveryDerivation{{EvidenceRef: "evidence", Start: -1, End: 1}}}, mappings)
	require.False(t, ok)
	_, ok = mapEvidenceDiscoveryProposal(dreamgeneration.EvidenceDiscoveryProposal{SubjectRef: "subject", PredicateRef: "predicate", ObjectRef: "value"}, mappings)
	require.False(t, ok)
}

type evidenceProviderGeneratorStub struct {
	model    string
	response dreamgeneration.EvidenceDiscoveryResponse
	err      error
	requests []dreamgeneration.EvidenceDiscoveryRequest
}

func (s *evidenceProviderGeneratorStub) GenerateEvidenceDiscoveries(_ context.Context, request dreamgeneration.EvidenceDiscoveryRequest) (dreamgeneration.EvidenceDiscoveryResponse, error) {
	s.requests = append(s.requests, request)
	return s.response, s.err
}

func (s *evidenceProviderGeneratorStub) ModelName() string { return s.model }

func providerEvidenceBoundaryRef(context dreamgeneration.EvidenceDiscoveryContext, offset int) string {
	for ref, candidate := range context.BoundaryRefs {
		if candidate == offset {
			return ref
		}
	}
	return ""
}
