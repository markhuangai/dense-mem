//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMatchingHistoricalReceiptRefresh(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	source := f.organizationEvidence(t, 1, "Atlas of", nil)
	definition := testTopic("grammarreceipt")
	definition.Definition.Description = "of"
	revalidationPublish(t, f, ontology.Change{Record: definition})
	service, calls := organizationFixtureService(t, f, nil, ambiguousClassification)
	input := ontology.OrganizationInput{OperationKey: "filtered-context", Sources: []ontology.SourceHandle{source}}
	original, err := service.Organize(ctx, f.team, input)
	require.NoError(t, err)
	var legacy ontology.OrganizationReceipt
	require.NoError(t, f.store.withScope(ctx, f.team, false, func(tx *gorm.DB, fence scope) error {
		var err error
		legacy, _, err = organizationByKey(tx, fence, input.OperationKey)
		if err != nil {
			return err
		}
		contextData, err := f.store.organizationContext(tx, fence, input.Sources)
		if err != nil {
			return err
		}
		require.Empty(t, contextData.Candidates)
		contextData.Candidates, err = f.store.organizationVocabulary(tx, fence, contextData.Sources, []string{definition.ID})
		if err != nil {
			return err
		}
		require.Len(t, contextData.Candidates, 1)
		legacy.ID, legacy.OperationKey = uuid.NewString(), "unfiltered-historical-context"
		legacy.Result.AssessmentID = legacy.ID
		legacy.Dependencies, err = ontology.OrganizationDependencies(contextData)
		if err != nil {
			return err
		}
		legacy.ContextHash, err = ontology.OrganizationContextHash(contextData)
		if err != nil {
			return err
		}
		return insertOrganization(tx, fence, legacy)
	}))
	before, history := f.canonicalSnapshot(t), revalidationReceiptHistory(t, f)
	input.OperationKey = legacy.OperationKey
	replay, err := service.Organize(ctx, f.team, input)
	require.NoError(t, err)
	require.True(t, replay.Existing)
	require.False(t, replay.Current)
	require.Equal(t, legacy.ID, replay.AssessmentID)
	revalidationRefresh(t, f, source)
	require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_sources SET status='ambiguous',assessment_id=?::uuid WHERE team_id=?::uuid AND source_kind=? AND source_id=?`, legacy.ID, f.team, source.Kind, source.ID).Error)
	f.store = NewStore(f.app, f.rls)
	require.NoError(t, f.store.withScope(ctx, f.team, false, func(tx *gorm.DB, fence scope) error {
		marker := maintenanceMarker{AnchorKind: "ontology_record_heads", AnchorID: definition.ID, TargetKind: "definition", TargetID: definition.ID}
		selected, err := maintenanceMarkerSources(tx, fence, marker, ontology.MaintenancePageSize)
		require.NoError(t, err)
		require.Equal(t, []ontology.SourceHandle{source}, selected, "historical receipt dependencies still schedule refresh without lexical overlap")
		page, err := maintenanceDefinitionGroupPage(tx, fence, []maintenanceMarker{marker}, ontology.MaintenancePageSize)
		require.NoError(t, err)
		require.Equal(t, selected, page.Sources)
		return refreshMaintenanceInvalidation(tx, fence, source, true)
	}))
	require.Equal(t, "pending", revalidationQueue(t, f, source).Status)
	require.Equal(t, history, revalidationReceiptHistory(t, f))
	input.OperationKey = "reuse-filtered-context"
	current, err := service.Organize(ctx, f.team, input)
	require.NoError(t, err)
	require.True(t, current.Existing)
	require.True(t, current.Current)
	require.Equal(t, original.AssessmentID, current.AssessmentID)
	require.Equal(t, int32(1), calls.Load())
	require.NoError(t, f.store.withScope(ctx, f.team, true, func(tx *gorm.DB, fence scope) error {
		stored, found, err := organizationByKey(tx, fence, legacy.OperationKey)
		require.True(t, found)
		require.Equal(t, legacy, stored)
		return err
	}))
	require.Equal(t, before, f.canonicalSnapshot(t))
}
