//go:build integration

package migrationapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/contract"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRunUpAutomaticallyMigratesLegacyPrivateHistory(t *testing.T) {
	for _, name := range []string{"legacy", "startup_already_failed_at_cutover", "data_move_completed_before_interruption"} {
		t.Run(name, func(t *testing.T) {
			db, team, owners, ingests := legacyStartupDatabase(t)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			migrator, err := storage.NewMigrator(db)
			require.NoError(t, err)
			if name != "legacy" {
				require.ErrorContains(t, migrator.RunUp(ctx), "private memory migration requires")
			}
			if name == "data_move_completed_before_interruption" {
				repository := privacypostgres.NewPrivateMemoryRepository(db, storage.NewRLS())
				report, err := repository.InspectPrivateMemoryMigration(ctx)
				require.NoError(t, err)
				require.NoError(t, repository.MigratePrivateMemory(ctx, report.SourceFingerprint))
			}
			require.NoError(t, RunUp(ctx, db, 0, newRecordingLogger()))
			report, err := privacypostgres.NewPrivateMemoryRepository(db, storage.NewRLS()).InspectPrivateMemoryMigration(ctx)
			require.NoError(t, err)
			require.Equal(t, "complete", report.Status)
			require.True(t, strings.HasPrefix(report.SourceFingerprint, "sha256:"))
			var destinations []string
			require.NoError(t, storage.NewRLS().WithSystemTx(ctx, db, func(tx *gorm.DB) error {
				for index, ingest := range ingests {
					var space, owner, kind, hash string
					if err := tx.Raw(`SELECT space.id::text,membership.actor_identity_id::text,space.kind,intake.request_hash
 FROM knowledge_ingests intake JOIN memory_spaces space ON space.id=intake.space_id
 JOIN team_memberships membership ON membership.id=space.owner_membership_id
 WHERE intake.team_id=? AND intake.ingest_id=?`, team, ingest).Row().Scan(&space, &owner, &kind, &hash); err != nil {
						return err
					}
					require.Equal(t, owners[index].String(), owner)
					require.Equal(t, "private", kind)
					require.Equal(t, "original-"+ingest.String(), hash)
					destinations = append(destinations, space)
				}
				return nil
			}))
			require.NotEqual(t, destinations[0], destinations[1])
			assertStartupPrivateIsolation(t, db, team, owners, destinations)
			require.NoError(t, RunUp(ctx, db, 0, newRecordingLogger()))
			after, err := privacypostgres.NewPrivateMemoryRepository(db, storage.NewRLS()).InspectPrivateMemoryMigration(ctx)
			require.NoError(t, err)
			require.Equal(t, report.SourceFingerprint, after.SourceFingerprint)
		})
	}
}

func TestRunUpSerializesPrivateMigrationAndCancelsWaitingStarter(t *testing.T) {
	db, _, _, _ := legacyStartupDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	migrator, err := storage.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.PreparePrivateMemoryMigration(ctx))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	blocker, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.DiscardAdvisoryLockConnection(blocker)) })
	_, err = blocker.ExecContext(ctx, `SELECT pg_advisory_lock(573,2701)`)
	require.NoError(t, err)
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	canceled := make(chan error, 1)
	done := make(chan error, 2)
	go func() { canceled <- RunUp(waitCtx, db, 0, newRecordingLogger()) }()
	for range 2 {
		go func() { done <- RunUp(ctx, db, 0, newRecordingLogger()) }()
	}
	require.Eventually(t, func() bool {
		var waiting int
		err := db.WithContext(ctx).Raw(`SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND classid=573 AND objid=2701 AND NOT granted`).Row().Scan(&waiting)
		return err == nil && waiting == 3
	}, 30*time.Second, 10*time.Millisecond)
	cancelWait()
	select {
	case err := <-canceled:
		require.True(t, errors.Is(err, context.Canceled), "unexpected cancellation error: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("waiting starter did not honor cancellation")
	}
	_, err = blocker.ExecContext(ctx, `SELECT pg_advisory_unlock(573,2701)`)
	require.NoError(t, err)
	for range 2 {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("concurrent startup did not finish")
		}
	}
	report, err := privacypostgres.NewPrivateMemoryRepository(db, storage.NewRLS()).InspectPrivateMemoryMigration(ctx)
	require.NoError(t, err)
	require.Equal(t, "complete", report.Status)
}

func assertStartupPrivateIsolation(t *testing.T, db *gorm.DB, team uuid.UUID, owners []uuid.UUID, spaces []string) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE ROLE private_startup_reader NOSUPERUSER NOBYPASSRLS;
 GRANT USAGE ON SCHEMA public TO private_startup_reader;
 GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO private_startup_reader;
 GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO private_startup_reader;
 GRANT EXECUTE ON FUNCTION dense_mem_active_space_generation(UUID,UUID),dense_mem_lock_memory_space(UUID,UUID) TO private_startup_reader`).Error)
	var evidenceIDs []string
	require.NoError(t, storage.NewRLS().WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT fragment_id::text FROM evidence_fragments WHERE team_id=?`, team).Scan(&evidenceIDs).Error
	}))
	for index, owner := range owners {
		var allowed []domain.MemorySpaceAccess
		if index < len(spaces) {
			allowed = []domain.MemorySpaceAccess{{ID: uuid.MustParse(spaces[index]), Kind: domain.MemorySpacePrivate, Generation: 1}}
		}
		ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: team, IdentityID: owner, OwnerID: owner, AllowedSpaces: allowed})
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(`SET LOCAL ROLE private_startup_reader`).Error; err != nil {
				return err
			}
			var bypass bool
			if err := tx.Raw(`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Row().Scan(&bypass); err != nil {
				return err
			}
			require.False(t, bypass)
			store := knowledgepostgres.NewStore(tx, storage.NewRLS(), knowledge.ConflictRuntimeConfig{})
			for targetIndex, space := range spaces {
				visible, err := store.ListSubmissionAssessmentKnownEvidence(ctx, knowledge.SubmissionAssessmentKnownEvidenceInput{
					TeamID: team.String(), OwnerProfileID: owner.String(), SpaceID: space, EvidenceIDs: evidenceIDs,
				})
				if err != nil {
					return err
				}
				if targetIndex == index {
					require.Len(t, visible.Evidence, 1)
				} else {
					require.Empty(t, visible.Evidence)
				}
			}
			return nil
		}))
	}
}

func TestRunUpBlocksAmbiguousPrivateWriter(t *testing.T) {
	db, team, _, _ := legacyStartupDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	require.NoError(t, storage.NewRLS().WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO entity_records(team_id,entity_kind,space_id)
 SELECT ?,'project',id FROM memory_spaces WHERE team_id=? AND kind='profile_private'`, team, team).Error
	}))
	err := RunUp(ctx, db, 0, newRecordingLogger())
	require.ErrorIs(t, err, privacy.ErrPrivateMemoryMigrationBlocked)
	require.NoError(t, storage.NewRLS().WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		var phase string
		if err := tx.Raw(`SELECT phase FROM private_memory_migration_state WHERE singleton`).Row().Scan(&phase); err != nil {
			return err
		}
		require.Equal(t, "pending", phase)
		var count int
		if err := tx.Raw(`SELECT count(*) FROM private_memory_migration_records`).Row().Scan(&count); err != nil {
			return err
		}
		require.Zero(t, count)
		return nil
	}))
}

func legacyStartupDatabase(t *testing.T) (*gorm.DB, uuid.UUID, []uuid.UUID, []uuid.UUID) {
	t.Helper()
	db := openMigrationIntegrationDB(t)
	source := storage.MigrationsDir()
	root := t.TempDir()
	files, err := filepath.Glob(filepath.Join(source, "*", "*.sql"))
	require.NoError(t, err)
	for _, file := range files {
		if strings.SplitN(filepath.Base(file), "_", 2)[0] >= "20261014010001" {
			continue
		}
		relative, err := filepath.Rel(source, file)
		require.NoError(t, err)
		destination := filepath.Join(root, "migrations", "postgres", relative)
		require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0o755))
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(destination, body, 0o600))
	}
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Chdir(root)
	require.NoError(t, storage.RunUp(context.Background(), db))
	t.Chdir(cwd)
	team, owner := uuid.New(), uuid.New()
	owners := []uuid.UUID{uuid.New(), uuid.New()}
	ingests := []uuid.UUID{uuid.New(), uuid.New()}
	require.NoError(t, storage.NewRLS().WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO teams(id,name) VALUES(?,'automatic private migration')`, team).Error; err != nil {
			return err
		}
		for _, actor := range append([]uuid.UUID{owner}, owners...) {
			if err := tx.Exec(`INSERT INTO actor_identities(id,kind,team_id,active) VALUES(?,'api_client',?,true)`, actor, team).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO team_memberships(team_id,actor_identity_id,status,maximum_grants) VALUES(?,?,'active',ARRAY['read','write'])`, team, actor).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO ownership_aliases(team_id,legacy_owner_id,canonical_identity_id) VALUES(?,?,?)`, team, actor, actor).Error; err != nil {
				return err
			}
		}
		var space uuid.UUID
		if err := tx.Raw(`INSERT INTO memory_spaces(team_id,kind,owner_profile_id) VALUES(?,'profile_private',?) RETURNING id`, team, owner).Row().Scan(&space); err != nil {
			return err
		}
		for index, ingest := range ingests {
			if err := tx.Exec(`INSERT INTO knowledge_ingests(team_id,ingest_id,owner_profile_id,space_id,status,request_hash,source_summary)
 VALUES(?,?,?,?,'completed',?,'original private intake')`, team, ingest, owners[index], space, "original-"+ingest.String()).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO evidence_fragments(team_id,fragment_id,ingest_id,owner_profile_id,space_id,evidence_index,content,content_hash)
 VALUES(?,?,?,?,?,0,'original private evidence',?)`, team, uuid.New(), ingest, owners[index], space, "sha256:"+ingest.String()).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	return db, team, owners, ingests
}
