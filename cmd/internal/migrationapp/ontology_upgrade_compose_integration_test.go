//go:build integration

package migrationapp

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepg "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	ontologypostgres "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestOntologyUpgradeComposeSeed(t *testing.T) {
	variant := os.Getenv("DENSE_MEM_E2E_ONTOLOGY_UPGRADE_VARIANT")
	if variant == "" {
		t.Skip("ontology upgrade seed requires its dedicated production-image scenario")
	}
	require.Contains(t, []string{"expired", "live", "unreconciled"}, variant)
	dsn := os.Getenv("DATABASE_URL")
	require.NotEmpty(t, dsn)
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	batch := seedOntologyUpgrade(t, db)
	definition := ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "upgrade-matching", Label: "Upgrade matching", Description: "of and the"}}
	var team string
	require.NoError(t, storage.NewRLS().WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT team_id::text FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, batch).Row().Scan(&team)
	}))
	_, err = ontologypostgres.NewStore(db, storage.NewRLS()).PublishAutomatic(context.Background(), team,
		ontology.Publication{OperationKey: "upgrade-matching-definition", Reason: "Preserved matching vocabulary", Changes: []ontology.Change{{Record: definition}}})
	require.NoError(t, err)
	seedRefinementUpgrade(t, db, team)
	require.NoError(t, storage.NewRLS().WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM ontology_maintenance_markers WHERE team_id=?::uuid AND anchor_kind='ontology_record_heads' AND anchor_id=?`, team, definition.ID).Error; err != nil {
			return err
		}
		switch variant {
		case "live":
			return tx.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()+interval '30 seconds' WHERE batch_id=?::uuid`, batch).Error
		case "unreconciled":
			return tx.Exec(`INSERT INTO ontology_maintenance_attempts(batch_id,assessment_id,attempt,window_id,reserved_input,reserved_output,admitted_at)
			 SELECT batch_id,?::uuid,1,window_id,20,30,clock_timestamp() FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, uuid.NewString(), batch).Error
		default:
			return nil
		}
	}))
}

func seedRefinementUpgrade(t *testing.T, db *gorm.DB, team string) {
	t.Helper()
	ctx, rls := context.Background(), storage.NewRLS()
	var owner, space string
	var generation int64
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT id::text FROM credentials WHERE team_id=?::uuid LIMIT 1`, team).Row().Scan(&owner); err != nil {
			return err
		}
		return tx.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, team).Row().Scan(&space, &generation)
	}))
	canonical := knowledgepg.NewStore(db, rls, knowledge.ConflictRuntimeConfig{})
	subject, err := canonical.CreateEntity(ctx, knowledge.CreateEntityInput{TeamID: team, OwnerProfileID: owner, EntityKind: "project", CanonicalName: "Upgrade software project"})
	require.NoError(t, err)
	object, err := canonical.CreateEntity(ctx, knowledge.CreateEntityInput{TeamID: team, OwnerProfileID: owner, EntityKind: "product", CanonicalName: "PostgreSQL"})
	require.NoError(t, err)
	predicate, err := canonical.EnsureSemanticReviewPredicateCandidate(ctx, knowledge.EnsureSemanticPredicateCandidateInput{TeamID: team, OwnerProfileID: owner, Predicate: "uses", RelationshipKind: "state", SubjectKind: "project", ObjectKind: "product"})
	require.NoError(t, err)
	evidence, err := canonical.CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: team, OwnerProfileID: owner, Evidence: []knowledge.EvidenceInput{{Content: "Upgrade software project uses PostgreSQL."}}})
	require.NoError(t, err)
	_, err = canonical.ApplyRelationshipDecision(ctx, knowledge.ApplyRelationshipDecisionInput{TeamID: team, OwnerProfileID: owner, IngestID: evidence.IngestID, SubjectEntityID: subject.EntityID, PredicateKey: predicate.PredicateKey, PredicateVersion: predicate.Version, ObjectEntityID: object.EntityID, Polarity: "+", Support: &knowledge.EvidenceSupportInput{FragmentID: evidence.Evidence[0].FragmentID, SourceGroupKey: uuid.NewString(), SpanEnd: len("Upgrade software project uses PostgreSQL."), Authority: "primary"}})
	require.NoError(t, err)
	store := ontologypostgres.NewStore(db, rls)
	var changes []ontology.Change
	handles := []ontology.SourceHandle{{Kind: ontology.EntitySource, ID: subject.EntityID, Version: int64(subject.Version)}, {Kind: ontology.PredicateSource, ID: predicate.PredicateKey, Version: int64(predicate.Version)}}
	snapshots, err := store.ReadSources(ctx, team, handles)
	require.NoError(t, err)
	for index, handle := range handles {
		kind, key, base := ontology.EntityClass, "project", "project"
		if handle.Kind == ontology.PredicateSource {
			kind, key, base = ontology.PredicateConcept, predicate.PredicateKey, ""
		}
		definition := ontology.Record{ID: uuid.NewString(), Kind: kind, Definition: &ontology.Definition{Key: key, Label: key, BaseEntityKind: base}}
		fingerprint, err := ontology.SourceFingerprint(snapshots[index])
		require.NoError(t, err)
		assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: handle, DefinitionID: definition.ID}, Sources: []ontology.SourceDependency{{SourceHandle: handle, Fingerprint: fingerprint}}}
		changes = append(changes, ontology.Change{Record: definition}, ontology.Change{Record: assignment})
		require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
			return tx.Exec(`INSERT INTO ontology_maintenance_sources(team_id,shared_space_id,space_generation,source_kind,source_id,source_version,eligible,status,revision,reason)
			 VALUES(?::uuid,?::uuid,?,?,?,?,true,'organized',5,'upgrade_refinement_fixture')`, team, space, generation, handle.Kind, handle.ID, handle.Version).Error
		}))
	}
	_, err = store.PublishAutomatic(ctx, team, ontology.Publication{OperationKey: "upgrade-refinement-assignments", ExpectedRevision: 1, Reason: "Preserve automatic assignments across the v2 queue refresh", Changes: changes})
	require.NoError(t, err)
}
