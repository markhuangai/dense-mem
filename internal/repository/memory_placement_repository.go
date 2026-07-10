package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type MemoryPlacementRepository interface {
	CreateRun(ctx context.Context, run domain.MemoryPlacementRun) error
	GetRun(ctx context.Context, profileID, ingestID string) (*domain.MemoryPlacementRun, error)
	ClaimNextQueuedRun(ctx context.Context) (*domain.MemoryPlacementRun, error)
	SaveRun(ctx context.Context, run domain.MemoryPlacementRun) error
	SaveRunWithTransitions(ctx context.Context, run domain.MemoryPlacementRun, events []domain.AssertionTransitionEvent) error
	AppendTransitionEvents(ctx context.Context, events []domain.AssertionTransitionEvent) error
	CreateDispute(ctx context.Context, session domain.MemoryDisputeSession) error
	GetDispute(ctx context.Context, profileID, disputeID string) (*domain.MemoryDisputeSession, error)
	SaveDispute(ctx context.Context, session domain.MemoryDisputeSession) error
	CreateDisputeAndSaveRun(ctx context.Context, session domain.MemoryDisputeSession, run domain.MemoryPlacementRun) error
	UpdateDisputeWithRun(ctx context.Context, profileID, disputeID string, update DisputeRunUpdate) (*domain.MemoryDisputeSession, *domain.MemoryPlacementRun, error)
}

type DisputeRunUpdate func(session *domain.MemoryDisputeSession, run *domain.MemoryPlacementRun) error

type MemoryPlacementRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ MemoryPlacementRepository = (*MemoryPlacementRepositoryImpl)(nil)

func NewMemoryPlacementRepository(db *gorm.DB, rls postgres.RLSHelper) *MemoryPlacementRepositoryImpl {
	return &MemoryPlacementRepositoryImpl{db: db, rls: rls}
}

func (r *MemoryPlacementRepositoryImpl) CreateRun(ctx context.Context, run domain.MemoryPlacementRun) error {
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return createRunTx(ctx, tx, run)
	})
	if err != nil {
		return fmt.Errorf("memory placement: create run: %w", err)
	}
	return nil
}

func (r *MemoryPlacementRepositoryImpl) GetRun(ctx context.Context, profileID, ingestID string) (*domain.MemoryPlacementRun, error) {
	profileID = strings.TrimSpace(profileID)
	ingestID = strings.TrimSpace(ingestID)
	if profileID == "" || ingestID == "" {
		return nil, nil
	}
	var run *domain.MemoryPlacementRun
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		loaded, err := readPlacementRun(ctx, tx, "WHERE profile_id = ? AND ingest_id = ?", profileID, ingestID)
		if err != nil {
			return err
		}
		run = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("memory placement: get run: %w", err)
	}
	return run, nil
}

func (r *MemoryPlacementRepositoryImpl) ClaimNextQueuedRun(ctx context.Context) (*domain.MemoryPlacementRun, error) {
	var ingestID string
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			WITH next AS (
				SELECT ingest_id
				FROM memory_placement_runs
				WHERE status = 'queued'
				   OR (
						status = 'processing'
						AND started_at IS NOT NULL
						AND updated_at < now() - interval '5 minutes'
				   )
				ORDER BY
					CASE WHEN status = 'queued' THEN 0 ELSE 1 END,
					created_at ASC
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			)
			UPDATE memory_placement_runs AS run
			SET status = 'processing',
			    started_at = now(),
			    updated_at = now()
			FROM next
			WHERE run.ingest_id = next.ingest_id
			RETURNING run.ingest_id::text
		`).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			if err := rows.Scan(&ingestID); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("memory placement: claim queued run: %w", err)
	}
	if ingestID == "" {
		return nil, nil
	}

	var run *domain.MemoryPlacementRun
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		loaded, err := readPlacementRun(ctx, tx, "WHERE ingest_id = ?", ingestID)
		if err != nil {
			return err
		}
		run = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("memory placement: load claimed run: %w", err)
	}
	return run, nil
}

func (r *MemoryPlacementRepositoryImpl) SaveRun(ctx context.Context, run domain.MemoryPlacementRun) error {
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return saveRunTx(ctx, tx, run)
	})
	if err != nil {
		return fmt.Errorf("memory placement: save run: %w", err)
	}
	return nil
}

func (r *MemoryPlacementRepositoryImpl) SaveRunWithTransitions(ctx context.Context, run domain.MemoryPlacementRun, events []domain.AssertionTransitionEvent) error {
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := saveRunTx(ctx, tx, run); err != nil {
			return err
		}
		return appendTransitionEventsTx(ctx, tx, events)
	})
	if err != nil {
		return fmt.Errorf("memory placement: save run with transitions: %w", err)
	}
	return nil
}

func (r *MemoryPlacementRepositoryImpl) AppendTransitionEvents(ctx context.Context, events []domain.AssertionTransitionEvent) error {
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return appendTransitionEventsTx(ctx, tx, events)
	})
	if err != nil {
		return fmt.Errorf("memory placement: append assertion transitions: %w", err)
	}
	return nil
}

func (r *MemoryPlacementRepositoryImpl) CountAssertionTransitions(ctx context.Context, teamID, actorProfileID string, from, to time.Time) (map[string]int64, error) {
	teamID = strings.TrimSpace(teamID)
	actorProfileID = strings.TrimSpace(actorProfileID)
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return nil, errors.New("assertion transition count requires a valid time window")
	}
	for name, value := range map[string]string{"team_id": teamID, "actor_profile_id": actorProfileID} {
		if value == "" {
			continue
		}
		if _, err := uuid.Parse(value); err != nil {
			return nil, fmt.Errorf("assertion transition count: invalid %s: %w", name, err)
		}
	}

	counts := map[string]int64{}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			WITH filters AS (
				SELECT NULLIF(?, '')::uuid AS team_id,
				       NULLIF(?, '')::uuid AS actor_profile_id
			)
			SELECT events.event_type, COUNT(*)::bigint
			FROM assertion_transition_events AS events
			LEFT JOIN memory_placement_runs AS runs ON runs.ingest_id = events.ingest_id
			CROSS JOIN filters
			WHERE events.occurred_at >= ?
			  AND events.occurred_at <= ?
			  AND (filters.team_id IS NULL OR events.profile_id = filters.team_id)
			  AND (filters.actor_profile_id IS NULL OR runs.actor_profile_id = filters.actor_profile_id)
			GROUP BY events.event_type
		`, teamID, actorProfileID, from.UTC(), to.UTC()).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var eventType string
			var count int64
			if err := rows.Scan(&eventType, &count); err != nil {
				return err
			}
			counts[eventType] = count
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("memory placement: count assertion transitions: %w", err)
	}
	return counts, nil
}

func (r *MemoryPlacementRepositoryImpl) CreateDispute(ctx context.Context, session domain.MemoryDisputeSession) error {
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return createDisputeTx(ctx, tx, session)
	})
	if err != nil {
		return fmt.Errorf("memory dispute: create session: %w", err)
	}
	return nil
}

func (r *MemoryPlacementRepositoryImpl) GetDispute(ctx context.Context, profileID, disputeID string) (*domain.MemoryDisputeSession, error) {
	profileID = strings.TrimSpace(profileID)
	disputeID = strings.TrimSpace(disputeID)
	if profileID == "" || disputeID == "" {
		return nil, nil
	}
	var session *domain.MemoryDisputeSession
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		loaded, err := readDisputeSession(ctx, tx, "WHERE profile_id = ? AND dispute_id = ? LIMIT 1", profileID, disputeID)
		if err != nil {
			return err
		}
		session = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("memory dispute: get session: %w", err)
	}
	return session, nil
}

func (r *MemoryPlacementRepositoryImpl) SaveDispute(ctx context.Context, session domain.MemoryDisputeSession) error {
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return saveDisputeTx(ctx, tx, session)
	})
	if err != nil {
		return fmt.Errorf("memory dispute: save session: %w", err)
	}
	return nil
}

func (r *MemoryPlacementRepositoryImpl) CreateDisputeAndSaveRun(ctx context.Context, session domain.MemoryDisputeSession, run domain.MemoryPlacementRun) error {
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := createDisputeTx(ctx, tx, session); err != nil {
			return err
		}
		return saveRunTx(ctx, tx, run)
	})
	if err != nil {
		return fmt.Errorf("memory dispute: create session with placement update: %w", err)
	}
	return nil
}

func (r *MemoryPlacementRepositoryImpl) UpdateDisputeWithRun(ctx context.Context, profileID, disputeID string, update DisputeRunUpdate) (*domain.MemoryDisputeSession, *domain.MemoryPlacementRun, error) {
	profileID = strings.TrimSpace(profileID)
	disputeID = strings.TrimSpace(disputeID)
	if profileID == "" || disputeID == "" {
		return nil, nil, nil
	}

	var session *domain.MemoryDisputeSession
	var run *domain.MemoryPlacementRun
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		loadedSession, err := readDisputeSession(ctx, tx, "WHERE profile_id = ? AND dispute_id = ? LIMIT 1 FOR UPDATE", profileID, disputeID)
		if err != nil || loadedSession == nil {
			session = loadedSession
			return err
		}
		loadedRun, err := readPlacementRunLocked(ctx, tx, "WHERE profile_id = ? AND ingest_id = ?", profileID, loadedSession.IngestID)
		if err != nil || loadedRun == nil {
			session = loadedSession
			run = loadedRun
			return err
		}
		if update != nil {
			if err := update(loadedSession, loadedRun); err != nil {
				return err
			}
		}
		if err := saveDisputeTx(ctx, tx, *loadedSession); err != nil {
			return err
		}
		if err := saveRunTx(ctx, tx, *loadedRun); err != nil {
			return err
		}
		session = loadedSession
		run = loadedRun
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("memory dispute: update session with placement: %w", err)
	}
	return session, run, nil
}

func (r *MemoryPlacementRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls != nil {
		return r.rls.WithSystemTx(ctx, r.db, fn)
	}
	return r.db.WithContext(ctx).Transaction(fn)
}

const createMemoryPlacementRunSQL = `
	INSERT INTO memory_placement_runs (
		ingest_id, profile_id, actor_profile_id, actor_role, status, check_after_seconds, status_tool,
		pipeline_version, evidence, proposal, review_tasks, security, migration_refs,
		requires_acknowledgement, error, created_at, updated_at, started_at,
		completed_at, acknowledged_at
	) VALUES (
		?, ?, nullif(?, '')::uuid, ?, ?, ?, ?, ?, ?::jsonb, ?::jsonb, ?::jsonb, ?::jsonb, ?::jsonb,
		?, ?, ?, ?, ?, ?, ?
	)`

const createMemoryPlacementItemSQL = `
	INSERT INTO memory_placement_items (
		item_id, ingest_id, profile_id, evidence_index, fragment_id,
		evidence_indexes, fragment_ids, category, status, reason, error,
		claim_id, fact_id, assertion_id, relationship_type, tier,
		assertion_status, policy_family, verifier_verdict, verifier_confidence,
		review_task_id, proposed_relationship, reviewed_relationship,
		security_signals, created_at, updated_at
	) VALUES (
		?, ?, ?, ?, ?, ?::jsonb, ?::jsonb, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		?, ?, ?, ?, ?, ?::jsonb, ?::jsonb, ?::jsonb, ?, ?
	)`

func createRunTx(ctx context.Context, tx *gorm.DB, run domain.MemoryPlacementRun) error {
	evidence, err := json.Marshal(nonNilEvidence(run.Evidence))
	if err != nil {
		return fmt.Errorf("memory placement: marshal evidence: %w", err)
	}
	proposal, reviewTasks, security, migrationRefs, err := marshalPlacementRunV2(run)
	if err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(createMemoryPlacementRunSQL,
		run.IngestID,
		run.ProfileID,
		run.ActorProfileID,
		run.ActorRole,
		string(run.Status),
		run.CheckAfterSeconds,
		run.StatusTool,
		run.PipelineVersion,
		string(evidence),
		proposal,
		reviewTasks,
		security,
		migrationRefs,
		run.RequiresAck,
		run.Error,
		utcOrNow(run.CreatedAt),
		utcOrNow(run.UpdatedAt),
		timePtrValue(run.StartedAt),
		timePtrValue(run.CompletedAt),
		timePtrValue(run.AcknowledgedAt),
	).Error; err != nil {
		return err
	}
	for _, item := range run.Items {
		itemJSON, err := marshalPlacementItemV2(item)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(createMemoryPlacementItemSQL,
			item.ItemID,
			run.IngestID,
			run.ProfileID,
			item.EvidenceIndex,
			item.FragmentID,
			itemJSON.evidenceIndexes,
			itemJSON.fragmentIDs,
			string(item.Category),
			item.Status,
			item.Reason,
			item.Error,
			item.ClaimID,
			item.FactID,
			item.AssertionID,
			item.RelationshipType,
			string(item.Tier),
			string(item.AssertionStatus),
			string(item.PolicyFamily),
			item.VerifierVerdict,
			item.VerifierConfidence,
			item.ReviewTaskID,
			itemJSON.proposed,
			itemJSON.reviewed,
			itemJSON.securitySignals,
			utcOrNow(item.CreatedAt),
			utcOrNow(item.UpdatedAt),
		).Error; err != nil {
			return err
		}
	}
	return nil
}

func saveRunTx(ctx context.Context, tx *gorm.DB, run domain.MemoryPlacementRun) error {
	evidence, err := json.Marshal(nonNilEvidence(run.Evidence))
	if err != nil {
		return fmt.Errorf("memory placement: marshal evidence: %w", err)
	}
	proposal, reviewTasks, security, migrationRefs, err := marshalPlacementRunV2(run)
	if err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(`
		UPDATE memory_placement_runs
		SET actor_profile_id = nullif(?, '')::uuid, actor_role = ?, status = ?, check_after_seconds = ?, status_tool = ?, pipeline_version = ?,
		    evidence = ?::jsonb, proposal = ?::jsonb, review_tasks = ?::jsonb,
		    security = ?::jsonb, migration_refs = ?::jsonb, requires_acknowledgement = ?, error = ?, updated_at = ?,
		    started_at = ?, completed_at = ?, acknowledged_at = ?
		WHERE ingest_id = ? AND profile_id = ?
	`,
		run.ActorProfileID,
		run.ActorRole,
		string(run.Status),
		run.CheckAfterSeconds,
		run.StatusTool,
		run.PipelineVersion,
		string(evidence),
		proposal,
		reviewTasks,
		security,
		migrationRefs,
		run.RequiresAck,
		run.Error,
		utcOrNow(run.UpdatedAt),
		timePtrValue(run.StartedAt),
		timePtrValue(run.CompletedAt),
		timePtrValue(run.AcknowledgedAt),
		run.IngestID,
		run.ProfileID,
	).Error; err != nil {
		return err
	}
	// Placement items are the current materialized result for a run; transition history lives in assertion_transition_events.
	if err := tx.WithContext(ctx).Exec(`
		DELETE FROM memory_placement_items
		WHERE ingest_id = ? AND profile_id = ?
	`, run.IngestID, run.ProfileID).Error; err != nil {
		return err
	}
	for _, item := range run.Items {
		itemJSON, err := marshalPlacementItemV2(item)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(createMemoryPlacementItemSQL,
			item.ItemID,
			run.IngestID,
			run.ProfileID,
			item.EvidenceIndex,
			item.FragmentID,
			itemJSON.evidenceIndexes,
			itemJSON.fragmentIDs,
			string(item.Category),
			item.Status,
			item.Reason,
			item.Error,
			item.ClaimID,
			item.FactID,
			item.AssertionID,
			item.RelationshipType,
			string(item.Tier),
			string(item.AssertionStatus),
			string(item.PolicyFamily),
			item.VerifierVerdict,
			item.VerifierConfidence,
			item.ReviewTaskID,
			itemJSON.proposed,
			itemJSON.reviewed,
			itemJSON.securitySignals,
			utcOrNow(item.CreatedAt),
			utcOrNow(item.UpdatedAt),
		).Error; err != nil {
			return err
		}
	}
	return nil
}

func createDisputeTx(ctx context.Context, tx *gorm.DB, session domain.MemoryDisputeSession) error {
	turns, err := json.Marshal(nonNilTurns(session.Turns))
	if err != nil {
		return fmt.Errorf("memory dispute: marshal turns: %w", err)
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO memory_dispute_sessions (
			dispute_id, profile_id, ingest_id, placement_item_id,
			status, turns, final_reason, created_at, updated_at, completed_at
		) VALUES (
			?, ?, ?, nullif(?, '')::uuid,
			?, ?::jsonb, ?, ?, ?, ?
		)
	`,
		session.DisputeID,
		session.ProfileID,
		session.IngestID,
		session.PlacementItemID,
		string(session.Status),
		string(turns),
		session.FinalReason,
		utcOrNow(session.CreatedAt),
		utcOrNow(session.UpdatedAt),
		timePtrValue(session.CompletedAt),
	).Error
}

func saveDisputeTx(ctx context.Context, tx *gorm.DB, session domain.MemoryDisputeSession) error {
	turns, err := json.Marshal(nonNilTurns(session.Turns))
	if err != nil {
		return fmt.Errorf("memory dispute: marshal turns: %w", err)
	}
	return tx.WithContext(ctx).Exec(`
		UPDATE memory_dispute_sessions
		SET status = ?, turns = ?::jsonb, final_reason = ?,
		    updated_at = ?, completed_at = ?
		WHERE dispute_id = ? AND profile_id = ?
	`,
		string(session.Status),
		string(turns),
		session.FinalReason,
		utcOrNow(session.UpdatedAt),
		timePtrValue(session.CompletedAt),
		session.DisputeID,
		session.ProfileID,
	).Error
}

func readPlacementRun(ctx context.Context, tx *gorm.DB, where string, args ...any) (*domain.MemoryPlacementRun, error) {
	return readPlacementRunWithLock(ctx, tx, where, false, args...)
}

func readPlacementRunLocked(ctx context.Context, tx *gorm.DB, where string, args ...any) (*domain.MemoryPlacementRun, error) {
	return readPlacementRunWithLock(ctx, tx, where, true, args...)
}

func readPlacementRunWithLock(ctx context.Context, tx *gorm.DB, where string, lock bool, args ...any) (*domain.MemoryPlacementRun, error) {
	run, found, err := scanSinglePlacementRun(ctx, tx, where, lock, args...)
	if err != nil || !found {
		return nil, err
	}
	items, err := readPlacementItemsWithLock(ctx, tx, run.IngestID, lock)
	if err != nil {
		return nil, err
	}
	run.Items = items
	return &run, nil
}

func scanSinglePlacementRun(ctx context.Context, tx *gorm.DB, where string, lock bool, args ...any) (domain.MemoryPlacementRun, bool, error) {
	lockClause := ""
	if lock {
		lockClause = " FOR UPDATE"
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT ingest_id::text, profile_id::text, COALESCE(actor_profile_id::text, ''), actor_role, status, check_after_seconds,
		       status_tool, pipeline_version, evidence, proposal, review_tasks,
		       security, migration_refs, requires_acknowledgement, error, created_at, updated_at,
		       started_at, completed_at, acknowledged_at
		FROM memory_placement_runs
		`+where+`
		LIMIT 1`+lockClause+`
	`, args...).Rows()
	if err != nil {
		return domain.MemoryPlacementRun{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.MemoryPlacementRun{}, false, err
		}
		return domain.MemoryPlacementRun{}, false, nil
	}
	run, err := scanPlacementRun(rows)
	if err != nil {
		return domain.MemoryPlacementRun{}, false, err
	}
	if err := rows.Err(); err != nil {
		return domain.MemoryPlacementRun{}, false, err
	}
	return run, true, nil
}

func readPlacementItemsWithLock(ctx context.Context, tx *gorm.DB, ingestID string, lock bool) ([]domain.MemoryPlacementItem, error) {
	lockClause := ""
	if lock {
		lockClause = " FOR UPDATE"
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT item_id::text, ingest_id::text, profile_id::text, evidence_index,
		       fragment_id, evidence_indexes, fragment_ids, category, status, reason,
		       error, claim_id, fact_id, assertion_id, relationship_type, tier,
		       assertion_status, policy_family, verifier_verdict, verifier_confidence,
		       review_task_id, proposed_relationship, reviewed_relationship,
		       security_signals, created_at, updated_at
		FROM memory_placement_items
		WHERE ingest_id = ?
		ORDER BY evidence_index ASC, created_at ASC`+lockClause+`
	`, ingestID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.MemoryPlacementItem{}
	for rows.Next() {
		item, err := scanPlacementItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func readDisputeSession(ctx context.Context, tx *gorm.DB, clause string, args ...any) (*domain.MemoryDisputeSession, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT dispute_id::text, profile_id::text, ingest_id::text,
		       COALESCE(placement_item_id::text, ''), status, turns,
		       final_reason, created_at, updated_at, completed_at
		FROM memory_dispute_sessions
		`+clause+`
	`, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	session, err := scanDisputeSession(rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &session, nil
}

func scanPlacementRun(rows *sql.Rows) (domain.MemoryPlacementRun, error) {
	var (
		run          domain.MemoryPlacementRun
		status       string
		evidenceRaw  []byte
		proposalRaw  []byte
		reviewRaw    []byte
		securityRaw  []byte
		migrationRaw []byte
		startedAt    sql.NullTime
		completedAt  sql.NullTime
		ackAt        sql.NullTime
	)
	if err := rows.Scan(
		&run.IngestID,
		&run.ProfileID,
		&run.ActorProfileID,
		&run.ActorRole,
		&status,
		&run.CheckAfterSeconds,
		&run.StatusTool,
		&run.PipelineVersion,
		&evidenceRaw,
		&proposalRaw,
		&reviewRaw,
		&securityRaw,
		&migrationRaw,
		&run.RequiresAck,
		&run.Error,
		&run.CreatedAt,
		&run.UpdatedAt,
		&startedAt,
		&completedAt,
		&ackAt,
	); err != nil {
		return domain.MemoryPlacementRun{}, err
	}
	run.Status = domain.MemoryPlacementRunStatus(status)
	if len(evidenceRaw) > 0 {
		if err := json.Unmarshal(evidenceRaw, &run.Evidence); err != nil {
			return domain.MemoryPlacementRun{}, fmt.Errorf("invalid memory placement evidence JSON: %w", err)
		}
	}
	if err := unmarshalPlacementRunV2(proposalRaw, reviewRaw, securityRaw, migrationRaw, &run); err != nil {
		return domain.MemoryPlacementRun{}, err
	}
	run.StartedAt = nullableTime(startedAt)
	run.CompletedAt = nullableTime(completedAt)
	run.AcknowledgedAt = nullableTime(ackAt)
	return run, nil
}

func scanPlacementItem(rows *sql.Rows) (domain.MemoryPlacementItem, error) {
	var item domain.MemoryPlacementItem
	var category string
	var tier, assertionStatus, policyFamily string
	var evidenceIndexesRaw, fragmentIDsRaw, proposedRaw, reviewedRaw, securityRaw []byte
	if err := rows.Scan(
		&item.ItemID,
		&item.IngestID,
		&item.ProfileID,
		&item.EvidenceIndex,
		&item.FragmentID,
		&evidenceIndexesRaw,
		&fragmentIDsRaw,
		&category,
		&item.Status,
		&item.Reason,
		&item.Error,
		&item.ClaimID,
		&item.FactID,
		&item.AssertionID,
		&item.RelationshipType,
		&tier,
		&assertionStatus,
		&policyFamily,
		&item.VerifierVerdict,
		&item.VerifierConfidence,
		&item.ReviewTaskID,
		&proposedRaw,
		&reviewedRaw,
		&securityRaw,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return domain.MemoryPlacementItem{}, err
	}
	item.Category = domain.MemoryPlacementCategory(category)
	item.Tier = domain.AssertionTier(tier)
	item.AssertionStatus = domain.AssertionStatus(assertionStatus)
	item.PolicyFamily = domain.AssertionPolicyFamily(policyFamily)
	if err := unmarshalPlacementItemV2(evidenceIndexesRaw, fragmentIDsRaw, proposedRaw, reviewedRaw, securityRaw, &item); err != nil {
		return domain.MemoryPlacementItem{}, err
	}
	return item, nil
}

func scanDisputeSession(rows *sql.Rows) (domain.MemoryDisputeSession, error) {
	var (
		session     domain.MemoryDisputeSession
		status      string
		turnsRaw    []byte
		completedAt sql.NullTime
	)
	if err := rows.Scan(
		&session.DisputeID,
		&session.ProfileID,
		&session.IngestID,
		&session.PlacementItemID,
		&status,
		&turnsRaw,
		&session.FinalReason,
		&session.CreatedAt,
		&session.UpdatedAt,
		&completedAt,
	); err != nil {
		return domain.MemoryDisputeSession{}, err
	}
	session.Status = domain.MemoryDisputeStatus(status)
	if len(turnsRaw) > 0 {
		if err := json.Unmarshal(turnsRaw, &session.Turns); err != nil {
			return domain.MemoryDisputeSession{}, fmt.Errorf("invalid memory dispute turns JSON: %w", err)
		}
	}
	session.CompletedAt = nullableTime(completedAt)
	return session, nil
}

func utcOrNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func nullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}

func nonNilEvidence(value []domain.MemoryEvidence) []domain.MemoryEvidence {
	if value == nil {
		return []domain.MemoryEvidence{}
	}
	return value
}

func nonNilTurns(value []domain.MemoryDisputeTurn) []domain.MemoryDisputeTurn {
	if value == nil {
		return []domain.MemoryDisputeTurn{}
	}
	return value
}

type placementItemJSON struct {
	evidenceIndexes string
	fragmentIDs     string
	proposed        string
	reviewed        string
	securitySignals string
}

func marshalPlacementRunV2(run domain.MemoryPlacementRun) (string, string, string, string, error) {
	proposal, err := json.Marshal(run.Proposal)
	if err != nil {
		return "", "", "", "", fmt.Errorf("memory placement: marshal proposal: %w", err)
	}
	reviewTasks := run.ReviewTasks
	if reviewTasks == nil {
		reviewTasks = []domain.MemoryReviewTask{}
	}
	review, err := json.Marshal(reviewTasks)
	if err != nil {
		return "", "", "", "", fmt.Errorf("memory placement: marshal review tasks: %w", err)
	}
	security, err := json.Marshal(run.Security)
	if err != nil {
		return "", "", "", "", fmt.Errorf("memory placement: marshal security: %w", err)
	}
	migrationRefs := run.MigrationRefs
	if migrationRefs == nil {
		migrationRefs = []domain.LegacyMemoryRef{}
	}
	migration, err := json.Marshal(migrationRefs)
	if err != nil {
		return "", "", "", "", fmt.Errorf("memory placement: marshal migration refs: %w", err)
	}
	return string(proposal), string(review), string(security), string(migration), nil
}

func marshalPlacementItemV2(item domain.MemoryPlacementItem) (placementItemJSON, error) {
	values := []struct {
		name  string
		value any
	}{
		{"evidence indexes", nonNilInts(item.EvidenceIndexes)},
		{"fragment ids", nonNilStrings(item.FragmentIDs)},
		{"proposed relationship", item.ProposedRelationship},
		{"reviewed relationship", item.ReviewedRelationship},
		{"security signals", nonNilStrings(item.SecuritySignals)},
	}
	encoded := make([]string, len(values))
	for i, value := range values {
		data, err := json.Marshal(value.value)
		if err != nil {
			return placementItemJSON{}, fmt.Errorf("memory placement: marshal %s: %w", value.name, err)
		}
		encoded[i] = string(data)
	}
	return placementItemJSON{
		evidenceIndexes: encoded[0],
		fragmentIDs:     encoded[1],
		proposed:        encoded[2],
		reviewed:        encoded[3],
		securitySignals: encoded[4],
	}, nil
}

func unmarshalPlacementRunV2(proposalRaw, reviewRaw, securityRaw, migrationRaw []byte, run *domain.MemoryPlacementRun) error {
	if len(proposalRaw) > 0 {
		if err := json.Unmarshal(proposalRaw, &run.Proposal); err != nil {
			return fmt.Errorf("invalid memory placement proposal JSON: %w", err)
		}
	}
	if len(reviewRaw) > 0 {
		if err := json.Unmarshal(reviewRaw, &run.ReviewTasks); err != nil {
			return fmt.Errorf("invalid memory placement review tasks JSON: %w", err)
		}
	}
	if len(securityRaw) > 0 {
		if err := json.Unmarshal(securityRaw, &run.Security); err != nil {
			return fmt.Errorf("invalid memory placement security JSON: %w", err)
		}
	}
	if len(migrationRaw) > 0 {
		if err := json.Unmarshal(migrationRaw, &run.MigrationRefs); err != nil {
			return fmt.Errorf("invalid memory placement migration refs JSON: %w", err)
		}
	}
	return nil
}

func unmarshalPlacementItemV2(evidenceIndexesRaw, fragmentIDsRaw, proposedRaw, reviewedRaw, securityRaw []byte, item *domain.MemoryPlacementItem) error {
	values := []struct {
		name string
		raw  []byte
		out  any
	}{
		{"evidence indexes", evidenceIndexesRaw, &item.EvidenceIndexes},
		{"fragment ids", fragmentIDsRaw, &item.FragmentIDs},
		{"proposed relationship", proposedRaw, &item.ProposedRelationship},
		{"reviewed relationship", reviewedRaw, &item.ReviewedRelationship},
		{"security signals", securityRaw, &item.SecuritySignals},
	}
	for _, value := range values {
		if len(value.raw) == 0 {
			continue
		}
		if err := json.Unmarshal(value.raw, value.out); err != nil {
			return fmt.Errorf("invalid memory placement %s JSON: %w", value.name, err)
		}
	}
	return nil
}

func appendTransitionEventsTx(ctx context.Context, tx *gorm.DB, events []domain.AssertionTransitionEvent) error {
	for i, event := range events {
		if strings.TrimSpace(event.EventID) == "" || strings.TrimSpace(event.ProfileID) == "" ||
			strings.TrimSpace(event.EventType) == "" || strings.TrimSpace(event.ReasonCode) == "" || strings.TrimSpace(event.Source) == "" {
			return fmt.Errorf("assertion transition[%d] requires event_id, team_id, event_type, reason_code, and source", i)
		}
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO assertion_transition_events (
				event_id, profile_id, ingest_id, placement_item_id, assertion_id,
				event_type, from_tier, to_tier, from_status, to_status,
				reason_code, source, occurred_at
			) VALUES (
				?, ?, nullif(?, '')::uuid, nullif(?, '')::uuid, ?,
				?, ?, ?, ?, ?, ?, ?, ?
			)
			ON CONFLICT (event_id) DO NOTHING
		`,
			event.EventID,
			event.ProfileID,
			event.IngestID,
			event.ItemID,
			event.AssertionID,
			event.EventType,
			string(event.FromTier),
			string(event.ToTier),
			string(event.FromStatus),
			string(event.ToStatus),
			event.ReasonCode,
			event.Source,
			utcOrNow(event.OccurredAt),
		).Error; err != nil {
			return err
		}
	}
	return nil
}

func nonNilInts(value []int) []int {
	if value == nil {
		return []int{}
	}
	return value
}

func nonNilStrings(value []string) []string {
	if value == nil {
		return []string{}
	}
	return value
}
