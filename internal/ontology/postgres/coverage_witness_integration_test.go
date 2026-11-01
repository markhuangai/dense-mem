//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestOntologyCoverageFailureWitnesses(t *testing.T) {
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
