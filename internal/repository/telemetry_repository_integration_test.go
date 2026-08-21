package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReadTelemetryLifecycleIsolatesSystemTeamAndProfileScopes(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "telemetry-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "telemetry-owner-a")
	ownerA2 := createLedgerProfile(t, adminDB, rls, teamA, "telemetry-owner-a2")
	teamB := createLedgerTeam(t, adminDB, rls, "telemetry-team-b")
	ownerB := createLedgerProfile(t, adminDB, rls, teamB, "telemetry-owner-b")

	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	createTelemetryRelationship(t, ctx, semantic, ledger, teamA, ownerA, "telemetry-a")
	createTelemetryRelationship(t, ctx, semantic, ledger, teamB, ownerB, "telemetry-b")

	reader := ledger
	from := time.Now().UTC().Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	teamAUUID := uuid.MustParse(teamA)
	ownerAUUID := uuid.MustParse(ownerA)
	ownerA2UUID := uuid.MustParse(ownerA2)

	system, err := reader.ReadTelemetryLifecycle(ctx, TelemetryLifecycleFilter{}, from, to)
	require.NoError(t, err)
	require.Equal(t, 2.0, system.Transitions["active"])
	require.Equal(t, 2.0, system.Current["active"])

	teamSnapshot, err := reader.ReadTelemetryLifecycle(ctx, TelemetryLifecycleFilter{TeamID: &teamAUUID}, from, to)
	require.NoError(t, err)
	require.Equal(t, 1.0, teamSnapshot.Transitions["active"])
	require.Equal(t, 1.0, teamSnapshot.Current["active"])

	profileSnapshot, err := reader.ReadTelemetryLifecycle(ctx, TelemetryLifecycleFilter{TeamID: &teamAUUID, ProfileID: &ownerAUUID}, from, to)
	require.NoError(t, err)
	require.Equal(t, 1.0, profileSnapshot.Transitions["active"])
	require.Equal(t, 1.0, profileSnapshot.Current["active"])

	otherOwnerSnapshot, err := reader.ReadTelemetryLifecycle(ctx, TelemetryLifecycleFilter{TeamID: &teamAUUID, ProfileID: &ownerA2UUID}, from, to)
	require.NoError(t, err)
	require.Empty(t, otherOwnerSnapshot.Transitions)
	require.Empty(t, otherOwnerSnapshot.Current)
}

func TestReadTelemetryLifecycleOmitsSealedGenerationRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "telemetry-sealed-generation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "telemetry-sealed-generation-owner")
	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	createTelemetryRelationship(t, ctx, semantic, ledger, teamID, ownerID, "telemetry-sealed-shared")

	privateSpace, err := NewMemorySpaceRepository(appDB, rls).EnsureCredentialPrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID))
	require.NoError(t, err)
	var privateGeneration int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, privateSpace.ID).Row().Scan(&privateGeneration)
	}))
	privateSubjectID := uuid.New()
	privateObjectID := uuid.New()
	privateRelationshipID := uuid.New()
	privateSubmissionID := uuid.New()
	privateCorrectionID := uuid.New()
	transitionID := uuid.New()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for _, entity := range []struct {
			id   uuid.UUID
			name string
		}{
			{privateSubjectID, "sealed telemetry subject"},
			{privateObjectID, "sealed telemetry object"},
		} {
			if err := tx.Exec(`
				INSERT INTO entity_records (
					team_id, entity_id, entity_kind, identity_context, metadata, space_id, space_generation
				) VALUES (?, ?, 'project', '{}'::jsonb, '{}'::jsonb, ?, ?)
			`, teamID, entity.id, privateSpace.ID, privateGeneration).Error; err != nil {
				return err
			}
			if err := tx.Exec(`
				INSERT INTO entity_names (
					team_id, entity_id, owner_profile_id, display_name, normalized_name, name_kind,
					metadata, space_id, space_generation
				) VALUES (?, ?, ?, ?, ?, 'canonical', '{}'::jsonb, ?, ?)
			`, teamID, entity.id, ownerID, entity.name, entity.name, privateSpace.ID, privateGeneration).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`
			INSERT INTO relationship_records (
				team_id, relationship_id, owner_profile_id, semantic_group_key,
				subject_entity_id, predicate_key, predicate_version, object_entity_id,
				relationship_kind, current_cardinality, status, support_count, metadata,
				space_id, space_generation
			) VALUES (?, ?, ?, 'sealed-telemetry-private', ?, 'uses', 1, ?, 'state', 'many', 'active', 1, '{}'::jsonb, ?, ?)
		`, teamID, privateRelationshipID, ownerID, privateSubjectID, privateObjectID, privateSpace.ID, privateGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_transition_events (
				team_id, transition_id, relationship_id, owner_profile_id,
				to_status, reason, metadata, space_id, space_generation
			) VALUES (?, ?, ?, ?, 'active', 'sealed generation test', '{}'::jsonb, ?, ?)
		`, teamID, transitionID, privateRelationshipID, ownerID, privateSpace.ID, privateGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_correction_submissions (
				team_id, submission_id, owner_profile_id, relationship_id, expected_version,
				request_hash, reason, idempotency_key, processing_state,
				successor_relationship_id, completed_at, space_id, space_generation
			) VALUES (?, ?, ?, ?, 1, 'sealed-correction-hash', 'sealed generation test',
				'sealed-correction-idempotency', 'completed', ?, now(), ?, ?)
		`, teamID, privateSubmissionID, ownerID, privateRelationshipID, privateRelationshipID, privateSpace.ID, privateGeneration).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO relationship_correction_events (
				team_id, correction_id, submission_id, owner_profile_id,
				original_relationship_id, original_relationship_version,
				successor_relationship_id, successor_relationship_version,
				reason, space_id, space_generation
			) VALUES (?, ?, ?, ?, ?, 1, ?, 1, 'sealed generation test', ?, ?)
		`, teamID, privateCorrectionID, privateSubmissionID, ownerID, privateRelationshipID, privateRelationshipID, privateSpace.ID, privateGeneration).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET lifecycle_state = 'sealed', generation = generation + 1, sealed_at = now(), updated_at = now()
			WHERE id = ?::uuid
		`, privateSpace.ID).Error
	}))

	from := time.Now().UTC().Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	teamUUID := uuid.MustParse(teamID)
	snapshot, err := ledger.ReadTelemetryLifecycle(ctx, TelemetryLifecycleFilter{TeamID: &teamUUID}, from, to)
	require.NoError(t, err)
	assert.Equal(t, 1.0, snapshot.Transitions["active"])
	assert.Equal(t, 0.0, snapshot.Corrections)
	assert.Equal(t, 1.0, snapshot.Current["active"])
}

func createTelemetryRelationship(t *testing.T, ctx context.Context, semantic *SemanticRepositoryImpl, ledger *LedgerRepositoryImpl, teamID, ownerID, key string) {
	t.Helper()
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", key+" subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", key+" object")
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, key+"-ingest", key+" subject uses "+key+" object")
	result := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", PredicateVersion: 1,
		ObjectEntityID: object.EntityID, EvidenceVerdict: "entailed",
		Support: &EvidenceSupportInput{
			FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: key,
			SpanStart: 0, SpanEnd: len(key + " subject uses " + key + " object"), Authority: "primary",
		},
	})
	require.NotNil(t, result.Relationship)
}
