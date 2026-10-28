package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedMatchingCohortVocabulary(t *testing.T, f *ontologyFixture, testCase evalharness.OrganizationMatchingCase, handles []ontology.SourceHandle) map[string]any {
	t.Helper()
	ctx := context.Background()
	record := testTopic(testCase.DefinitionKey)
	record.Definition.Description = testCase.DefinitionDescription
	record.Definition.Aliases = testCase.DefinitionAliases
	page, err := f.store.ListRecords(ctx, f.team, "", "", 1)
	require.NoError(t, err)
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), page.Revision, ontology.Change{Record: record}))
	require.NoError(t, err)
	expected := 0
	if testCase.ExpectedCandidate {
		expected = 1
	}
	for _, handle := range handles {
		contextData, err := f.store.ReadOrganization(ctx, f.team, []ontology.SourceHandle{handle})
		require.NoError(t, err)
		require.Len(t, contextData.Candidates, expected)
		if expected > 0 {
			require.Equal(t, record.ID, contextData.Candidates[0].ID)
		}
	}
	matches := 0
	require.NoError(t, f.store.withScope(ctx, f.team, false, func(tx *gorm.DB, fence scope) error {
		for _, handle := range handles {
			if err := refreshMaintenanceSource(tx, fence, handle, true); err != nil {
				return err
			}
		}
		marker := maintenanceMarker{AnchorKind: "ontology_record_heads", AnchorID: record.ID, TargetKind: "definition", TargetID: record.ID}
		individual, err := maintenanceMarkerSources(tx, fence, marker, ontology.MaintenancePageSize)
		if err != nil {
			return err
		}
		grouped, err := maintenanceDefinitionGroupPage(tx, fence, []maintenanceMarker{marker}, ontology.MaintenancePageSize)
		if err != nil {
			return err
		}
		require.ElementsMatch(t, individual, grouped.Sources)
		matches = len(individual)
		require.Equal(t, expected*len(handles), matches)
		if expected > 0 {
			require.ElementsMatch(t, handles, individual)
		}
		return nil
	}))
	return map[string]any{"lookup_candidates_per_source": expected, "definition_marker_sources": matches}
}
