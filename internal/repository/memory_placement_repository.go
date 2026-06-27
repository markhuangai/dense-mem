package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type MemoryPlacementRepository interface {
	CreateRun(ctx context.Context, run domain.MemoryPlacementRun) error
	GetRun(ctx context.Context, profileID, ingestID string) (*domain.MemoryPlacementRun, error)
	ClaimNextQueuedRun(ctx context.Context) (*domain.MemoryPlacementRun, error)
	SaveRun(ctx context.Context, run domain.MemoryPlacementRun) error
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
				   OR (status = 'processing' AND updated_at < now() - interval '5 minutes')
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

func createRunTx(ctx context.Context, tx *gorm.DB, run domain.MemoryPlacementRun) error {
	evidence, err := json.Marshal(nonNilEvidence(run.Evidence))
	if err != nil {
		return fmt.Errorf("memory placement: marshal evidence: %w", err)
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO memory_placement_runs (
			ingest_id, profile_id, status, check_after_seconds, status_tool,
			evidence, error, created_at, updated_at, started_at, completed_at
		) VALUES (
			?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?
		)
	`,
		run.IngestID,
		run.ProfileID,
		string(run.Status),
		run.CheckAfterSeconds,
		run.StatusTool,
		string(evidence),
		run.Error,
		utcOrNow(run.CreatedAt),
		utcOrNow(run.UpdatedAt),
		timePtrValue(run.StartedAt),
		timePtrValue(run.CompletedAt),
	).Error; err != nil {
		return err
	}
	for _, item := range run.Items {
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO memory_placement_items (
				item_id, ingest_id, profile_id, evidence_index, fragment_id,
				category, status, reason, error, claim_id, fact_id, created_at, updated_at
			) VALUES (
				?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			)
		`,
			item.ItemID,
			run.IngestID,
			run.ProfileID,
			item.EvidenceIndex,
			item.FragmentID,
			string(item.Category),
			item.Status,
			item.Reason,
			item.Error,
			item.ClaimID,
			item.FactID,
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
	if err := tx.WithContext(ctx).Exec(`
		UPDATE memory_placement_runs
		SET status = ?, check_after_seconds = ?, status_tool = ?,
		    evidence = ?::jsonb, error = ?, updated_at = ?,
		    started_at = ?, completed_at = ?
		WHERE ingest_id = ? AND profile_id = ?
	`,
		string(run.Status),
		run.CheckAfterSeconds,
		run.StatusTool,
		string(evidence),
		run.Error,
		utcOrNow(run.UpdatedAt),
		timePtrValue(run.StartedAt),
		timePtrValue(run.CompletedAt),
		run.IngestID,
		run.ProfileID,
	).Error; err != nil {
		return err
	}
	for _, item := range run.Items {
		if err := tx.WithContext(ctx).Exec(`
			UPDATE memory_placement_items
			SET fragment_id = ?, category = ?, status = ?, reason = ?, error = ?,
			    claim_id = ?, fact_id = ?, updated_at = ?
			WHERE item_id = ? AND ingest_id = ? AND profile_id = ?
		`,
			item.FragmentID,
			string(item.Category),
			item.Status,
			item.Reason,
			item.Error,
			item.ClaimID,
			item.FactID,
			utcOrNow(item.UpdatedAt),
			item.ItemID,
			run.IngestID,
			run.ProfileID,
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
	lockClause := ""
	if lock {
		lockClause = " FOR UPDATE"
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT ingest_id::text, profile_id::text, status, check_after_seconds,
		       status_tool, evidence, error, created_at, updated_at,
		       started_at, completed_at
		FROM memory_placement_runs
		`+where+`
		LIMIT 1`+lockClause+`
	`, args...).Rows()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	run, err := scanPlacementRun(rows)
	if err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	items, err := readPlacementItemsWithLock(ctx, tx, run.IngestID, lock)
	if err != nil {
		return nil, err
	}
	run.Items = items
	return &run, nil
}

func readPlacementItems(ctx context.Context, tx *gorm.DB, ingestID string) ([]domain.MemoryPlacementItem, error) {
	return readPlacementItemsWithLock(ctx, tx, ingestID, false)
}

func readPlacementItemsWithLock(ctx context.Context, tx *gorm.DB, ingestID string, lock bool) ([]domain.MemoryPlacementItem, error) {
	lockClause := ""
	if lock {
		lockClause = " FOR UPDATE"
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT item_id::text, ingest_id::text, profile_id::text, evidence_index,
		       fragment_id, category, status, reason, error, claim_id, fact_id,
		       created_at, updated_at
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
		run         domain.MemoryPlacementRun
		status      string
		evidenceRaw []byte
		startedAt   sql.NullTime
		completedAt sql.NullTime
	)
	if err := rows.Scan(
		&run.IngestID,
		&run.ProfileID,
		&status,
		&run.CheckAfterSeconds,
		&run.StatusTool,
		&evidenceRaw,
		&run.Error,
		&run.CreatedAt,
		&run.UpdatedAt,
		&startedAt,
		&completedAt,
	); err != nil {
		return domain.MemoryPlacementRun{}, err
	}
	run.Status = domain.MemoryPlacementRunStatus(status)
	if len(evidenceRaw) > 0 {
		if err := json.Unmarshal(evidenceRaw, &run.Evidence); err != nil {
			return domain.MemoryPlacementRun{}, fmt.Errorf("invalid memory placement evidence JSON: %w", err)
		}
	}
	run.StartedAt = nullableTime(startedAt)
	run.CompletedAt = nullableTime(completedAt)
	return run, nil
}

func scanPlacementItem(rows *sql.Rows) (domain.MemoryPlacementItem, error) {
	var item domain.MemoryPlacementItem
	var category string
	if err := rows.Scan(
		&item.ItemID,
		&item.IngestID,
		&item.ProfileID,
		&item.EvidenceIndex,
		&item.FragmentID,
		&category,
		&item.Status,
		&item.Reason,
		&item.Error,
		&item.ClaimID,
		&item.FactID,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return domain.MemoryPlacementItem{}, err
	}
	item.Category = domain.MemoryPlacementCategory(category)
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
