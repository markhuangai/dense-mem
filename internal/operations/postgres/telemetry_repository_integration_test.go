//go:build integration

package postgres

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	dreampostgres "github.com/markhuangai/dense-mem/internal/dream/postgres"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/observability"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
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

	semantic := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	createTelemetryRelationship(t, ctx, semantic, ledger, teamA, ownerA, "telemetry-a")
	createTelemetryRelationship(t, ctx, semantic, ledger, teamB, ownerB, "telemetry-b")

	reader := NewTelemetryLifecycleRepository(appDB, rls)
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
	semantic := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	createTelemetryRelationship(t, ctx, semantic, ledger, teamID, ownerID, "telemetry-sealed-shared")
	reader := NewTelemetryLifecycleRepository(appDB, rls)

	privateSpace, err := privacypostgres.NewMemorySpaceRepository(appDB, rls).EnsureCredentialPrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID))
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
	snapshot, err := reader.ReadTelemetryLifecycle(ctx, TelemetryLifecycleFilter{TeamID: &teamUUID}, from, to)
	require.NoError(t, err)
	assert.Equal(t, 1.0, snapshot.Transitions["active"])
	assert.Equal(t, 0.0, snapshot.Corrections)
	assert.Equal(t, 1.0, snapshot.Current["active"])
	operational, err := reader.ReadOperationalTelemetry(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1.0, findNamedTelemetryCount(operational.RelationshipsCurrent, "active"))
	assert.Equal(t, 1.0, findWindowedTelemetryCount(operational.RelationshipTransitions, "15m", "active"))
}

func TestReadOperationalTelemetryUsesDurableLedgerAndSurvivesCollectorRecreation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "operational-telemetry-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "operational-telemetry-owner")
	semantic := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	firstIngestID := createTelemetryRelationship(t, ctx, semantic, ledger, teamID, ownerID, "telemetry-confirmed")
	secondIngestID := createTelemetryRelationship(t, ctx, semantic, ledger, teamID, ownerID, "telemetry-rejected", "-")
	thirdIngestID := createTelemetryRelationship(t, ctx, semantic, ledger, teamID, ownerID, "telemetry-unconfirmed")
	fourthIngestID := createTelemetryRelationship(t, ctx, semantic, ledger, teamID, ownerID, "telemetry-refuted", "-")
	createTelemetryRelationshipIdentityAlias(t, ctx, adminDB, rls, teamID, ownerID, firstIngestID)

	dreamStore := dreampostgres.NewStore(appDB, rls)
	started := time.Now().UTC()
	run, err := dreamStore.ClaimDreamCycle(ctx, dreamcontract.DreamCycleClaimInput{
		TeamID: teamID, InitiatedByProfileID: ownerID, RunDate: started.Format("2006-01-02"),
		WindowKey: "operational-telemetry-" + uuid.NewString(), LeaseToken: uuid.NewString(), LeaseUntil: started.Add(time.Minute),
	})
	require.NoError(t, err)
	require.True(t, run.Claimed)
	require.NoError(t, dreamStore.CompleteDreamCycle(ctx, dreamcontract.DreamCycleCompleteInput{
		TeamID: teamID, InitiatedByProfileID: ownerID, RunID: run.RunID, LeaseToken: run.LeaseToken,
		Status: "completed", InputCount: 4, ProviderProposals: 0, CreatedHypotheses: 0,
	}))
	hypothesisID := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO hypotheses (
				team_id, hypothesis_id, created_by_profile_id, status, payload, statement,
				space_id, space_generation, lane, created_at, updated_at
			) VALUES (
				?::uuid, ?::uuid, ?::uuid, 'proposed', '{}'::jsonb, '',
				dense_mem_team_shared_space(?::uuid), dense_mem_team_shared_generation(?::uuid), 'graph',
				now() - interval '2 hours', now()
			)
		`, teamID, hypothesisID, ownerID, teamID, teamID).Error; err != nil {
			return err
		}
		for _, feedback := range []struct {
			decision  string
			ingestID  *string
			olderThan bool
		}{
			{decision: "confirm_true", ingestID: &firstIngestID},
			{decision: "confirm_false", ingestID: &secondIngestID, olderThan: true},
			{decision: "confirm_true"},
			{decision: "confirm_false", ingestID: &fourthIngestID},
		} {
			var ingestID any
			if feedback.ingestID != nil {
				ingestID = *feedback.ingestID
			}
			if err := tx.Exec(`
				INSERT INTO hypothesis_feedback_events (
					team_id, feedback_event_id, space_id, space_generation, hypothesis_id,
					actor_profile_id, decision, feedback, submitted_ingest_id, created_at
				) VALUES (
					?::uuid, gen_random_uuid(), dense_mem_team_shared_space(?::uuid),
					dense_mem_team_shared_generation(?::uuid), ?::uuid, ?::uuid, ?, 'fixture',
					NULLIF(?, '')::uuid, CASE WHEN ? THEN now() - interval '2 hours' ELSE now() END
				)
			`, teamID, teamID, teamID, hypothesisID, ownerID, feedback.decision, ingestID, feedback.olderThan).Error; err != nil {
				return err
			}
		}
		canonicalAttemptID := uuid.NewString()
		for _, attempt := range []struct {
			id          string
			key         string
			requestHash string
			outcome     string
			canonicalID string
		}{
			{id: canonicalAttemptID, key: "telemetry-0", requestHash: "request-0", outcome: "completed"},
			{id: uuid.NewString(), key: "telemetry-0", requestHash: "request-0", outcome: "replayed", canonicalID: canonicalAttemptID},
			{id: uuid.NewString(), key: "telemetry-2", requestHash: "request-2", outcome: "failed"},
		} {
			if err := tx.Exec(`
				INSERT INTO remember_attempts (
					team_id, attempt_id, owner_profile_id, space_id, space_generation,
					idempotency_key, request_hash, contract_version, submission_kind, outcome, canonical_attempt_id,
					created_at
				) VALUES (
					?::uuid, ?::uuid, ?::uuid, dense_mem_team_shared_space(?::uuid),
					dense_mem_team_shared_generation(?::uuid), ?, ?, 'dense-mem.v2.6', 'remember', ?, NULLIF(?, '')::uuid, now()
				)
			`, teamID, attempt.id, ownerID, teamID, teamID, attempt.key, attempt.requestHash, attempt.outcome, attempt.canonicalID).Error; err != nil {
				return err
			}
		}
		return nil
	}))

	reader := NewTelemetryLifecycleRepository(appDB, rls)
	snapshot, err := reader.ReadOperationalTelemetry(ctx)
	require.NoError(t, err)
	dreamRun := findDreamRunTelemetry(snapshot.DreamRuns, "1h", "graph", "completed")
	require.Equal(t, 1.0, dreamRun.Runs)
	require.Equal(t, 1.0, dreamRun.Attempts)
	require.Equal(t, 4.0, dreamRun.InputTargets)
	require.Zero(t, dreamRun.ProviderProposals)
	require.Zero(t, dreamRun.CreatedHypotheses)
	hypothesis := findHypothesisTelemetry(snapshot.Hypotheses, "graph", "proposed")
	require.Equal(t, 1.0, hypothesis.Count)
	require.Equal(t, 1.0, hypothesis.BacklogCount)
	require.GreaterOrEqual(t, hypothesis.OldestBacklogAge, 7000.0)
	require.Less(t, hypothesis.OldestBacklogAge, 8000.0)
	require.Equal(t, 2.0, findWindowedTelemetryCount(snapshot.Feedback, "15m", "confirm_true"))
	require.Equal(t, 1.0, findWindowedTelemetryCount(snapshot.Feedback, "15m", "confirm_false"))
	require.Equal(t, 2.0, findWindowedTelemetryCount(snapshot.Feedback, "12h", "confirm_false"))
	require.Zero(t, findWindowedTelemetryCount(snapshot.Feedback, "15m", "ignore"), "no feedback event is not inferred as an explicit ignore")
	require.Equal(t, 1.0, findWindowedTelemetryCount(snapshot.RememberAttempts, "15m", "completed"))
	require.Equal(t, 1.0, findWindowedTelemetryCount(snapshot.RememberAttempts, "15m", "replayed"))
	require.Equal(t, 1.0, findWindowedTelemetryCount(snapshot.RememberAttempts, "15m", "failed"))
	require.Equal(t, 2.0, findWindowedTelemetryCount(snapshot.ConfirmedRelationships, "15m", "active"), "positive and refuting feedback finalized in the window count with completed ingests")
	require.Equal(t, 3.0, findWindowedTelemetryCount(snapshot.ConfirmedRelationships, "12h", "active"), "the older refuting feedback counts in its finalization window")
	require.Zero(t, findWindowedTelemetryCount(snapshot.ConfirmedRelationships, "15m", "needs_review"), "identity aliases do not count as confirmed Relationships")
	require.Equal(t, 4.0, findNamedTelemetryCount(snapshot.RelationshipsCurrent, "active"))
	require.Zero(t, findNamedTelemetryCount(snapshot.RelationshipsCurrent, "needs_review"), "identity aliases are not canonical current Relationships")
	require.Equal(t, 4.0, findWindowedTelemetryCount(snapshot.RelationshipTransitions, "15m", "active"))
	require.NotEmpty(t, thirdIngestID)
	require.NotEmpty(t, fourthIngestID)
	assertOperationalTelemetryWindowIndexPlans(t, ctx, appDB, rls)

	for _, recreatedReader := range []*TelemetryLifecycleRepository{reader, NewTelemetryLifecycleRepository(appDB, rls)} {
		metrics := observability.NewPrometheusMetrics()
		require.NoError(t, metrics.RegisterOperationalTelemetryCollector(recreatedReader))
		response := httptest.NewRecorder()
		metrics.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
		require.Contains(t, response.Body.String(), `densemem_operational_ledger_collection_success 1`)
		require.Contains(t, response.Body.String(), `densemem_operational_dream_provider_proposals{lane="graph",status="completed",window="1h"} 0`)
		require.Contains(t, response.Body.String(), `densemem_operational_dream_confirmed_relationships{status="active",window="15m"} 2`)
		require.NotContains(t, response.Body.String(), teamID)
		require.NotContains(t, response.Body.String(), ownerID)
		require.NotContains(t, response.Body.String(), hypothesisID)
	}
}

func assertOperationalTelemetryWindowIndexPlans(t *testing.T, ctx context.Context, db *gorm.DB, rls *storagepostgres.RLS) {
	t.Helper()
	queries := []struct {
		index string
		query string
	}{
		{
			index: "dream_cycle_runs_telemetry_window_idx",
			query: `EXPLAIN (COSTS OFF) WITH ` + operationalTelemetryWindowCTE + `
				SELECT window_bounds.window_key, count(*)
				FROM dream_cycle_runs AS cycle
				CROSS JOIN window_bounds
				WHERE cycle.canonical_run_id IS NULL
				  AND cycle.started_at >= window_bounds.starts_at
				  AND ` + activeSemanticSpaceGenerationSQL("cycle") + `
				GROUP BY window_bounds.window_key`,
		},
		{
			index: "hypothesis_feedback_events_telemetry_window_idx",
			query: `EXPLAIN (COSTS OFF) WITH ` + operationalTelemetryWindowCTE + `
				SELECT window_bounds.window_key, feedback.decision, count(*)
				FROM hypothesis_feedback_events AS feedback
				CROSS JOIN window_bounds
				WHERE feedback.created_at >= window_bounds.starts_at
				  AND ` + activeSemanticSpaceGenerationSQL("feedback") + `
				GROUP BY window_bounds.window_key, feedback.decision`,
		},
	}

	plans := make([]string, len(queries))
	err := rls.WithSystemReadOnlyRepeatableTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
			return err
		}
		for i, query := range queries {
			rows, err := tx.WithContext(ctx).Raw(query.query).Rows()
			if err != nil {
				return err
			}
			var plan strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					_ = rows.Close()
					return err
				}
				plan.WriteString(line)
				plan.WriteByte('\n')
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			plans[i] = plan.String()
		}
		return nil
	})
	require.NoError(t, err)
	for i, query := range queries {
		require.Contains(t, plans[i], query.index)
	}
}

func TestOperationalTelemetryCollectionMarksPartialLedgerFailureUnavailable(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	teamID := createLedgerTeam(t, adminDB, rls, "operational-telemetry-outage-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "operational-telemetry-outage-owner")
	semantic := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	createTelemetryRelationship(t, context.Background(), semantic, ledger, teamID, ownerID, "telemetry-outage")
	require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
		return tx.Exec("REVOKE SELECT ON TABLE relationship_correction_events FROM " + ledgerTestRole).Error
	}))

	metrics := observability.NewPrometheusMetrics()
	reader := NewTelemetryLifecycleRepository(appDB, rls)
	require.NoError(t, metrics.RegisterOperationalTelemetryCollector(reader))
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	require.Contains(t, response.Body.String(), `densemem_operational_ledger_collection_success 0`)
	require.NotContains(t, response.Body.String(), "densemem_operational_dream_confirmed_relationships")
	require.NotContains(t, response.Body.String(), "densemem_operational_relationships_current")
	require.NotContains(t, response.Body.String(), "densemem_operational_dream_runs")
}

func createTelemetryRelationship(t *testing.T, ctx context.Context, semantic *knowledgepostgres.Store, ledger *knowledgepostgres.Store, teamID, ownerID, key string, polarity ...string) string {
	t.Helper()
	relationshipPolarity := "+"
	if len(polarity) > 0 {
		relationshipPolarity = polarity[0]
	}
	statement := key + " subject uses " + key + " object"
	if relationshipPolarity == "-" {
		statement = key + " subject does not use " + key + " object"
	}
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", key+" subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", key+" object")
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, key+"-ingest", statement)
	result := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", PredicateVersion: 1,
		ObjectEntityID: object.EntityID, EvidenceVerdict: "entailed", Polarity: relationshipPolarity,
		Support: &EvidenceSupportInput{
			FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: key,
			SpanStart: 0, SpanEnd: len(statement), Authority: "primary",
		},
	})
	require.NotNil(t, result.Relationship)
	return ingest.IngestID
}

func createTelemetryRelationshipIdentityAlias(t *testing.T, ctx context.Context, db *gorm.DB, rls *storagepostgres.RLS, teamID, ownerID, ingestID string) {
	t.Helper()
	aliasID := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		var canonicalID string
		if err := tx.Raw(`
			SELECT relationship_id::text
			FROM relationship_observations
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid AND relationship_id IS NOT NULL
			ORDER BY created_at, observation_id
			LIMIT 1
		`, teamID, ingestID).Row().Scan(&canonicalID); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_records (
				team_id, relationship_id, owner_profile_id, semantic_group_key,
				subject_entity_id, predicate_key, predicate_version, object_entity_id, object_value_id,
				relationship_kind, current_cardinality, status, polarity, scope_key,
				valid_from, valid_to, support_count, source_group_count, version, metadata,
				created_at, updated_at, recorded_to, space_id, space_generation,
				identity_alias_of_relationship_id
			)
			SELECT team_id, ?::uuid, owner_profile_id, semantic_group_key,
				subject_entity_id, predicate_key, predicate_version, object_entity_id, object_value_id,
				relationship_kind, current_cardinality, 'needs_review', polarity, scope_key,
				valid_from, valid_to, support_count, source_group_count, version, metadata,
				created_at, now(), now(), space_id, space_generation, relationship_id
			FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid AND owner_profile_id = ?::uuid
		`, aliasID, teamID, canonicalID, ownerID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO relationship_observations (
				team_id, observation_id, relationship_id, ingest_id, owner_profile_id,
				subject_ref, original_predicate, object_ref, subject_entity_id, predicate_key,
				predicate_version, object_entity_id, object_value_id, polarity, scope_key, valid_from,
				valid_to, evidence, metadata, created_at, space_id, space_generation
			)
			SELECT team_id, gen_random_uuid(), ?::uuid, ingest_id, owner_profile_id,
				subject_ref, original_predicate, object_ref, subject_entity_id, predicate_key,
				predicate_version, object_entity_id, object_value_id, polarity, scope_key, valid_from,
				valid_to, evidence, metadata, now(), space_id, space_generation
			FROM relationship_observations
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid AND owner_profile_id = ?::uuid
		`, aliasID, teamID, canonicalID, ownerID).Error
	}))
}

func findDreamRunTelemetry(values []operationscontract.DreamRunTelemetry, window, lane, status string) operationscontract.DreamRunTelemetry {
	for _, value := range values {
		if value.Window == window && value.Lane == lane && value.Status == status {
			return value
		}
	}
	return operationscontract.DreamRunTelemetry{}
}

func findHypothesisTelemetry(values []operationscontract.HypothesisTelemetry, lane, status string) operationscontract.HypothesisTelemetry {
	for _, value := range values {
		if value.Lane == lane && value.Status == status {
			return value
		}
	}
	return operationscontract.HypothesisTelemetry{}
}

func findWindowedTelemetryCount(values []operationscontract.WindowedTelemetryCount, window, kind string) float64 {
	for _, value := range values {
		if value.Window == window && value.Kind == kind {
			return value.Count
		}
	}
	return 0
}

func findNamedTelemetryCount(values []operationscontract.NamedTelemetryCount, kind string) float64 {
	for _, value := range values {
		if value.Kind == kind {
			return value.Count
		}
	}
	return 0
}
