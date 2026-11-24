//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

func TestOntologyCoverageFailureWitnesses(t *testing.T) {

	f := newOrganizationFixture(t)
	a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	b := f.organizationEvidence(t, 1, "Atlas stores project records in PostgreSQL.", nil)
	seed, _ := organizationFixtureService(t, f, nil, nil)
	_, err := seed.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "coverage-seed", Sources: []ontology.SourceHandle{a}})
	require.NoError(t, err)
	t.Run("settled_diagnostic_uses_server_decisions", func(t *testing.T) {
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
		require.Zero(t, calls.Load())
		require.Equal(t, []ontology.AssessmentAttempt{}, attempts)
		require.NoError(t, assessment.Validate(request, response))
		after, err := f.store.ListRecords(context.Background(), f.team, "", "", ontology.MaxPageSize)
		require.NoError(t, err)
		require.Equal(t, catalog, after)
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	for _, family := range []string{"request_id_mismatch", "item_ref_unknown", "item_ambiguous_classification", "item_reason_limit", "item_source_kind", "definition_record", "valid_ambiguity"} {
		t.Run(family, func(t *testing.T) {
			before := f.canonicalSnapshot(t)
			catalog, err := f.store.ListRecords(context.Background(), f.team, "", "", ontology.MaxPageSize)
			require.NoError(t, err)
			service, calls := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
				require.Len(t, response.Items, 1)
				item := &response.Items[0]
				switch family {
				case "request_id_mismatch":
					response.RequestID = "different-request"
				case "item_ref_unknown":
					for _, contextItem := range request.Items {
						if contextItem.LockedDefinitionRef != "" {
							item.Ref = contextItem.Ref
						}
					}
				case "item_ambiguous_classification":
					item.Status, item.Reason = "ambiguous", "Cannot choose"
				case "item_reason_limit":
					item.Reason = strings.Repeat("x", 129)
				case "valid_ambiguity":
					item.Status, item.DefinitionRef, item.Reason = "ambiguous", "", "Insufficient topic context"
				case "definition_record":
					response.Definitions = append(response.Definitions, assessment.Definition{Ref: "empty-key", Kind: ontology.Topic, Label: "Storage", Aliases: []string{}})
					item.DefinitionRef = "empty-key"
				case "item_source_kind":
					response.Definitions = append(response.Definitions, assessment.Definition{Ref: "wrong-class", Kind: ontology.Kind("entity_class"), Key: "wrong-class", Label: "Wrong class", Aliases: []string{}})
					item.DefinitionRef = "wrong-class"
				}
				if family != "valid_ambiguity" {
					var rejected *assessor.SemanticAssessmentResponseValidationError
					require.ErrorAs(t, assessment.ValidateModelResponse(request, *response), &rejected)
					require.NotEmpty(t, rejected.Errors)
					if family == "item_source_kind" {
						require.Contains(t, rejected.Error(), "requires an Evidence topic")
						require.Contains(t, rejected.Error(), `received kind "entity_class"`)
					}
					if family == "item_ref_unknown" {
						require.Contains(t, rejected.Error(), "outside the requested item allowlist")
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
					expected := family
					if family == "item_source_kind" {
						expected = "definition_record"
					}
					require.Equal(t, expected, attempt.Validation.Code)
				}
			}
			after, err := f.store.ListRecords(context.Background(), f.team, "", "", ontology.MaxPageSize)
			require.NoError(t, err)
			require.Equal(t, catalog, after)
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
	t.Run("complete_locked_repair", func(t *testing.T) {
		before := f.canonicalSnapshot(t)
		catalog, err := f.store.ListRecords(context.Background(), f.team, "", "", ontology.MaxPageSize)
		require.NoError(t, err)
		turns := 0
		service, calls := organizationFixtureServiceWithIdentity(t, f, nil, func(request assessment.Request, response *assessment.Response) {
			turns++
			current, err := f.store.ListRecords(context.Background(), f.team, "", "", ontology.MaxPageSize)
			require.NoError(t, err)
			require.Equal(t, catalog, current, "invalid attempts must not partially publish")
			if turns < 3 {
				for _, item := range request.Items {
					if item.LockedDefinitionRef != "" {
						response.Items[0] = assessment.Decision{Ref: item.Ref, Status: "ambiguous", Reason: "uncertain"}
						var rejected *assessor.SemanticAssessmentResponseValidationError
						require.ErrorAs(t, assessment.ValidateModelResponse(request, *response), &rejected)
						require.Contains(t, rejected.Error(), item.Ref)
					}
				}
			}
		}, "repair-model", assessor.DefaultSemanticAssessmentLimits())
		result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "complete-locked-repair", Sources: []ontology.SourceHandle{a, b}})
		require.NoError(t, err)
		require.Equal(t, int32(3), calls.Load())
		require.True(t, result.Current)
		require.NotNil(t, result.Publication)
		require.Len(t, result.Attempts, 3)
		require.Equal(t, "item_ref_unknown", result.Attempts[0].Validation.Code)
		require.Equal(t, "item_ref_unknown", result.Attempts[1].Validation.Code)
		require.Nil(t, result.Attempts[2].Validation)
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("model cannot join memories the manager kept separate", func(t *testing.T) {
		f := newOrganizationFixture(t)
		left := f.organizationEvidence(t, 0, "Atlas stores task data in PostgreSQL.", nil)
		middle := f.organizationEvidence(t, 0, "Atlas persists data in PostgreSQL.", nil)
		right := f.organizationEvidence(t, 0, "Atlas stores account data in PostgreSQL.", nil)
		separation := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind,
			Override: &ontology.Override{Action: ontology.KeepSeparate, Members: []ontology.SourceHandle{left, right}},
			Sources:  []ontology.SourceDependency{f.source(t, left), f.source(t, right)}}
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("keep-separate", 0, ontology.Change{Record: separation}))
		require.NoError(t, err)
		before := f.canonicalSnapshot(t)
		service, calls := organizationFixtureService(t, f, func(assessment.Item, assessment.Item) bool { return true },
			func(request assessment.Request, response *assessment.Response) {
				require.Len(t, request.Pairs, 3)
				require.Len(t, response.Equivalence, 2)
			})
		result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{
			OperationKey: "bridge-kept-separate", Sources: []ontology.SourceHandle{left, middle, right},
		})
		require.Error(t, err)
		require.Equal(t, "provider_response_invalid", result.FailureCode)
		require.Len(t, result.Attempts, 3)
		for _, attempt := range result.Attempts {
			require.NotNil(t, attempt.Validation)
			require.Equal(t, "equivalence_transitivity", attempt.Validation.Code)
		}
		require.Equal(t, int32(3), calls.Load())
		require.Nil(t, result.Publication)
		page, err := f.store.ListRecords(context.Background(), f.team, ontology.EvidenceGroup, "", 20)
		require.NoError(t, err)
		require.Empty(t, page.Records)
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
}
