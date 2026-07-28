package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDreamRepositoryCandidateSafeHypothesisLifecycle(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	mark := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Mark Huang")
	alex := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Alex")
	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")

	candidateIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"dream candidate source", "Dense-Mem may use PostgreSQL.")
	candidate := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        candidateIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		EvidenceVerdict: "insufficient",
	})
	require.NotNil(t, candidate.Relationship)

	activeIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"dream active source", "Mark Huang works on Dense-Mem.")
	active := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        activeIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     activeIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:dream-active",
			SpanStart:      0,
			SpanEnd:        len("Mark Huang works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, active.Relationship)

	unsupportedIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"dream unsupported active source", "Alex works on PostgreSQL.")
	unsupported := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        unsupportedIngest.IngestID,
		SubjectEntityID: alex.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  postgres.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     unsupportedIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:dream-unsupported",
			SpanStart:      0,
			SpanEnd:        len("Alex works on PostgreSQL."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, unsupported.Relationship)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET support_count = 0
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, unsupported.Relationship.RelationshipID).Error
	}))

	run, err := semanticRepo.ClaimDreamCycle(ctx, DreamCycleClaimInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		RunDate:        "2026-07-17",
		WindowKey:      "dream-lifecycle",
		LeaseUntil:     time.Now().UTC().Add(time.Minute),
		SourceSnapshot: []map[string]any{{
			"relationship_id": candidate.Relationship.RelationshipID,
			"version":         candidate.Relationship.Version,
		}},
	})
	require.NoError(t, err)
	require.True(t, run.Claimed)

	inputs, err := semanticRepo.ListDreamInputs(ctx, DreamInputListInput{
		TeamID: teamID,
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, inputs, 2)
	assertDreamInput(t, inputs, candidate.Relationship.RelationshipID, "pending_evidence")
	assertDreamInput(t, inputs, active.Relationship.RelationshipID, "active")
	assertDreamInputMissing(t, inputs, unsupported.Relationship.RelationshipID)

	proposal := UpsertHypothesisInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		RunID:            run.RunID,
		Statement:        "Dense-Mem may use PostgreSQL.",
		Rationale:        "The candidate needs independent evidence before semantic commitment.",
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     "uses",
		PredicateVersion: 1,
		ObjectEntityID:   postgres.EntityID,
		SourceRefs: []map[string]any{{
			"type": "candidate_relationship",
			"id":   candidate.Relationship.RelationshipID,
		}},
		SourceVersions: map[string]int{
			candidate.Relationship.RelationshipID: candidate.Relationship.Version,
		},
		SourceOwnerProfileIDs: []string{ownerID},
		ContentHash:           "sha256:dream-candidate-postgres",
		GeneratorKind:         "test",
		GeneratorVersion:      "test-dream",
		Payload:               map[string]any{"source_status": "pending_evidence"},
	}
	record, inserted, err := semanticRepo.UpsertHypothesis(ctx, proposal)
	require.NoError(t, err)
	require.True(t, inserted)
	require.NotEmpty(t, record.HypothesisID)
	assert.Equal(t, "proposed", record.Status)

	record, inserted, err = semanticRepo.UpsertHypothesis(ctx, proposal)
	require.NoError(t, err)
	require.False(t, inserted)
	assert.Equal(t, "reinforced", record.Status)

	recall, err := semanticRepo.RecallHypotheses(ctx, RecallHypothesesInput{
		TeamID: teamID,
		Query:  "PostgreSQL",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Len(t, recall, 1)
	assert.Equal(t, record.HypothesisID, recall[0].HypothesisID)

	staleProposal := proposal
	staleProposal.ContentHash = "sha256:dream-candidate-stale"
	staleProposal.Statement = "Dense-Mem may use PostgreSQL after the source changes."
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET version = version + 1
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, candidate.Relationship.RelationshipID).Error
	}))
	staleCount, err := semanticRepo.RefreshHypothesisStaleness(ctx, RefreshHypothesisStalenessInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, staleCount)
	stale, _, err := semanticRepo.ListHypotheses(ctx, ListHypothesesInput{
		TeamID: teamID,
		Status: "stale",
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, record.HypothesisID, stale[0].HypothesisID)

	_, _, err = semanticRepo.UpsertHypothesis(ctx, staleProposal)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDreamSourceStale), err)

	exactActive := UpsertHypothesisInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		RunID:            run.RunID,
		Statement:        "Mark Huang may work on Dense-Mem.",
		Rationale:        "This duplicates an already active relationship.",
		SubjectEntityID:  mark.EntityID,
		PredicateKey:     "works_on",
		PredicateVersion: 1,
		ObjectEntityID:   denseMem.EntityID,
		SourceRefs: []map[string]any{{
			"type": "relationship",
			"id":   active.Relationship.RelationshipID,
		}},
		SourceVersions: map[string]int{
			active.Relationship.RelationshipID: active.Relationship.Version,
		},
		SourceOwnerProfileIDs: []string{ownerID},
		ContentHash:           "sha256:dream-exact-active",
		GeneratorKind:         "test",
		GeneratorVersion:      "test-dream",
		Payload:               map[string]any{"source_status": "active"},
	}
	_, _, err = semanticRepo.UpsertHypothesis(ctx, exactActive)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDreamExactRelationshipExists), err)
}

func TestDreamControlRepositoryIsTeamScopedAndAuditsAtomicRefresh(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "dream-control-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "dream-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "dream-owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "dream-control-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "dream-owner-c")
	repo := NewSemanticRepository(appDB, rls)

	hypothesisA := uuid.NewString()
	hypothesisB := uuid.NewString()
	hypothesisC := uuid.NewString()
	missingSourceA := uuid.NewString()
	missingSourceB := uuid.NewString()
	missingSourceC := uuid.NewString()
	runA := uuid.NewString()
	runB := uuid.NewString()
	runC := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for _, ref := range []struct {
			teamID  string
			ownerID string
		}{
			{teamID: teamA, ownerID: ownerA},
			{teamID: teamA, ownerID: ownerB},
			{teamID: teamC, ownerID: ownerC},
		} {
			if err := ensureSemanticRefs(ctx, tx, ref.teamID, ref.ownerID); err != nil {
				return err
			}
		}
		return tx.Exec(`
			INSERT INTO hypotheses (
			    team_id, hypothesis_id, owner_profile_id, status, statement,
			    source_versions, updated_at
			) VALUES
			    (?::uuid, ?::uuid, ?::uuid, 'proposed', 'owner A hypothesis', jsonb_build_object(?::text, 1), now() - interval '2 minutes'),
			    (?::uuid, ?::uuid, ?::uuid, 'proposed', 'owner B hypothesis', jsonb_build_object(?::text, 1), now() - interval '1 minute'),
			    (?::uuid, ?::uuid, ?::uuid, 'proposed', 'owner C hypothesis', jsonb_build_object(?::text, 1), now())
		`, teamA, hypothesisA, ownerA, missingSourceA,
			teamA, hypothesisB, ownerB, missingSourceB,
			teamC, hypothesisC, ownerC, missingSourceC).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO dream_cycle_runs (
			    team_id, run_id, owner_profile_id, run_date, window_key,
			    status, started_at, completed_at
			) VALUES
			    (?::uuid, ?::uuid, ?::uuid, '2026-07-28', 'owner-a', 'completed', now() - interval '2 hours', now() - interval '2 hours'),
			    (?::uuid, ?::uuid, ?::uuid, '2026-07-28', 'owner-b', 'completed', now() - interval '1 hour', now() - interval '1 hour'),
			    (?::uuid, ?::uuid, ?::uuid, '2026-07-28', 'owner-c', 'completed', now(), now())
		`, teamA, runA, ownerA, teamA, runB, ownerB, teamC, runC, ownerC).Error
	}))

	records, _, err := repo.ListHypotheses(ctx, ListHypothesesInput{TeamID: teamA, Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.ElementsMatch(t, []string{ownerA, ownerB}, []string{records[0].OwnerProfileID, records[1].OwnerProfileID})

	pending, err := repo.CountHypotheses(ctx, teamA, "proposed")
	require.NoError(t, err)
	assert.Equal(t, 2, pending)

	runs, err := repo.ListDreamCyclesForTeam(ctx, teamA, 10)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, runB, runs[0].RunID)
	assert.Equal(t, runA, runs[1].RunID)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			ALTER TABLE audit_log
			ADD CONSTRAINT audit_log_reject_dream_refresh
			CHECK (operation <> 'dream_staleness_refreshed')
		`).Error
	}))
	defer func() {
		_ = rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
			return tx.Exec(`
				ALTER TABLE audit_log
				DROP CONSTRAINT IF EXISTS audit_log_reject_dream_refresh
			`).Error
		})
	}()

	refresh := RefreshTeamHypothesisStalenessInput{
		TeamID:        teamA,
		Limit:         200,
		ActorSource:   "control_portal:authorization-bearer",
		ActorRole:     "control",
		ClientIP:      "192.0.2.10",
		CorrelationID: "corr-dream-control",
	}
	_, err = repo.RefreshTeamHypothesisStaleness(ctx, refresh)
	require.Error(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var proposed int
		if err := tx.Raw(`
			SELECT count(*)
			FROM hypotheses
			WHERE team_id = ?::uuid
			  AND status = 'proposed'
		`, teamA).Scan(&proposed).Error; err != nil {
			return err
		}
		assert.Equal(t, 2, proposed)
		return nil
	}))

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			ALTER TABLE audit_log
			DROP CONSTRAINT audit_log_reject_dream_refresh
		`).Error
	}))
	updated, err := repo.RefreshTeamHypothesisStaleness(ctx, refresh)
	require.NoError(t, err)
	assert.Equal(t, 2, updated)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var staleA, proposedC, auditCount, auditedUpdated int
		if err := tx.Raw(`
			SELECT count(*)
			FROM hypotheses
			WHERE team_id = ?::uuid
			  AND status = 'stale'
		`, teamA).Scan(&staleA).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT count(*)
			FROM hypotheses
			WHERE team_id = ?::uuid
			  AND status = 'proposed'
		`, teamC).Scan(&proposedC).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT count(*), max((after_payload->>'updated_count')::int)
			FROM audit_log
			WHERE team_id = ?::uuid
			  AND operation = 'dream_staleness_refreshed'
			  AND actor_profile_id IS NULL
			  AND actor_role = 'control'
			  AND client_ip = '192.0.2.10'::inet
			  AND correlation_id = 'corr-dream-control'
			  AND metadata->>'actor_source' = 'control_portal:authorization-bearer'
		`, teamA).Row().Scan(&auditCount, &auditedUpdated); err != nil {
			return err
		}
		assert.Equal(t, 2, staleA)
		assert.Equal(t, 1, proposedC)
		assert.Equal(t, 1, auditCount)
		assert.Equal(t, 2, auditedUpdated)
		return nil
	}))

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE teams
			SET status = 'archived',
			    deleted_at = now()
			WHERE id = ?::uuid
		`, teamA).Error
	}))
	_, err = repo.RefreshTeamHypothesisStaleness(ctx, refresh)
	require.ErrorIs(t, err, ErrTeamInactive)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var auditCount int
		if err := tx.Raw(`
			SELECT count(*)
			FROM audit_log
			WHERE team_id = ?::uuid
			  AND operation = 'dream_staleness_refreshed'
		`, teamA).Scan(&auditCount).Error; err != nil {
			return err
		}
		assert.Equal(t, 1, auditCount)
		return nil
	}))

	raceTeam := createLedgerTeam(t, adminDB, rls, "dream-control-archive-race")
	raceOwner := createLedgerProfile(t, adminDB, rls, raceTeam, "dream-control-archive-owner")
	raceHypothesis := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := ensureSemanticRefs(ctx, tx, raceTeam, raceOwner); err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO hypotheses (
			    team_id, hypothesis_id, owner_profile_id, status, statement,
			    source_versions, updated_at
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, 'proposed', 'archive race hypothesis',
			    jsonb_build_object(?::text, 1), now()
			)
		`, raceTeam, raceHypothesis, raceOwner, uuid.NewString()).Error
	}))

	appSQL, err := appDB.DB()
	require.NoError(t, err)
	appSQL.SetMaxOpenConns(1)
	appSQL.SetMaxIdleConns(1)
	require.NoError(t, appDB.Exec(`SET lock_timeout = '100ms'`).Error)

	archiveTx := adminDB.WithContext(ctx).Begin()
	require.NoError(t, archiveTx.Error)
	defer func() { _ = archiveTx.Rollback().Error }()
	require.NoError(t, archiveTx.Exec(`SELECT set_config('app.tx_mode', 'system', true)`).Error)
	require.NoError(t, archiveTx.Exec(`
		UPDATE teams
		SET status = 'archived',
		    deleted_at = now(),
		    updated_at = now()
		WHERE id = ?::uuid
	`, raceTeam).Error)

	raceRefresh := refresh
	raceRefresh.TeamID = raceTeam
	raceRefresh.CorrelationID = "corr-dream-control-race"
	_, err = repo.RefreshTeamHypothesisStaleness(ctx, raceRefresh)
	require.ErrorContains(t, err, "lock timeout")
	require.NoError(t, archiveTx.Commit().Error)
	require.NoError(t, appDB.Exec(`SET lock_timeout = DEFAULT`).Error)

	_, err = repo.RefreshTeamHypothesisStaleness(ctx, raceRefresh)
	require.ErrorIs(t, err, ErrTeamInactive)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var proposed, auditCount int
		if err := tx.Raw(`
			SELECT count(*)
			FROM hypotheses
			WHERE team_id = ?::uuid
			  AND hypothesis_id = ?::uuid
			  AND status = 'proposed'
		`, raceTeam, raceHypothesis).Scan(&proposed).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT count(*)
			FROM audit_log
			WHERE team_id = ?::uuid
			  AND operation = 'dream_staleness_refreshed'
		`, raceTeam).Scan(&auditCount).Error; err != nil {
			return err
		}
		assert.Equal(t, 1, proposed)
		assert.Zero(t, auditCount)
		return nil
	}))
}

func assertDreamInput(t *testing.T, inputs []DreamInput, relationshipID, status string) {
	t.Helper()
	for _, input := range inputs {
		if input.RelationshipID == relationshipID {
			assert.Equal(t, status, input.Status)
			return
		}
	}
	t.Fatalf("missing dream input %s in %+v", relationshipID, inputs)
}

func assertDreamInputMissing(t *testing.T, inputs []DreamInput, relationshipID string) {
	t.Helper()
	for _, input := range inputs {
		if input.RelationshipID == relationshipID {
			t.Fatalf("unexpected dream input %s in %+v", relationshipID, inputs)
		}
	}
}
