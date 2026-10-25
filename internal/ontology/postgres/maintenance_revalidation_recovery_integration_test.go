//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceRevalidationRecovery(t *testing.T) {
	for _, change := range []string{"unchanged", "definition", "source", "override", "busy_catalog", "busy_source"} {
		t.Run(change, func(t *testing.T) {
			x := newFinalizationFixture(t)
			f, source := x.f, x.claim.Sources[0]
			topic := testTopic("postgresql")
			revalidationPublish(t, f, ontology.Change{Record: topic})
			revalidationDiscover(t, f, x.window)
			require.Greater(t, revalidationQueue(t, f, source).Revision, x.claim.Revisions[ontology.SourceKey(source)])
			x.stage(t)
			switch change {
			case "definition":
				topic.Definition.Description = "Changed required storage definition"
				revalidationPublish(t, f, ontology.Change{ExpectedVersion: 1, Record: topic})
			case "source":
				snapshots, err := f.store.ReadSources(context.Background(), f.team, []ontology.SourceHandle{source})
				require.NoError(t, err)
				owner := 0
				if snapshots[0].OwnerID == f.owners[1] {
					owner = 1
				}
				_, err = f.knowledge.RetractEvidence(f.actor(owner, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[owner], EvidenceIDs: []string{source.ID}, Reason: "withdraw before recovery", IdempotencyKey: uuid.NewString(), RequestHash: testHash("withdraw before recovery")})
				require.NoError(t, err)
			case "override":
				other := testTopic("manager-recovery-replacement")
				override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: other.ID, Members: []ontology.SourceHandle{source}}, Sources: []ontology.SourceDependency{f.source(t, source)}}
				revalidationPublish(t, f, ontology.Change{Record: other}, ontology.Change{Record: override})
			}
			if change == "definition" || change == "source" || change == "override" {
				revalidationRefresh(t, f, source)
			}
			before := revalidationQueue(t, f, source)
			canonical, receipts, calls := f.canonicalSnapshot(t), revalidationReceiptHistory(t, f), x.calls.Load()
			if change == "busy_catalog" || change == "busy_source" {
				require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
					if change == "busy_catalog" {
						if err := lockOrganizationCatalog(tx, scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation}); err != nil {
							return err
						}
					} else if err := tx.Exec(`SELECT 1 FROM ontology_maintenance_sources WHERE team_id=?::uuid AND source_kind=? AND source_id=? FOR UPDATE`, f.team, source.Kind, source.ID).Error; err != nil {
						return err
					}
					started := time.Now()
					x.recover(t)
					require.Less(t, time.Since(started), time.Second, "recovery must skip busy inputs")
					require.Equal(t, "running", x.batchStatus(t))
					for _, handle := range x.claim.Sources {
						require.Equal(t, "pending", revalidationQueue(t, f, handle).Status, "busy recovery must not partially complete")
					}
					return nil
				}))
			}
			x.recover(t)
			require.Equal(t, "completed", x.batchStatus(t))
			after := revalidationQueue(t, f, source)
			require.Equal(t, before.Revision, after.Revision)
			if change == "unchanged" || change == "busy_catalog" || change == "busy_source" {
				require.Equal(t, "organized", after.Status)
				require.Equal(t, x.result.AssessmentID, after.AssessmentID)
			} else {
				require.Equal(t, before, after, "recovery must preserve changed inputs")
			}
			x.recover(t)
			require.Equal(t, 1, maintenanceRunByID(t, f, x.claim.RunID).CompletedBatches)
			require.Equal(t, calls, x.calls.Load())
			require.Equal(t, receipts, revalidationReceiptHistory(t, f))
			require.Equal(t, canonical, f.canonicalSnapshot(t))
		})
	}
}
