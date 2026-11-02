//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestOntologyCoverageFailureWitnesses(t *testing.T) {
	t.Run("predicate_source_key", func(t *testing.T) {
		f := newOrganizationFixture(t)
		ctx := context.Background()
		subject, err := f.knowledge.CreateEntity(ctx, knowledge.CreateEntityInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityKind: "person", CanonicalName: "Ada"})
		require.NoError(t, err)
		object, err := f.knowledge.CreateEntity(ctx, knowledge.CreateEntityInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityKind: "project", CanonicalName: "Atlas"})
		require.NoError(t, err)
		predicate, err := f.knowledge.EnsureSemanticReviewPredicateCandidate(ctx, knowledge.EnsureSemanticPredicateCandidateInput{TeamID: f.team, OwnerProfileID: f.owners[0], Predicate: "works_on", RelationshipKind: "state", SubjectKind: "person", ObjectKind: "project"})
		require.NoError(t, err)
		ingest := f.evidence(t, 1, "Ada works on Atlas.")
		_, err = f.knowledge.ApplyRelationshipDecision(ctx, knowledge.ApplyRelationshipDecisionInput{
			TeamID: f.team, OwnerProfileID: f.owners[1], IngestID: ingest.IngestID,
			SubjectEntityID: subject.EntityID, PredicateKey: predicate.PredicateKey, PredicateVersion: predicate.Version,
			ObjectEntityID: object.EntityID, Polarity: "+",
			Support: &knowledge.EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "predicate-key-witness", SpanEnd: len("Ada works on Atlas."), Authority: "primary"},
		})
		require.NoError(t, err)
		source := ontology.SourceHandle{Kind: ontology.PredicateSource, ID: predicate.PredicateKey, Version: int64(predicate.Version)}
		beforeSource := f.source(t, source)
		before := f.canonicalSnapshot(t)
		service, calls := organizationFixtureService(t, f, nil, func(request assessment.Request, _ *assessment.Response) {
			require.Len(t, request.Items, 1)
			require.Equal(t, ontology.PredicateSource, request.Items[0].Kind)
			require.Equal(t, predicate.PredicateKey, request.Items[0].Context["predicate"])
		})
		result, err := service.Organize(ctx, f.team, ontology.OrganizationInput{OperationKey: "predicate-key", Sources: []ontology.SourceHandle{source}})
		require.NoError(t, err)
		require.True(t, result.Current)
		require.NotNil(t, result.Publication)
		require.Equal(t, int32(1), calls.Load())
		require.Equal(t, beforeSource, f.source(t, source))
		require.Equal(t, before, f.canonicalSnapshot(t))
		foreign := maintenanceOtherTeam(t, f)
		result, err = service.Organize(ctx, foreign.team, ontology.OrganizationInput{OperationKey: "foreign-predicate-key", Sources: []ontology.SourceHandle{source}})
		require.NoError(t, err)
		require.Contains(t, result.Outcomes, ontology.OrganizationOutcome{Source: source, Status: "unavailable", Reason: "source_unavailable"})
		require.Equal(t, int32(1), calls.Load())
	})
	f := newOrganizationFixture(t)
	a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	b := f.organizationEvidence(t, 1, "Atlas stores project records in PostgreSQL.", nil)
	seed, _ := organizationFixtureService(t, f, nil, nil)
	_, err := seed.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "coverage-seed", Sources: []ontology.SourceHandle{a}})
	require.NoError(t, err)
	t.Run("locked_diagnostic_still_assesses", func(t *testing.T) {
		before := f.canonicalSnapshot(t)
		catalog, err := f.store.ListRecords(context.Background(), f.team, "", "", ontology.MaxPageSize)
		require.NoError(t, err)
		sourceContext, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{a})
		require.NoError(t, err)
		service, calls := organizationFixtureService(t, f, nil, nil)
		request, response, attempts, err := service.AssessDiagnostic(context.Background(), sourceContext)
		require.NoError(t, err)
		require.Len(t, request.Items, 1)
		require.NotEmpty(t, request.Items[0].LockedDefinitionRef)
		require.Equal(t, int32(1), calls.Load())
		require.Len(t, attempts, 1)
		require.NoError(t, assessment.Validate(request, response))
		after, err := f.store.ListRecords(context.Background(), f.team, "", "", ontology.MaxPageSize)
		require.NoError(t, err)
		require.Equal(t, catalog, after)
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	for _, family := range []string{"request_id_mismatch", "item_locked_classification", "item_ambiguous_classification", "item_reason_limit", "valid_ambiguity"} {
		t.Run(family, func(t *testing.T) {
			before := f.canonicalSnapshot(t)
			catalog, err := f.store.ListRecords(context.Background(), f.team, "", "", ontology.MaxPageSize)
			require.NoError(t, err)
			service, calls := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
				for i, item := range request.Items {
					if item.LockedDefinitionRef == "" {
						if family == "valid_ambiguity" {
							response.Items[i] = assessment.Decision{Ref: item.Ref, Status: "ambiguous", Reason: "Insufficient topic context"}
						}
						continue
					}
					switch family {
					case "request_id_mismatch":
						response.RequestID = "different-request"
					case "item_locked_classification":
						response.Definitions = []assessment.Definition{{Ref: "different", Kind: ontology.Topic, Key: "different", Label: "Different", Aliases: []string{}}}
						response.Items[i].DefinitionRef = "different"
					case "item_ambiguous_classification":
						response.Items[i] = assessment.Decision{Ref: item.Ref, Status: "ambiguous", Reason: "Cannot choose"}
					case "item_reason_limit":
						response.Items[i].Reason = strings.Repeat("x", 129)
					}
				}
			})
			result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: family, Sources: []ontology.SourceHandle{a, b}})
			if family == "valid_ambiguity" {
				require.NoError(t, err)
				require.Equal(t, int32(1), calls.Load())
				require.Contains(t, result.Outcomes, ontology.OrganizationOutcome{Source: b, Status: "ambiguous", Reason: "classification_ambiguous"})
			} else {
				require.Error(t, err)
				require.Equal(t, "provider_response_invalid", result.FailureCode)
				require.Equal(t, int32(3), calls.Load())
				for _, attempt := range result.Attempts {
					require.Equal(t, family, attempt.Validation.Code)
				}
			}
			after, err := f.store.ListRecords(context.Background(), f.team, "", "", ontology.MaxPageSize)
			require.NoError(t, err)
			require.Equal(t, catalog, after)
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
}
