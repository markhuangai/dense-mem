//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMatchingConcurrentInvalidation(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	source := f.organizationEvidence(t, 1, "heliostat of", nil)
	definition := testTopic("concurrentcatalog")
	definition.Definition.Description = "of and the"
	revalidationPublish(t, f, ontology.Change{Record: definition})
	before := f.canonicalSnapshot(t)
	writerCtx, cancelWriter := context.WithTimeout(f.actor(0, "manager"), 15*time.Second)
	defer cancelWriter()
	done := make(chan error, 1)
	require.NoError(t, f.store.withScope(ctx, f.team, false, func(tx *gorm.DB, fence scope) error {
		if err := refreshMaintenanceSource(tx, fence, source, true); err != nil {
			return err
		}
		marker := maintenanceMarker{AnchorKind: "ontology_record_heads", AnchorID: definition.ID, TargetKind: "definition", TargetID: definition.ID}
		if err := tx.Raw(`SELECT marker_sequence,cursor FROM ontology_maintenance_markers
		 WHERE team_id=?::uuid AND anchor_kind=? AND anchor_id=?`, f.team, marker.AnchorKind, marker.AnchorID).Row().Scan(&marker.Sequence, &marker.Cursor); err != nil {
			return err
		}
		claimed, err := claimMaintenanceMarker(tx, fence, marker)
		require.NoError(t, err)
		require.True(t, claimed)
		page, err := maintenanceDefinitionGroupPage(tx, fence, []maintenanceMarker{marker}, ontology.MaintenancePageSize)
		require.NoError(t, err)
		require.Empty(t, page.Sources)
		require.True(t, page.Exhausted[0])
		definition.Definition.Description = "heliostat"
		go func() {
			_, err := f.store.PublishManager(writerCtx, f.team, testPublication("meaningful-concurrent-change", 1, ontology.Change{ExpectedVersion: 1, Record: definition}))
			done <- err
		}()
		require.False(t, discoveryWriterState(t, f, done), "publication must wait for the claimed marker")
		return deleteMaintenanceMarker(tx, fence, marker)
	}))
	require.NoError(t, <-done)
	require.NoError(t, f.store.withScope(ctx, f.team, true, func(tx *gorm.DB, fence scope) error {
		marker := maintenanceMarker{AnchorKind: "ontology_record_heads", AnchorID: definition.ID, TargetKind: "definition", TargetID: definition.ID}
		var count int
		require.NoError(t, tx.Raw(`SELECT count(*) FROM ontology_maintenance_markers WHERE team_id=?::uuid AND anchor_kind=? AND anchor_id=? AND cursor=''`, f.team, marker.AnchorKind, marker.AnchorID).Row().Scan(&count))
		require.Equal(t, 1, count, "the meaningful change must survive consumption of the old empty page")
		handles, err := maintenanceMarkerSources(tx, fence, marker, ontology.MaintenancePageSize)
		require.NoError(t, err)
		require.Equal(t, []ontology.SourceHandle{source}, handles)
		page, err := maintenanceDefinitionGroupPage(tx, fence, []maintenanceMarker{marker}, ontology.MaintenancePageSize)
		require.NoError(t, err)
		require.Equal(t, handles, page.Sources)
		return nil
	}))
	contextData, err := f.store.ReadOrganization(ctx, f.team, []ontology.SourceHandle{source})
	require.NoError(t, err)
	require.Len(t, contextData.Candidates, 1)
	require.Equal(t, definition.ID, contextData.Candidates[0].ID)
	require.Equal(t, before, f.canonicalSnapshot(t))
}
