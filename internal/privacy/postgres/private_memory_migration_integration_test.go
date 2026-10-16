//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/contract"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	processor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/markhuangai/dense-mem/internal/session/extraction"
	sessionservice "github.com/markhuangai/dense-mem/internal/session/service"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"
)

type privateMigrationFixture struct {
	auditID                                                                                uuid.UUID
	dsn                                                                                    string
	db                                                                                     *gorm.DB
	rls                                                                                    *storage.RLS
	repo                                                                                   *PrivateMemoryRepositoryImpl
	team, user, ownerA, ownerB, memberA, memberB, legacy, evidence, independent, duplicate uuid.UUID
	relationship, ingestA, ingestB                                                         uuid.UUID
}

func legacyPrivateMigrationFixture(t *testing.T) *privateMigrationFixture {
	t.Helper()
	dsn, cleanup := setupLedgerRepositoryDSN(t)
	t.Cleanup(cleanup)
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
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
		require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0755))
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(destination, body, 0600))
	}
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Chdir(root)
	migrator, err := storage.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(context.Background()))
	t.Chdir(cwd)
	f := &privateMigrationFixture{db: db, dsn: dsn, rls: storage.NewRLS(), team: uuid.New(), user: uuid.New(), ownerA: uuid.New(), ownerB: uuid.New(), memberA: uuid.New(), memberB: uuid.New(), evidence: uuid.New(), independent: uuid.New(), duplicate: uuid.New(), relationship: uuid.New(), ingestA: uuid.New(), ingestB: uuid.New()}
	f.repo = NewPrivateMemoryRepository(db, f.rls)
	f.auditID = uuid.New()
	require.NoError(t, f.rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO teams(id,name) VALUES(?,?)`, f.team, "private-migration-"+f.team.String()).Error
	}))
	f.user = createLedgerSSOIdentity(t, db, f.rls, f.team)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT id FROM memory_spaces WHERE team_id=? AND kind='profile_private' AND owner_profile_id=?`, f.team, f.user).Row().Scan(&f.legacy); err != nil {
			return err
		}
		for _, actor := range []struct {
			id, member uuid.UUID
			kind       string
		}{{f.ownerA, f.memberA, "api_client"}, {f.ownerB, f.memberB, "api_client"}} {
			if err := tx.Exec(`INSERT INTO actor_identities(id,kind,team_id,active) VALUES(?,?,?,true)`, actor.id, actor.kind, f.team).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO team_memberships(id,team_id,actor_identity_id,status,maximum_grants,sso_entitlement_status) VALUES(?,?,?,'active',ARRAY['read','write'],'active')`, actor.member, f.team, actor.id).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO membership_grants(membership_id,grant_name,source) VALUES(?,'read','explicit'),(?,'write','explicit')`, actor.member, actor.member).Error; err != nil {
				return err
			}
			if actor.kind == "api_client" {
				if err := tx.Exec(`INSERT INTO credentials(id,team_id,actor_identity_id,owner_identity_id,key_hash,key_prefix,key_suffix,name,scopes,memory_binding,memory_space_id)
     VALUES(?,?,?,?,'synthetic-test-hash',?,'test01',?,ARRAY['read','write'],'profile_private',?)`, actor.id, f.team, actor.id, f.user, actor.id.String()[:24], "migration client "+actor.id.String(), f.legacy).Error; err != nil {
					return err
				}
			}
			if err := tx.Exec(`INSERT INTO ownership_aliases(team_id,legacy_owner_id,canonical_identity_id,credential_id) VALUES(?,?,?,?)`, f.team, actor.id, actor.id, actor.id).Error; err != nil {
				return err
			}
		}
		ingestA, ingestB := f.ingestA, f.ingestB
		for _, intake := range []struct{ owner, id uuid.UUID }{{f.ownerA, ingestA}, {f.ownerB, ingestB}} {
			if err := tx.Exec(`INSERT INTO knowledge_ingests(team_id,ingest_id,owner_profile_id,space_id,status,idempotency_key,request_hash,proposal)
     VALUES(?,?,?,?,'completed',?,?,'{"historical_targetless":true}')`, f.team, intake.id, intake.owner, f.legacy, "original:"+intake.id.String(), "unchanged-hash:"+intake.id.String()).Error; err != nil {
				return err
			}
		}
		for _, evidence := range []struct {
			id, ingest, owner uuid.UUID
			text              string
		}{{f.evidence, ingestA, f.ownerA, "A exact supporting evidence"}, {f.independent, ingestB, f.ownerB, "B independent evidence"}} {
			if err := tx.Exec(`INSERT INTO evidence_fragments(team_id,fragment_id,ingest_id,owner_profile_id,space_id,evidence_index,content,content_hash)
     VALUES(?,?,?,?,?,0,?,?)`, f.team, evidence.id, evidence.ingest, evidence.owner, f.legacy, evidence.text, "sha256:"+evidence.id.String()).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`INSERT INTO evidence_occurrences(team_id,occurrence_id,canonical_fragment_id,canonical_owner_profile_id,ingest_id,owner_profile_id,space_id,space_generation,evidence_index,content,content_hash)
    VALUES(?,?,?,?,?,?,?,1,1,'A exact supporting evidence',?)`, f.team, f.duplicate, f.evidence, f.ownerA, ingestB, f.ownerB, f.legacy, "sha256:"+f.evidence.String()).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO remember_attempts(team_id,attempt_id,owner_profile_id,space_id,space_generation,idempotency_key,request_hash,contract_version,outcome,public_result)
 VALUES(?,?,?,?,1,?,'unchanged-targetless-hash','dense-mem.v2.7.0','completed','{"contract_version":"dense-mem.v2.7.0","processing_state":"completed","submission_kind":"remember"}')`, f.team, f.ingestA, f.ownerA, f.legacy, "legacy:"+f.ingestA.String()).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO remember_attempt_diagnostics(team_id,attempt_id,owner_profile_id,sequence_no,kind,request_bytes,captured_at,expires_at)
 VALUES(?,?,?,1,'original_request',convert_to('{"legacy_request":"A exact supporting evidence"}','UTF8'),now()-interval '7 days',now()-interval '1 second')`, f.team, f.ingestA, f.ownerA).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO audit_log(id,team_id,operation,entity_type,entity_id,actor_profile_id,before_payload,after_payload,metadata,memory_space_id)
 VALUES(?,?,'PRIVATE_WRITE','memory',?,?,'{"content":"A private audit before"}','{"content":"A private audit after"}','{"context":"B mixed private context"}',?)`, f.auditID, f.team, f.ingestA.String(), f.ownerA, f.legacy).Error
	}))
	return f
}

func (f *privateMigrationFixture) applicationDB(t *testing.T) *gorm.DB {
	t.Helper()
	require.NoError(t, f.db.Exec(fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOBYPASSRLS;
 GRANT USAGE ON SCHEMA public TO %s;
 GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO %s;
 GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO %s;
 GRANT EXECUTE ON FUNCTION dense_mem_active_space_generation(UUID,UUID),dense_mem_lock_memory_space(UUID,UUID) TO %s`, ledgerTestRole, ledgerTestPassword, ledgerTestRole, ledgerTestRole, ledgerTestRole, ledgerTestRole)).Error)
	app, err := gorm.Open(gormpostgres.Open(ledgerAppDSN(t, f.dsn)), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := app.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	var bypass bool
	require.NoError(t, app.Raw(`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Row().Scan(&bypass))
	require.False(t, bypass)
	return app
}

func (f *privateMigrationFixture) seedDependentRelationship(t *testing.T) {
	t.Helper()
	contract := insertSearchTestContract(t, f.db, f.rls, "migration-dependent", 3, "exact", "")
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO team_predicate_definitions(team_id,predicate_key,version,aliases,allowed_subject_kinds,allowed_object_kinds,relationship_kind,current_cardinality,lifecycle_state,origin,metadata,created_at)
 SELECT ?,predicate_key,version,aliases,allowed_subject_kinds,allowed_object_kinds,relationship_kind,current_cardinality,lifecycle_state,'built_in',metadata,created_at FROM predicate_definitions WHERE predicate_key='uses' AND version=1`, f.team).Error; err != nil {
			return err
		}
		subject, object, observation, verification, support := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
		for _, entity := range []uuid.UUID{subject, object} {
			if err := tx.Exec(`INSERT INTO entity_records(team_id,entity_id,entity_kind,space_id) VALUES(?,?,'project',?)`, f.team, entity, f.legacy).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO entity_resolution_events(team_id,ingest_id,owner_profile_id,mention_ref,action,entity_id,space_id) VALUES(?,?,?,'B endpoint','create',?,?)`, f.team, f.ingestB, f.ownerB, entity, f.legacy).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO entity_names(team_id,entity_id,owner_profile_id,display_name,normalized_name,name_kind,space_id) VALUES(?,?,?,'B endpoint','b endpoint','canonical',?)`, f.team, entity, f.ownerB, f.legacy).Error; err != nil {
				return err
			}
		}
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO relationship_records(team_id,relationship_id,owner_profile_id,semantic_group_key,subject_entity_id,predicate_key,predicate_version,object_entity_id,relationship_kind,current_cardinality,status,support_count,source_group_count,space_id)
 VALUES(?,?,?,'legacy-dependency',?,'uses',1,?,'state','many','active',1,1,?)`, []any{f.team, f.relationship, f.ownerB, subject, object, f.legacy}},
			{`INSERT INTO relationship_observations(team_id,observation_id,relationship_id,ingest_id,owner_profile_id,subject_ref,original_predicate,object_ref,subject_entity_id,predicate_key,predicate_version,object_entity_id,space_id)
 VALUES(?,?,?,?,?,'B endpoint','uses','B endpoint',?,'uses',1,?,?)`, []any{f.team, observation, f.relationship, f.ingestB, f.ownerB, subject, object, f.legacy}},
			{`INSERT INTO verification_events(team_id,verification_event_id,observation_id,owner_profile_id,evidence_verdict,space_id) VALUES(?,?,?,?,'entailed',?)`, []any{f.team, verification, observation, f.ownerB, f.legacy}},
			{`INSERT INTO relationship_evidence_supports(team_id,support_id,relationship_id,observation_id,verification_event_id,fragment_id,owner_profile_id,evidence_owner_profile_id,occurrence_id,occurrence_owner_profile_id,source_group_key,span_start,span_end,quote,space_id)
 VALUES(?,?,?,?,?,?,?,?,?,?,'original-source-A',0,27,'A exact supporting evidence',?)`, []any{f.team, support, f.relationship, observation, verification, f.evidence, f.ownerB, f.ownerA, f.duplicate, f.ownerB, f.legacy}},
			{`INSERT INTO relationship_support_decision_events(team_id,support_id,relationship_id,owner_profile_id,decision,reason,space_id) VALUES(?,?,?,?,'grant','original support',?)`, []any{f.team, support, f.relationship, f.ownerB, f.legacy}},
			{`INSERT INTO search_documents(team_id,owner_profile_id,source_kind,source_id,source_version,embedding_contract_id,embedding_dimensions,document_text,document_hash,space_id)
 VALUES(?,?,'relationship',?,1,?,3,'old mixed projection','original-hash',?)`, []any{f.team, f.ownerB, f.relationship, contract, f.legacy}},
		} {
			if err := tx.Exec(statement.query, statement.args...).Error; err != nil {
				return err
			}
		}
		return nil
	}))
}

func TestPrivateMigrationFingerprintResumeHandlesAndCopiedErasure(t *testing.T) {
	f := legacyPrivateMigrationFixture(t)
	f.seedDependentRelationship(t)
	ctx := context.Background()
	report, err := f.repo.InspectPrivateMemoryMigration(ctx)
	require.NoError(t, err)
	require.Positive(t, report.SupportCopyCount)
	require.Positive(t, report.HandleMappingCount)
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "exact supporting evidence")
	require.NotContains(t, string(raw), "private audit")
	var auditCovered bool
	for _, table := range report.Tables {
		if table.Name == "audit_log" {
			auditCovered = true
			require.EqualValues(t, 1, table.PrivateRows)
		}
	}
	require.True(t, auditCovered, "preflight must account for private audit history")
	var prepared bool
	require.NoError(t, f.db.Raw(`SELECT to_regclass('public.private_memory_migration_state') IS NOT NULL`).Row().Scan(&prepared))
	require.False(t, prepared)
	migrator, err := storage.NewMigrator(f.db)
	require.NoError(t, err)
	require.NoError(t, migrator.PreparePrivateMemoryMigration(ctx))
	after, err := f.repo.InspectPrivateMemoryMigration(ctx)
	require.NoError(t, err)
	require.Equal(t, report.SourceFingerprint, after.SourceFingerprint)
	require.Error(t, migrator.RunUp(ctx), "normal startup cannot bypass maintenance")
	require.ErrorIs(t, f.repo.MigratePrivateMemory(ctx, "sha256:"+strings.Repeat("0", 64)), privacy.ErrPrivateMemoryMigrationBlocked)
	var plan *privateMigrationPlan
	require.NoError(t, f.rls.WithSystemReadOnlyRepeatableTx(ctx, f.db, func(tx *gorm.DB) error { var err error; plan, err = planPrivateMemoryMigration(ctx, tx); return err }))
	require.NoError(t, f.repo.stagePrivateMigration(ctx, plan))
	count, err := f.repo.movePrivateMigrationBatch(ctx, true)
	require.NoError(t, err)
	require.Positive(t, count)
	require.Error(t, migrator.RunUp(ctx))
	require.NoError(t, NewPrivateMemoryRepository(f.db, f.rls).MigratePrivateMemory(ctx, report.SourceFingerprint))
	require.NoError(t, migrator.RunUp(ctx))
	require.NoError(t, f.repo.MigratePrivateMemory(ctx, report.SourceFingerprint))
	app := f.applicationDB(t)
	f.repo = NewPrivateMemoryRepository(app, f.rls)
	var spaceA, spaceB uuid.UUID
	var copyID string
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT id FROM memory_spaces WHERE owner_membership_id=? AND NOT migration_archive`, f.memberA).Row().Scan(&spaceA); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT id FROM memory_spaces WHERE owner_membership_id=? AND NOT migration_archive`, f.memberB).Row().Scan(&spaceB); err != nil {
			return err
		}
		return tx.Raw(`SELECT canonical_fragment_id::text FROM evidence_occurrences WHERE team_id=? AND occurrence_id=?`, f.team, f.duplicate).Row().Scan(&copyID)
	}))
	require.NotEqual(t, f.evidence.String(), copyID)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		var archived bool
		var before, after string
		if err := tx.Raw(`SELECT space.migration_archive AND space.lifecycle_state='sealed',audit.before_payload::text,audit.after_payload::text
 FROM audit_log audit JOIN memory_spaces space ON space.id=audit.memory_space_id WHERE audit.id=?`, f.auditID).Row().Scan(&archived, &before, &after); err != nil {
			return err
		}
		require.True(t, archived)
		require.JSONEq(t, `{"content":"A private audit before"}`, before)
		require.JSONEq(t, `{"content":"A private audit after"}`, after)
		return nil
	}))
	keyA := f.ownerA
	aCtx := requestctx.WithActor(ctx, requestctx.Actor{TeamID: f.team, IdentityID: f.ownerA, MembershipID: f.memberA, OwnerID: f.ownerA, CredentialID: &keyA, Grants: []string{"read", "write"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: spaceA, Kind: domain.MemorySpacePrivate, Generation: 1}}})
	api := remember.NewService(remember.Dependencies{Synchronous: processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: knowledgepostgres.NewStore(app, f.rls, knowledgepostgres.ConflictRuntimeConfig{})})})
	_, err = api.Remember(aCtx, remember.RememberRequest{IdempotencyKey: "legacy:" + f.ingestA.String()})
	require.ErrorIs(t, err, domain.ErrMemoryTargetRequired, "same-version historical receipts cannot bypass required target validation")
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		var hash, receipt, request string
		if err := tx.Raw(`SELECT request_hash,public_result::text FROM remember_attempts WHERE team_id=? AND attempt_id=?`, f.team, f.ingestA).Row().Scan(&hash, &receipt); err != nil {
			return err
		}
		require.Equal(t, "unchanged-targetless-hash", hash)
		require.JSONEq(t, `{"contract_version":"dense-mem.v2.7.0","processing_state":"completed","submission_kind":"remember"}`, receipt)
		if err := tx.Raw(`SELECT convert_from(request_bytes,'UTF8') FROM remember_attempt_diagnostics WHERE team_id=? AND attempt_id=?`, f.team, f.ingestA).Row().Scan(&request); err != nil {
			return err
		}
		require.Equal(t, `{"legacy_request":"A exact supporting evidence"}`, request)
		return nil
	}))
	actor := requestctx.Actor{TeamID: f.team, IdentityID: f.ownerB, MembershipID: f.memberB, OwnerID: f.ownerB, Grants: []string{"read"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: spaceB, Kind: domain.MemorySpacePrivate, Generation: 1}}}
	ownerCtx := requestctx.WithActor(ctx, actor)
	require.NoError(t, f.rls.WithTeamProfileTx(ownerCtx, app, f.team.String(), f.ownerB.String(), func(tx *gorm.DB) error {
		resolved, err := storage.ResolveMemoryHandleTx(ownerCtx, tx, f.team.String(), "evidence_fragments", f.evidence.String(), spaceB.String())
		require.NoError(t, err)
		require.Equal(t, copyID, resolved)
		foreign, err := storage.ResolveMemoryHandleTx(ownerCtx, tx, f.team.String(), "evidence_fragments", f.evidence.String(), spaceA.String())
		require.NoError(t, err)
		require.Equal(t, f.evidence.String(), foreign, "the destination argument cannot grant access to A")
		var count int
		if err := tx.Raw(`SELECT count(*) FROM evidence_fragments WHERE team_id=? AND fragment_id=? AND dense_mem_space_allowed(space_id) AND space_generation=dense_mem_active_space_generation(team_id,space_id)`, f.team, f.evidence).Row().Scan(&count); err != nil {
			return err
		}
		require.Zero(t, count, "the adapter space predicate and team RLS must hide A's original record")
		if err := tx.Raw(`SELECT count(*) FROM evidence_fragments WHERE team_id=? AND fragment_id=?`, f.team, copyID).Row().Scan(&count); err != nil {
			return err
		}
		require.Equal(t, 1, count, "only the necessary copied support is visible")
		if err := tx.Raw(`SELECT support_count FROM relationship_records WHERE team_id=? AND relationship_id=?`, f.team, f.relationship).Row().Scan(&count); err != nil {
			return err
		}
		require.Equal(t, 1, count, "copies must not inflate corroboration")
		return nil
	}))
	func() {
		locked := make(chan error, 1)
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- f.rls.WithSystemTx(ctx, app, func(tx *gorm.DB) error {
				var id uuid.UUID
				err := tx.Raw(`SELECT id FROM memory_spaces WHERE id=? FOR UPDATE`, spaceB).Row().Scan(&id)
				locked <- err
				if err == nil {
					<-release
				}
				return err
			})
		}()
		defer func() {
			close(release)
			require.NoError(t, <-done)
		}()
		require.NoError(t, <-locked)
		purgeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		deleted, err := knowledgepostgres.NewStore(app, f.rls, knowledgepostgres.ConflictRuntimeConfig{}).PurgeExpiredRememberAttemptDiagnostics(purgeCtx, 100)
		require.NoError(t, err)
		require.Zero(t, deleted, "purge must skip mixed source diagnostics while any copied lineage space is locked")
	}()
	_, _, err = f.repo.PlaceLegalHold(ctx, spaceB, "copied-support-hold")
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(ctx, app, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET private_content_at=now()-interval '2 days' WHERE id IN (?,?)`, spaceA, spaceB).Error
	}))
	retention, created, err := f.repo.RunRetention(ctx, PrivateMemoryRetentionRequest{
		ActorClass: domain.PrivateMemoryActorControl, RetentionDays: 1,
		IdempotencyScopeHash: strings.Repeat("5", 64), RequestHash: strings.Repeat("6", 64),
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Zero(t, retention.QueuedCount, "a hold on copied support excludes every affected space from retention")
	deleted, err := knowledgepostgres.NewStore(app, f.rls, knowledgepostgres.ConflictRuntimeConfig{}).PurgeExpiredRememberAttemptDiagnostics(ctx, 100)
	require.NoError(t, err)
	require.Zero(t, deleted, "a hold on B also protects retained mixed source diagnostics")
	_, _, err = f.repo.RequestControlErasure(ctx, spaceA, strings.Repeat("1", 64), strings.Repeat("2", 64), "owner-erasure")
	require.ErrorIs(t, err, ErrPrivateMemoryLegalHold)
	_, _, err = f.repo.ReleaseLegalHold(ctx, spaceB)
	require.NoError(t, err)
	_, _, err = f.repo.RequestControlErasure(ctx, spaceA, strings.Repeat("1", 64), strings.Repeat("2", 64), "owner-erasure")
	require.NoError(t, err)
	require.NoError(t, f.repo.Prepare(ctx))
	_, _, err = f.repo.PlaceLegalHold(ctx, spaceB, "post-queue-copy-hold")
	require.NoError(t, err)
	claim, err := f.repo.ClaimNext(ctx, "held-migration-test", 0)
	require.NoError(t, err)
	require.Nil(t, claim, "a copied-record hold must leave the erasure queued without consuming attempts")
	_, _, err = f.repo.ReleaseLegalHold(ctx, spaceB)
	require.NoError(t, err)
	claim, err = f.repo.ClaimNext(ctx, "migration-test", 0)
	require.NoError(t, err)
	require.NotNil(t, claim)
	err = f.rls.WithSystemTx(ctx, app, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE id=?`, spaceB).Error; err != nil {
			return err
		}
		if err := tx.Exec(`SELECT set_config('app.private_erasure_rebase_space_id',?,true),set_config('app.private_erasure_operation_id',?,true)`, spaceB.String(), claim.ID.String()).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE search_documents SET space_generation=space_generation+1,document_text='changed during rebase' WHERE team_id=? AND source_id=?`, f.team, f.relationship).Error
	})
	require.ErrorContains(t, err, "private erasure rebase can change only the placement generation")
	_, err = f.repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		var count int
		if err := tx.Raw(`SELECT count(*) FROM evidence_fragments WHERE team_id=? AND fragment_id IN (?::uuid,?::uuid)`, f.team, f.evidence, copyID).Row().Scan(&count); err != nil {
			return err
		}
		require.Zero(t, count)
		if err := tx.Raw(`SELECT count(*) FROM evidence_fragments WHERE team_id=? AND fragment_id=?`, f.team, f.independent).Row().Scan(&count); err != nil {
			return err
		}
		require.Equal(t, 1, count)
		if err := tx.Raw(`SELECT count(*) FROM evidence_occurrences WHERE team_id=? AND occurrence_id=?`, f.team, f.duplicate).Row().Scan(&count); err != nil {
			return err
		}
		require.Zero(t, count)
		var status, searchState string
		if err := tx.Raw(`SELECT status,support_count FROM relationship_records WHERE team_id=? AND relationship_id=?`, f.team, f.relationship).Row().Scan(&status, &count); err != nil {
			return err
		}
		require.Equal(t, "pending_evidence", status)
		require.Zero(t, count)
		if err := tx.Raw(`SELECT search_state FROM search_documents WHERE team_id=? AND source_id=?`, f.team, f.relationship).Row().Scan(&searchState); err != nil {
			return err
		}
		require.Equal(t, "not_required", searchState)
		var validProjection bool
		if err := tx.Raw(`SELECT search_tsv=to_tsvector('simple',document_text) AND space_generation=2 FROM search_documents WHERE team_id=? AND source_id=?`, f.team, f.relationship).Row().Scan(&validProjection); err != nil {
			return err
		}
		require.True(t, validProjection, "generation rebasing preserves the generated search projection")
		var scrubbed bool
		if err := tx.Raw(`SELECT before_payload IS NULL AND after_payload IS NULL AND metadata='{"private_content_erased":true}'::jsonb FROM audit_log WHERE id=?`, f.auditID).Row().Scan(&scrubbed); err != nil {
			return err
		}
		require.True(t, scrubbed, "audit identity survives but erased private payloads do not")
		if err := tx.Raw(`SELECT count(*) FROM remember_attempt_diagnostics WHERE team_id=? AND attempt_id=?`, f.team, f.ingestA).Row().Scan(&count); err != nil {
			return err
		}
		require.Zero(t, count, "erasure removes archived content-bearing diagnostics")
		if err := tx.Raw(`SELECT count(*) FROM semantic_edges WHERE team_id=? AND relationship_id=?`, f.team, f.relationship).Row().Scan(&count); err != nil {
			return err
		}
		require.Zero(t, count)
		return nil
	}))
	require.ErrorIs(t, f.rls.WithTeamProfileTx(ownerCtx, app, f.team.String(), f.ownerB.String(), func(tx *gorm.DB) error {
		resolved, err := storage.ResolveMemoryHandleTx(ownerCtx, tx, f.team.String(), "evidence_fragments", f.evidence.String(), spaceB.String())
		require.NoError(t, err)
		require.Equal(t, f.evidence.String(), resolved, "erased copy mappings cannot recover content")
		return storage.AuthorizeMemoryMutationTx(ownerCtx, tx, f.team.String(), f.ownerB.String(), spaceB.String(), 1)
	}), domain.ErrMemoryWriteDenied, "the old B generation cannot restore erased support")
}

func TestPrivateMigrationRestoresForeignConflictAndScopesRelationshipHandles(t *testing.T) {
	f := legacyPrivateMigrationFixture(t)
	f.seedDependentRelationship(t)
	ctx := context.Background()
	foreignRelationship, foreignObject, conflict, preferred, suppressed := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	observation, verification, support := uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO entity_records(team_id,entity_id,entity_kind,space_id) VALUES(?,?,'project',?)`, []any{f.team, foreignObject, f.legacy}},
			{`INSERT INTO entity_resolution_events(team_id,ingest_id,owner_profile_id,mention_ref,action,entity_id,space_id) VALUES(?,?,?,'foreign choice','create',?,?)`, []any{f.team, f.ingestA, f.ownerA, foreignObject, f.legacy}},
			{`INSERT INTO entity_names(team_id,entity_id,owner_profile_id,display_name,normalized_name,name_kind,space_id) VALUES(?,?,?,'A foreign choice','a foreign choice','canonical',?)`, []any{f.team, foreignObject, f.ownerA, f.legacy}},
			{`INSERT INTO relationship_records(team_id,relationship_id,owner_profile_id,semantic_group_key,subject_entity_id,predicate_key,predicate_version,object_entity_id,relationship_kind,current_cardinality,status,support_count,source_group_count,space_id)
 SELECT team_id,?,?,'foreign-choice',subject_entity_id,predicate_key,predicate_version,?,'state','one','active',1,1,space_id FROM relationship_records WHERE team_id=? AND relationship_id=?`, []any{foreignRelationship, f.ownerA, foreignObject, f.team, f.relationship}},
			{`INSERT INTO relationship_observations(team_id,observation_id,relationship_id,ingest_id,owner_profile_id,subject_ref,original_predicate,object_ref,subject_entity_id,predicate_key,predicate_version,object_entity_id,space_id)
 SELECT team_id,?,relationship_id,?,owner_profile_id,'B endpoint','uses','A foreign choice',subject_entity_id,predicate_key,predicate_version,object_entity_id,space_id FROM relationship_records WHERE team_id=? AND relationship_id=?`, []any{observation, f.ingestA, f.team, foreignRelationship}},
			{`INSERT INTO verification_events(team_id,verification_event_id,observation_id,owner_profile_id,evidence_verdict,space_id) VALUES(?,?,?,?,'entailed',?)`, []any{f.team, verification, observation, f.ownerA, f.legacy}},
			{`INSERT INTO relationship_evidence_supports(team_id,support_id,relationship_id,observation_id,verification_event_id,fragment_id,owner_profile_id,evidence_owner_profile_id,source_group_key,span_start,span_end,quote,space_id)
 VALUES(?,?,?,?,?,?,?,?,'source-A',0,27,'A exact supporting evidence',?)`, []any{f.team, support, foreignRelationship, observation, verification, f.evidence, f.ownerA, f.ownerA, f.legacy}},
			{`INSERT INTO relationship_support_decision_events(team_id,support_id,relationship_id,owner_profile_id,decision,reason,space_id) VALUES(?,?,?,?,'grant','original support',?)`, []any{f.team, support, foreignRelationship, f.ownerA, f.legacy}},
			{`INSERT INTO relationship_conflict_cases(team_id,conflict_id,semantic_scope_key,status,subject_entity_id,predicate_key,relationship_kind,current_cardinality,review_due_at,next_review_at,review_ttl_days,resolved_at,effective_at,space_id)
 SELECT team_id,?,'legacy-mixed-conflict','resolved',subject_entity_id,predicate_key,'state','one',now(),now(),7,now(),now(),space_id FROM relationship_records WHERE team_id=? AND relationship_id=?`, []any{conflict, f.team, f.relationship}},
			{`INSERT INTO relationship_conflict_positions(team_id,conflict_id,position_id,position_key,object_entity_id,disposition,support_group_count,space_id)
 SELECT team_id,?,?,'A choice',object_entity_id,'preferred',1,space_id FROM relationship_records WHERE team_id=? AND relationship_id=?`, []any{conflict, preferred, f.team, foreignRelationship}},
			{`INSERT INTO relationship_conflict_positions(team_id,conflict_id,position_id,position_key,object_entity_id,disposition,support_group_count,space_id)
 SELECT team_id,?,?,'B choice',object_entity_id,'suppressed_current',1,space_id FROM relationship_records WHERE team_id=? AND relationship_id=?`, []any{conflict, suppressed, f.team, f.relationship}},
			{`UPDATE relationship_conflict_cases SET preferred_position_id=? WHERE team_id=? AND conflict_id=?`, []any{preferred, f.team, conflict}},
			{`INSERT INTO relationship_conflict_position_members(team_id,conflict_id,position_id,relationship_id,owner_profile_id,support_id,verification_event_id,fragment_id,source_group_key,accepted_at,space_id)
 SELECT team_id,?,CASE WHEN relationship_id=? THEN ?::uuid ELSE ?::uuid END,relationship_id,owner_profile_id,support_id,verification_event_id,fragment_id,source_group_key,now(),space_id FROM relationship_evidence_supports WHERE team_id=?`, []any{conflict, foreignRelationship, preferred, suppressed, f.team}},
			{`UPDATE relationship_records SET current_cardinality='one',status='superseded',valid_to=now(),recorded_to=now(),version=version+1 WHERE team_id=? AND relationship_id=?`, []any{f.team, f.relationship}},
			{`INSERT INTO relationship_transition_events(team_id,relationship_id,owner_profile_id,from_status,to_status,reason,space_id)
 VALUES(?,?,?,'active','superseded','conflict_resolved',?)`, []any{f.team, f.relationship, f.ownerB, f.legacy}},
		} {
			if err := tx.Exec(statement.query, statement.args...).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	report, err := f.repo.InspectPrivateMemoryMigration(ctx)
	require.NoError(t, err)
	require.Empty(t, report.Blockers)
	migrator, err := storage.NewMigrator(f.db)
	require.NoError(t, err)
	require.NoError(t, migrator.PreparePrivateMemoryMigration(ctx))
	require.NoError(t, f.repo.MigratePrivateMemory(ctx, report.SourceFingerprint))
	require.NoError(t, migrator.RunUp(ctx))
	app := f.applicationDB(t)
	var spaceA, spaceB uuid.UUID
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT id FROM memory_spaces WHERE owner_membership_id=? AND NOT migration_archive`, f.memberA).Row().Scan(&spaceA); err != nil {
			return err
		}
		return tx.Raw(`SELECT id FROM memory_spaces WHERE owner_membership_id=? AND NOT migration_archive`, f.memberB).Row().Scan(&spaceB)
	}))
	aCtx := requestctx.WithActor(ctx, requestctx.Actor{TeamID: f.team, IdentityID: f.ownerA, MembershipID: f.memberA, OwnerID: f.ownerA, Grants: []string{"read"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: spaceA, Kind: domain.MemorySpacePrivate, Generation: 1}}})
	bCtx := requestctx.WithActor(ctx, requestctx.Actor{TeamID: f.team, IdentityID: f.ownerB, MembershipID: f.memberB, OwnerID: f.ownerB, Grants: []string{"read"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: spaceB, Kind: domain.MemorySpacePrivate, Generation: 1}}})
	traces := tracepostgres.New(app, f.rls, nil, nil)
	input := tracecontract.Input{TeamID: f.team.String(), RelationshipID: f.relationship.String()}
	result, err := traces.TraceRelationship(bCtx, input)
	require.NoError(t, err, "the original B relationship handle remains usable after splitting")
	require.Equal(t, f.relationship.String(), result.Relationship.RelationshipID)
	require.Equal(t, f.ownerB.String(), result.Relationship.OwnerProfileID)
	require.Equal(t, "active", result.Relationship.Status)
	require.Nil(t, result.Relationship.ValidTo, "restore the original validity from the immutable observation")
	require.Nil(t, result.Relationship.RecordedTo)
	require.Equal(t, 1, result.Relationship.SupportCount)
	require.Len(t, result.Transitions, 2, "preserve the original suppression and append restoration")
	require.Len(t, result.SearchDocuments, 1)
	require.NoError(t, f.rls.WithTeamProfileTx(bCtx, app, f.team.String(), f.ownerB.String(), func(tx *gorm.DB) error {
		var document string
		if err := tx.Raw(`SELECT document_text FROM search_documents WHERE team_id=? AND source_id=? AND source_kind='relationship'`, f.team, f.relationship).Row().Scan(&document); err != nil {
			return err
		}
		require.Contains(t, document, "B endpoint")
		require.NotContains(t, document, "old mixed projection")
		require.NotContains(t, document, "A foreign choice")
		resolved, err := storage.ResolveMemoryHandleTx(bCtx, tx, f.team.String(), "relationship_records", f.relationship.String(), spaceB.String())
		require.NoError(t, err)
		require.Equal(t, f.relationship.String(), resolved)
		resolved, err = storage.ResolveMemoryHandleTx(bCtx, tx, f.team.String(), "relationship_records", foreignRelationship.String(), spaceA.String())
		require.NoError(t, err)
		require.Equal(t, foreignRelationship.String(), resolved, "an unauthorized destination cannot grant a handle")
		var count int
		if err := tx.Raw(`SELECT count(*) FROM semantic_edges WHERE team_id=? AND relationship_id=?`, f.team, f.relationship).Row().Scan(&count); err != nil {
			return err
		}
		require.Equal(t, 1, count, "foreign suppression must no longer hide B's graph edge")
		if err := tx.Raw(`SELECT count(*) FROM relationship_conflict_cases WHERE team_id=? AND dense_mem_space_allowed(space_id) AND space_generation=dense_mem_active_space_generation(team_id,space_id)`, f.team).Row().Scan(&count); err != nil {
			return err
		}
		require.Zero(t, count, "mixed conflict projections remain sealed")
		return nil
	}))
	_, err = traces.TraceRelationship(aCtx, input)
	require.ErrorIs(t, err, tracecontract.ErrRelationshipNotFound)
	_, err = traces.TraceRelationship(bCtx, tracecontract.Input{TeamID: f.team.String(), RelationshipID: foreignRelationship.String()})
	require.ErrorIs(t, err, tracecontract.ErrRelationshipNotFound)
	_, _, err = NewPrivateMemoryRepository(app, f.rls).RequestControlErasure(ctx, spaceB, strings.Repeat("3", 64), strings.Repeat("4", 64), "owner-erasure")
	require.NoError(t, err)
	_, err = traces.TraceRelationship(bCtx, input)
	require.ErrorIs(t, err, tracecontract.ErrRelationshipNotFound, "historical handles cannot recover a sealed generation")
}

func TestPrivateMigrationDetectsStoppedStateDrift(t *testing.T) {
	f := legacyPrivateMigrationFixture(t)
	ctx := context.Background()
	report, err := f.repo.InspectPrivateMemoryMigration(ctx)
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE credentials SET scopes=ARRAY['read'] WHERE id=?`, f.ownerA).Error
	}))
	current, err := f.repo.InspectPrivateMemoryMigration(ctx)
	require.NoError(t, err)
	require.NotEqual(t, report.SourceFingerprint, current.SourceFingerprint)
	migrator, err := storage.NewMigrator(f.db)
	require.NoError(t, err)
	require.NoError(t, migrator.PreparePrivateMemoryMigration(ctx))
	require.ErrorIs(t, f.repo.MigratePrivateMemory(ctx, report.SourceFingerprint), privacy.ErrPrivateMemoryMigrationBlocked)
}

func TestPrivateMigrationResumesEachCommittedPhase(t *testing.T) {
	for _, phase := range []string{"pending", "planned", "copying", "moving", "rebuilding", "ready"} {
		t.Run(phase, func(t *testing.T) {
			f := legacyPrivateMigrationFixture(t)
			f.seedDependentRelationship(t)
			ctx := context.Background()
			report, err := f.repo.InspectPrivateMemoryMigration(ctx)
			require.NoError(t, err)
			migrator, err := storage.NewMigrator(f.db)
			require.NoError(t, err)
			require.NoError(t, migrator.PreparePrivateMemoryMigration(ctx))
			if phase != "pending" {
				var plan *privateMigrationPlan
				require.NoError(t, f.rls.WithSystemReadOnlyRepeatableTx(ctx, f.db, func(tx *gorm.DB) error { var err error; plan, err = planPrivateMemoryMigration(ctx, tx); return err }))
				require.NoError(t, f.repo.stagePrivateMigration(ctx, plan))
			}
			if phase == "copying" {
				n, err := f.repo.movePrivateMigrationBatch(ctx, true)
				require.NoError(t, err)
				require.Positive(t, n)
			}
			if phase == "moving" || phase == "rebuilding" || phase == "ready" {
				for {
					n, err := f.repo.movePrivateMigrationBatch(ctx, true)
					require.NoError(t, err)
					if n == 0 {
						break
					}
				}
				for {
					n, err := f.repo.movePrivateMigrationBatch(ctx, false)
					require.NoError(t, err)
					if n == 0 || phase == "moving" {
						break
					}
				}
			}
			if phase == "rebuilding" || phase == "ready" {
				for {
					n, err := f.repo.rebuildPrivateMigrationBatch(ctx)
					require.NoError(t, err)
					if n == 0 {
						break
					}
				}
			}
			if phase == "ready" {
				require.NoError(t, f.repo.finishPrivateMigration(ctx))
			}
			if phase != "ready" {
				require.Error(t, migrator.RunUp(ctx), "incomplete migration cannot serve")
			}
			require.NoError(t, NewPrivateMemoryRepository(f.db, f.rls).MigratePrivateMemory(ctx, report.SourceFingerprint))
			require.NoError(t, migrator.RunUp(ctx))
			require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
				var incomplete int
				if err := tx.Raw(`SELECT count(*) FROM private_memory_migration_records WHERE NOT completed`).Row().Scan(&incomplete); err != nil {
					return err
				}
				require.Zero(t, incomplete)
				var hash, proposal string
				if err := tx.Raw(`SELECT request_hash,proposal::text FROM knowledge_ingests WHERE team_id=? AND ingest_id=?`, f.team, f.ingestA).Row().Scan(&hash, &proposal); err != nil {
					return err
				}
				require.Equal(t, "unchanged-hash:"+f.ingestA.String(), hash)
				require.JSONEq(t, `{"historical_targetless":true}`, proposal)
				return nil
			}))
		})
	}
}

func TestPrivateMigrationRejectsAmbiguousNodeWriter(t *testing.T) {
	f := legacyPrivateMigrationFixture(t)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.db, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO entity_records(team_id,entity_kind,space_id) VALUES(?,'project',?)`, f.team, f.legacy).Error
	}))
	report, err := f.repo.InspectPrivateMemoryMigration(context.Background())
	require.ErrorIs(t, err, privacy.ErrPrivateMemoryMigrationBlocked)
	require.Contains(t, strings.Join(report.Blockers, "\n"), "ambiguous_node_writer:entity_records:")
}

func TestPrivateMigrationPreservesSessionIdentityAndRecomputesMixedCheckpoints(t *testing.T) {
	f := legacyPrivateMigrationFixture(t)
	ctx := context.Background()
	submissionID := uuid.New()
	req := session.Request{IdempotencyKey: "original-session-operation", Framework: "test", AppName: "automation", UserID: "B", SessionID: "session", Events: []session.Event{{EventID: "one", Text: "B observed a retry condition."}}}
	intake := session.Intake{Scope: session.Scope{TeamID: f.team.String(), OwnerProfileID: f.ownerB.String(), SpaceID: f.legacy.String(), SpaceGeneration: 1}, Request: req, RequestHash: "sha256:" + strings.Repeat("5", 64), ExtractionVersion: session.ExtractionVersion, Tokenizer: "o200k_base", DuplicateResults: map[string]session.EventResult{}}
	var err error
	intake.RequestHash, err = sessionservice.RequestHash(req)
	require.NoError(t, err)
	intake.Windows, err = sessionservice.BuildWindows(req, intake.Tokenizer)
	require.NoError(t, err)
	require.Len(t, intake.Windows, 1)
	intakeBody, err := json.Marshal(intake)
	require.NoError(t, err)
	receipt := `{"contract_version":"dense-mem.v2.7.0","processing_state":"failed","errors":[{"code":"provider_unavailable","retryable":true}]}`
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO session_submissions(team_id,owner_profile_id,space_id,space_generation,submission_id,idempotency_key,request_hash,intake,prior_context,new_event_indices,accepted_event_count,duplicate_event_count,result)
 VALUES(?,?,?,1,?,?,?,?::jsonb,'[]','[0]',1,0,?::jsonb)`, f.team, f.ownerB, f.legacy, submissionID, req.IdempotencyKey, intake.RequestHash, string(intakeBody), receipt).Error; err != nil {
			return err
		}
		body, err := json.Marshal(req.Events[0])
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO session_events(team_id,owner_profile_id,space_id,space_generation,identity_hash,submission_id,event_index,framework,app_name,user_id,session_id,event_id,body)
 VALUES(?,?,?,1,?,?,0,?,?,?,?,?,?::jsonb)`, f.team, f.ownerB, f.legacy, session.IdentityHash(req, "one"), submissionID, req.Framework, req.AppName, req.UserID, req.SessionID, "one", string(body)).Error; err != nil {
			return err
		}
		cached, err := json.Marshal(migrationEmptyExtraction(session.ExtractionRequest{RequestID: fmt.Sprintf("session:%s:window:0", submissionID), Window: intake.Windows[0]}))
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO session_extraction_checkpoints(team_id,owner_profile_id,space_id,space_generation,submission_id,window_index,body) VALUES(?,?,?,1,?,0,?::jsonb)`, f.team, f.ownerB, f.legacy, submissionID, string(cached)).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO session_submission_receipts(team_id,owner_profile_id,space_id,space_generation,submission_id,body) VALUES(?,?,?,1,?,?::jsonb)`, f.team, f.ownerB, f.legacy, submissionID, receipt).Error
	}))
	report, err := f.repo.InspectPrivateMemoryMigration(ctx)
	require.NoError(t, err)
	migrator, err := storage.NewMigrator(f.db)
	require.NoError(t, err)
	require.NoError(t, migrator.PreparePrivateMemoryMigration(ctx))
	require.NoError(t, f.repo.MigratePrivateMemory(ctx, report.SourceFingerprint))
	require.NoError(t, migrator.RunUp(ctx))
	app := f.applicationDB(t)
	var destination uuid.UUID
	var generation int64
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT id,generation FROM memory_spaces WHERE owner_membership_id=? AND NOT migration_archive`, f.memberB).Row().Scan(&destination, &generation); err != nil {
			return err
		}
		var storedIntake, storedReceipt string
		if err := tx.Raw(`SELECT intake::text,result::text FROM session_submissions WHERE team_id=? AND space_id=? AND submission_id=?`, f.team, destination, submissionID).Row().Scan(&storedIntake, &storedReceipt); err != nil {
			return err
		}
		require.JSONEq(t, string(intakeBody), storedIntake)
		require.JSONEq(t, receipt, storedReceipt)
		var sealed int
		if err := tx.Raw(`SELECT count(*) FROM session_extraction_checkpoints checkpoint JOIN memory_spaces space ON space.id=checkpoint.space_id WHERE checkpoint.team_id=? AND checkpoint.submission_id=? AND space.migration_archive AND space.lifecycle_state='sealed'`, f.team, submissionID).Row().Scan(&sealed); err != nil {
			return err
		}
		require.Equal(t, 1, sealed)
		return nil
	}))
	actor := requestctx.Actor{TeamID: f.team, IdentityID: f.ownerB, MembershipID: f.memberB, OwnerID: f.ownerB, CredentialID: &f.ownerB, Grants: []string{"read"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: destination, Kind: domain.MemorySpacePrivate, Generation: generation}}}
	actorCtx := requestctx.WithActor(ctx, actor)
	scope := session.Scope{TeamID: f.team.String(), OwnerProfileID: f.ownerB.String(), SpaceID: destination.String(), SpaceGeneration: generation}
	store := knowledgepostgres.NewStore(app, f.rls, knowledgepostgres.ConflictRuntimeConfig{})
	loaded, err := store.LookupSession(actorCtx, scope, req.IdempotencyKey)
	require.NoError(t, err)
	require.Equal(t, submissionID.String(), loaded.ID)
	require.Equal(t, intake.RequestHash, loaded.Intake.RequestHash)
	require.Equal(t, req, loaded.Intake.Request)
	require.Equal(t, scope, loaded.Intake.Scope)
	require.Empty(t, loaded.Extractions)
	require.Empty(t, loaded.Linked)
	transport := &migrationSessionTransport{}
	api := sessionservice.NewService(sessionservice.Dependencies{Enabled: true, Repository: store, Tokenizer: intake.Tokenizer,
		Preparer:  processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store}),
		Extractor: extraction.NewProvider(transport, "migration-fixture", assessor.DefaultSemanticAssessmentLimits()),
	})
	result, err := api.Ingest(actorCtx, req)
	require.NoError(t, err)
	require.Equal(t, "completed", result.ProcessingState)
	require.Equal(t, submissionID.String(), result.SubmissionID)
	require.Len(t, transport.requests, 1, "even a valid old checkpoint must be regenerated")
	require.Empty(t, transport.requests[0].Prior)
	require.Equal(t, intake.Windows[0], transport.requests[0].Window)
	loaded, err = store.LookupSession(actorCtx, scope, req.IdempotencyKey)
	require.NoError(t, err)
	require.NotEmpty(t, loaded.Extractions[0])
	require.Equal(t, intake.RequestHash, loaded.Intake.RequestHash)
	require.Equal(t, req.IdempotencyKey, loaded.Intake.Request.IdempotencyKey)
	replayed, err := api.Ingest(actorCtx, req)
	require.NoError(t, err)
	expectedReceipt, err := json.Marshal(result)
	require.NoError(t, err)
	replayedReceipt, err := json.Marshal(replayed)
	require.NoError(t, err)
	require.Equal(t, string(expectedReceipt), string(replayedReceipt))
	require.Len(t, transport.requests, 1)
}

type migrationSessionTransport struct{ requests []session.ExtractionRequest }

func (p *migrationSessionTransport) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	var input session.ExtractionRequest
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	p.requests = append(p.requests, input)
	body, err := json.Marshal(migrationEmptyExtraction(input))
	return modelprovider.StructuredResult{Content: string(body)}, err
}

func migrationEmptyExtraction(input session.ExtractionRequest) session.ExtractionResponse {
	response := session.ExtractionResponse{RequestID: input.RequestID, Coverage: []string{}, Entities: []session.EntityProposal{}, Relationships: []session.RelationshipProposal{}, SecuritySignals: []session.ExtractionSignal{}}
	for _, segment := range input.Window.Core {
		response.Coverage = append(response.Coverage, segment.Ref)
	}
	return response
}

func TestPrivateMigrationCarriesLegacyHoldAndPreservesSharedHistory(t *testing.T) {
	f := legacyPrivateMigrationFixture(t)
	ctx := context.Background()
	sharedID := uuid.New()
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO private_memory_legal_holds(team_id,space_id,reason_code,actor_class) VALUES(?,?,'legacy-hold','control')`, f.team, f.legacy).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO knowledge_ingests(team_id,ingest_id,owner_profile_id,space_id,request_hash,idempotency_key,proposal,status)
 VALUES(?,?,?,(SELECT id FROM memory_spaces WHERE team_id=? AND kind='team_shared'),'shared-hash','shared-operation','{"exact":"shared history"}','completed')`, f.team, sharedID, f.ownerA, f.team).Error
	}))
	var before string
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT to_jsonb(row)::text FROM knowledge_ingests row WHERE team_id=? AND ingest_id=?`, f.team, sharedID).Row().Scan(&before)
	}))
	report, err := f.repo.InspectPrivateMemoryMigration(ctx)
	require.NoError(t, err)
	migrator, err := storage.NewMigrator(f.db)
	require.NoError(t, err)
	require.NoError(t, migrator.PreparePrivateMemoryMigration(ctx))
	require.NoError(t, f.repo.MigratePrivateMemory(ctx, report.SourceFingerprint))
	require.NoError(t, migrator.RunUp(ctx))
	var destination uuid.UUID
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		var after string
		if err := tx.Raw(`SELECT to_jsonb(row)::text FROM knowledge_ingests row WHERE team_id=? AND ingest_id=?`, f.team, sharedID).Row().Scan(&after); err != nil {
			return err
		}
		require.Equal(t, before, after)
		return tx.Raw(`SELECT id FROM memory_spaces WHERE owner_membership_id=? AND NOT migration_archive`, f.memberA).Row().Scan(&destination)
	}))
	_, _, err = f.repo.RequestControlErasure(ctx, destination, strings.Repeat("6", 64), strings.Repeat("7", 64), "legacy-hold")
	require.ErrorIs(t, err, ErrPrivateMemoryLegalHold)
	_, _, err = f.repo.ReleaseLegalHold(ctx, f.legacy)
	require.NoError(t, err)
	_, _, err = f.repo.RequestControlErasure(ctx, destination, strings.Repeat("6", 64), strings.Repeat("7", 64), "legacy-hold")
	require.NoError(t, err)
}

func TestPrivateMigrationArchiveHoldBlocksLineageErasure(t *testing.T) {
	f := legacyPrivateMigrationFixture(t)
	ctx := context.Background()
	report, err := f.repo.InspectPrivateMemoryMigration(ctx)
	require.NoError(t, err)
	migrator, err := storage.NewMigrator(f.db)
	require.NoError(t, err)
	require.NoError(t, migrator.PreparePrivateMemoryMigration(ctx))
	require.NoError(t, f.repo.MigratePrivateMemory(ctx, report.SourceFingerprint))
	require.NoError(t, migrator.RunUp(ctx))
	app := f.applicationDB(t)
	f.repo = NewPrivateMemoryRepository(app, f.rls)
	require.NoError(t, f.repo.Prepare(ctx))
	// Purge expired diagnostics so their row-delete guard cannot mask erasure of the held audit archive.
	deleted, err := knowledgepostgres.NewStore(app, f.rls, knowledgepostgres.ConflictRuntimeConfig{}).PurgeExpiredRememberAttemptDiagnostics(ctx, 100)
	require.NoError(t, err)
	require.Positive(t, deleted)
	unrelatedOwner := createLedgerProfile(t, f.db, f.rls, f.team.String(), "unrelated-private")
	var destination, unrelatedSpace uuid.UUID
	var cutoverAt time.Time
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT id FROM memory_spaces WHERE owner_membership_id=? AND NOT migration_archive`, f.memberA).Row().Scan(&destination); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT space.id FROM memory_spaces space JOIN team_memberships membership ON membership.id=space.owner_membership_id WHERE membership.actor_identity_id=? AND space.kind='private'`, unrelatedOwner).Row().Scan(&unrelatedSpace); err != nil {
			return err
		}
		return tx.Raw(`SELECT updated_at FROM private_memory_migration_state WHERE singleton AND phase='complete'`).Row().Scan(&cutoverAt)
	}))
	hold, created, err := f.repo.PlaceLegalHold(ctx, f.legacy, "post-cutover-archive-hold")
	require.NoError(t, err)
	require.True(t, created)
	require.True(t, hold.PlacedAt.After(cutoverAt))
	spaces, err := f.repo.ListSpaces(ctx, 100, 0)
	require.NoError(t, err)
	listedHold := false
	for _, space := range spaces {
		if space.Space.ID == f.legacy && space.ActiveHold != nil && space.ActiveHold.ID == hold.ID {
			listedHold = true
		}
	}
	require.True(t, listedHold)
	require.NoError(t, f.rls.WithSystemTx(ctx, app, func(tx *gorm.DB) error {
		var held bool
		if err := tx.Raw(`SELECT dense_mem_private_space_held(?)`, unrelatedSpace).Row().Scan(&held); err != nil {
			return err
		}
		require.False(t, held, "archive holds do not extend to unrelated private spaces")
		return nil
	}))
	_, created, err = f.repo.RequestControlErasure(ctx, destination, strings.Repeat("1", 64), strings.Repeat("2", 64), "archive-hold")
	assert.ErrorIs(t, err, ErrPrivateMemoryLegalHold)
	assert.False(t, created)
	_, _, err = f.repo.ReleaseLegalHold(ctx, f.legacy)
	require.NoError(t, err)
	operation, _, err := f.repo.RequestControlErasure(ctx, destination, strings.Repeat("1", 64), strings.Repeat("2", 64), "archive-hold")
	require.NoError(t, err)
	claim, err := f.repo.ClaimNext(ctx, "archive-hold-test", 0)
	require.NoError(t, err)
	require.NotNil(t, claim)
	_, _, err = f.repo.PlaceLegalHold(ctx, f.legacy, "claimed-archive-hold")
	require.NoError(t, err)
	completed, err := f.repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	assert.ErrorIs(t, err, ErrPrivateMemoryLegalHold)
	assert.Nil(t, completed)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		var before, after string
		if err := tx.Raw(`SELECT COALESCE(before_payload->>'content',''),COALESCE(after_payload->>'content','') FROM audit_log WHERE id=?`, f.auditID).Row().Scan(&before, &after); err != nil {
			return err
		}
		assert.Equal(t, "A private audit before", before)
		assert.Equal(t, "A private audit after", after)
		return nil
	}))
	if err == nil {
		return
	}
	require.NoError(t, f.repo.ReleaseClaim(ctx, claim.ID, claim.WorkerID, claim.Fence, "legal_hold"))
	retrying, err := f.repo.GetOperation(ctx, operation.ID)
	require.NoError(t, err)
	require.NotNil(t, retrying.NextAttemptAt)
	f.repo.now = func() time.Time { return *retrying.NextAttemptAt }
	claim, err = f.repo.ClaimNext(ctx, "archive-hold-test", 0)
	require.NoError(t, err)
	require.Nil(t, claim, "a queued archive hold must not consume another attempt")
	_, _, err = f.repo.ReleaseLegalHold(ctx, f.legacy)
	require.NoError(t, err)
	claim, err = f.repo.ClaimNext(ctx, "archive-hold-test", 0)
	require.NoError(t, err)
	require.NotNil(t, claim)
	completed, err = f.repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		var scrubbed bool
		if err := tx.Raw(`SELECT before_payload IS NULL AND after_payload IS NULL FROM audit_log WHERE id=?`, f.auditID).Row().Scan(&scrubbed); err != nil {
			return err
		}
		require.True(t, scrubbed)
		return nil
	}))
}
