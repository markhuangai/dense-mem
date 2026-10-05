package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

func newOrganizationFixture(t *testing.T) *ontologyFixture {
	t.Helper()
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	t.Setenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS", "1")
	return newOntologyFixture(t)
}

func (f *ontologyFixture) organizationEvidence(t *testing.T, owner int, text string, metadata map[string]any) ontology.SourceHandle {
	t.Helper()
	result, err := f.knowledge.CreateIngestForTest(context.Background(), knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: f.owners[owner], IdempotencyKey: uuid.NewString(), RequestHash: testHash(text + uuid.NewString()), Evidence: []knowledge.EvidenceInput{{FragmentID: uuid.NewString(), Content: text, ForceInsert: true, Metadata: metadata}}})
	require.NoError(t, err)
	return ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: result.Evidence[0].FragmentID, Version: 1}
}

func organizationFixtureResponse(request assessment.Request, equivalent func(assessment.Item, assessment.Item) bool) assessment.Response {
	response := assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{}, Items: []assessment.Decision{}, Equivalence: []assessment.Equivalence{}}
	definitions := map[string]string{}
	items := map[string]assessment.Item{}
	for _, item := range request.Items {
		items[item.Ref] = item
		ref := item.LockedDefinitionRef
		if ref == "" {
			for _, d := range request.Definitions {
				if (item.Kind == ontology.EvidenceSource || item.Kind == ontology.RelationshipSource) && d.Kind == ontology.Topic {
					ref = d.Ref
					break
				}
			}
		}
		if ref == "" {
			kind, key, base := ontology.Topic, "postgresql", ""
			if item.Kind == ontology.EntitySource {
				kind = ontology.EntityClass
				key = item.EntityKind
				base = item.EntityKind
			}
			if item.Kind == ontology.PredicateSource {
				kind = ontology.PredicateConcept
				key = "registered-predicate"
			}
			if definitions[key] == "" {
				ref = "new-" + key
				definitions[key] = ref
				response.Definitions = append(response.Definitions, assessment.Definition{Ref: ref, Kind: kind, Key: key, Label: key, Description: "Synthetic organization vocabulary", Aliases: []string{}, BaseEntityKind: base})
			} else {
				ref = definitions[key]
			}
		}
		response.Items = append(response.Items, assessment.Decision{Ref: item.Ref, Status: "classified", DefinitionRef: ref})
	}
	for _, pair := range request.Pairs {
		relation := pair.RequiredRelation
		if relation == "" {
			relation = "distinct"
			if equivalent != nil && equivalent(items[pair.LeftRef], items[pair.RightRef]) {
				relation = "equivalent"
			}
		}
		response.Equivalence = append(response.Equivalence, assessment.Equivalence{Ref: pair.Ref, Relation: relation})
	}
	return response
}

func organizationFixtureService(t *testing.T, f *ontologyFixture, equivalent func(assessment.Item, assessment.Item) bool, edit func(assessment.Request, *assessment.Response)) (*organization.Service, *atomic.Int32) {
	return organizationFixtureServiceWithIdentity(t, f, equivalent, edit, "fixture-model", assessor.DefaultSemanticAssessmentLimits())
}

func organizationFixtureServiceWithIdentity(t *testing.T, f *ontologyFixture, equivalent func(assessment.Item, assessment.Item) bool, edit func(assessment.Request, *assessment.Response), model string, limits assessor.SemanticAssessmentLimits) (*organization.Service, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var envelope struct {
			Messages       []struct{ Role, Content string }
			ResponseFormat struct {
				JSONSchema struct {
					Name string `json:"name"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var request assessment.Request
		if len(envelope.Messages) < 2 {
			t.Error("missing immutable request")
			w.WriteHeader(400)
			return
		}
		if err := json.Unmarshal([]byte(envelope.Messages[1].Content), &request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if envelope.ResponseFormat.JSONSchema.Name != assessment.SchemaName {
			t.Error("wrong response schema")
		}
		response := organizationFixtureResponse(request, equivalent)
		if edit != nil {
			edit(request, &response)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(encoded)}}}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{AIVerifierAPIURL: server.URL, AIVerifierAPIKey: "synthetic-provider-key", AIVerifierModel: model}
	transport := assessorprovider.NewOpenAIAssessorWithAssessmentLimits(cfg, server.Client(), limits)
	provider := assessment.NewProvider(transport, cfg.AIVerifierModel, limits)
	return organization.NewService(f.store, provider), calls
}

func receiptCount(t *testing.T, f *ontologyFixture) int {
	t.Helper()
	var count int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_assessments WHERE team_id=?::uuid`, f.team).Row().Scan(&count))
	return count
}
