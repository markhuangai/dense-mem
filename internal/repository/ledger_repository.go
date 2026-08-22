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

var (
	ErrIdempotencyConflict       = errors.New("idempotency conflict")
	ErrPlacementLeaseConflict    = errors.New("placement lease conflict")
	ErrPlacementNotFound         = errors.New("placement not found")
	ErrSourceRevisionConflict    = errors.New("source revision conflict")
	ErrEvidenceLifecycleNotFound = errors.New("evidence lifecycle target not found")
	ErrEvidenceLifecycleConflict = errors.New("evidence lifecycle conflict")
	ErrTeamInactive              = errors.New("team is not active")
)

type LedgerRepository interface {
	CreateIngest(ctx context.Context, input CreateIngestInput) (*CreateIngestResult, error)
	GetPlacementRun(ctx context.Context, input GetPlacementRunInput) (*CreateIngestResult, error)
	AdvanceSourceRevision(ctx context.Context, input AdvanceSourceRevisionInput) (*SourceRevisionResult, error)
	AppendSecurityEvent(ctx context.Context, input SecurityEventInput) (string, error)
	AppendPlacementOutcome(ctx context.Context, input PlacementOutcomeInput) (string, error)
	ClaimNextPlacementRun(ctx context.Context, teamID string, workerID string, lease time.Duration) (*PlacementRun, error)
	FinishPlacementRun(ctx context.Context, teamID string, placementRunID string, workerID string, status string, message string) (*PlacementFirstDisposition, error)
}

type CreateIngestInput struct {
	TeamID            string
	OwnerProfileID    string
	SpaceID           string
	SpaceGeneration   int64
	IdempotencyKey    string
	RequestHash       string
	SourceSummary     string
	Status            string
	TelemetryRemember bool
	Proposal          map[string]any
	Metadata          map[string]any
	Evidence          []EvidenceInput
}

type GetPlacementRunInput struct {
	TeamID         string
	OwnerProfileID string
	IngestID       string
}

type EvidenceInput struct {
	Content                       string
	ContentHash                   string
	SourceType                    string
	Authority                     string
	SourceRef                     string
	SourceKey                     string
	SourceRevisionToken           string
	ExpectedPreviousRevisionToken string
	SourceRevisionContentHash     string
	SourceRevisionEnvelope        map[string]any
	SupersedesEvidenceIDs         []string
	IdempotencyKey                string
	Labels                        []string
	Metadata                      map[string]any
	InitialEvent                  *SecurityEventDraft
}

type SecurityEventDraft struct {
	EventKind string
	Decision  string
	Reason    string
	Signals   []SecuritySignalInput
	Metadata  map[string]any
}

type SecurityEventInput struct {
	TeamID         string
	OwnerProfileID string
	IngestID       string
	FragmentID     string
	SecurityEventDraft
}

type SecuritySignalInput struct {
	Kind      string
	Severity  string
	SpanStart int
	SpanEnd   int
	Quote     string
	Metadata  map[string]any
}

type CreateIngestResult struct {
	TeamID           string
	OwnerProfileID   string
	IngestID         string
	PlacementRunID   string
	Status           string
	CorrelationID    string
	Attempts         int
	MaxAttempts      int
	Existing         bool
	Proposal         map[string]any
	ContractVersion  string
	Evidence         []EvidenceFragment
	Items            []PlacementItem
	FirstDisposition *PlacementFirstDisposition
	// Status projection metadata is loaded for the owner-scoped public status
	// endpoint; it is not placement data and is never exposed directly.
	SubmittedAt         *time.Time
	NextAttemptAt       *time.Time
	StartedAt           *time.Time
	UpdatedAt           *time.Time
	CompletedAt         *time.Time
	QuarantineExpiresAt *time.Time
}

type EvidenceFragment struct {
	FragmentID            string
	EvidenceIndex         int
	Content               string
	ContentHash           string
	Authority             string
	SourceID              string
	SourceRevisionID      string
	SupersededEvidenceIDs []string
}

type PlacementRun struct {
	TeamID          string
	PlacementRunID  string
	IngestID        string
	OwnerProfileID  string
	SpaceID         string
	SpaceGeneration int64
	CorrelationID   string
	Status          string
	Attempts        int
	MaxAttempts     int
	LeaseUntil      *time.Time
}

type PlacementItem struct {
	PlacementItemID, FragmentID string
	ClaimKey                    string
	EvidenceIndex               int
	Status, Category            string
	Version                     int
	Result                      map[string]any
}

// Keep the historical package-local name for tests and constructors while
// using the canonical storage contract as the single interface definition.
type rLSHelper = postgres.RLSHelper

type LedgerRepositoryImpl struct {
	db                      *gorm.DB
	rls                     rLSHelper
	embeddingJobMaxAttempts int
	conflictReviewTTLDays   int
	conflictReviewTimezone  string
}

var _ LedgerRepository = (*LedgerRepositoryImpl)(nil)

func NewLedgerRepository(db *gorm.DB, rls *postgres.RLS) *LedgerRepositoryImpl {
	return NewLedgerRepositoryWithEmbeddingJobMaxAttempts(db, rls, defaultEmbeddingJobMaxAttempts)
}

func NewLedgerRepositoryWithEmbeddingJobMaxAttempts(
	db *gorm.DB,
	rls *postgres.RLS,
	maxAttempts int,
) *LedgerRepositoryImpl {
	return NewLedgerRepositoryWithRuntimeConfig(db, rls, maxAttempts, ConflictRuntimeConfig{})
}

func NewLedgerRepositoryWithRuntimeConfig(
	db *gorm.DB,
	rls *postgres.RLS,
	maxAttempts int,
	conflictConfig ConflictRuntimeConfig,
) *LedgerRepositoryImpl {
	conflictConfig = normalizeConflictRuntimeConfig(conflictConfig)
	return &LedgerRepositoryImpl{
		db:                      db,
		rls:                     rls,
		embeddingJobMaxAttempts: normalizeEmbeddingJobMaxAttempts(maxAttempts),
		conflictReviewTTLDays:   conflictConfig.ReviewTTLDays,
		conflictReviewTimezone:  conflictConfig.Timezone,
	}
}

func (r *LedgerRepositoryImpl) CreateIngest(ctx context.Context, input CreateIngestInput) (*CreateIngestResult, error) {
	input = normalizeCreateIngestInput(input)
	if err := validateCreateIngestInput(input); err != nil {
		return nil, err
	}
	var result *CreateIngestResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if replay, err := loadDirectSupersessionReplay(ctx, tx, input); err != nil {
			return err
		} else if replay != nil {
			result = replay
			return nil
		}
		ingestID, created, err := insertKnowledgeIngest(ctx, tx, input)
		if err != nil {
			return err
		}
		if !created {
			if err := validateExistingIngestHash(ctx, tx, input, ingestID); err != nil {
				return err
			}
			loaded, err := loadCreateIngestResult(ctx, tx, input.TeamID, ingestID, true)
			if err != nil {
				return err
			}
			result = loaded
			return nil
		}
		placementRunID, createdAt, completedAt, err := insertPlacementRun(ctx, tx, input, ingestID)
		if err != nil {
			return err
		}
		evidence := make([]EvidenceFragment, 0, len(input.Evidence))
		items := make([]PlacementItem, 0, len(input.Evidence))
		sources := make(map[string]SourceRevisionResult)
		for i, item := range input.Evidence {
			var source *SourceRevisionResult
			if item.SourceKey != "" {
				advanced, err := advanceSourceRevisionInTx(ctx, tx, AdvanceSourceRevisionInput{
					TeamID:                        input.TeamID,
					OwnerProfileID:                input.OwnerProfileID,
					IngestID:                      ingestID,
					SpaceID:                       input.SpaceID,
					SpaceGeneration:               input.SpaceGeneration,
					SourceKey:                     item.SourceKey,
					SourceKind:                    sourceKindForEvidence(item.SourceType),
					Authority:                     item.Authority,
					RevisionToken:                 item.SourceRevisionToken,
					ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken,
					ContentHash:                   item.SourceRevisionContentHash,
					Envelope:                      item.SourceRevisionEnvelope,
				}, sources)
				if err != nil {
					return err
				}
				source = advanced
			}
			fragment, err := insertEvidenceFragment(ctx, tx, input, ingestID, i, item, source)
			if err != nil {
				return err
			}
			evidence = append(evidence, fragment)
			placementItem, err := insertPlacementItem(ctx, tx, input, ingestID, placementRunID, fragment, item)
			if err != nil {
				return err
			}
			items = append(items, placementItem)
			if item.InitialEvent != nil {
				eventInput := SecurityEventInput{
					TeamID:             input.TeamID,
					OwnerProfileID:     input.OwnerProfileID,
					IngestID:           ingestID,
					FragmentID:         fragment.FragmentID,
					SecurityEventDraft: *item.InitialEvent,
				}
				if _, err := insertSecurityEvent(ctx, tx, eventInput); err != nil {
					return err
				}
				if item.InitialEvent.Decision == "quarantine" {
					if err := insertEvidenceQuarantine(ctx, tx, input, ingestID, fragment.FragmentID, item.InitialEvent.Reason); err != nil {
						return err
					}
				}
			}
		}
		if err := applyDirectEvidenceSupersessions(ctx, tx, input, ingestID, evidence); err != nil {
			return err
		}
		var firstDisposition *PlacementFirstDisposition
		if completedAt != nil {
			firstDisposition, err = appendPlacementFirstDisposition(
				ctx,
				tx,
				input.TeamID,
				input.OwnerProfileID,
				placementRunID,
				input.Status,
				createdAt,
				*completedAt,
			)
			if err != nil {
				return err
			}
		}
		result = &CreateIngestResult{
			TeamID:           input.TeamID,
			OwnerProfileID:   input.OwnerProfileID,
			IngestID:         ingestID,
			PlacementRunID:   placementRunID,
			Status:           input.Status,
			Proposal:         input.Proposal,
			Evidence:         evidence,
			Items:            items,
			FirstDisposition: firstDisposition,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: create ingest: %w", err)
	}
	return result, nil
}

func (r *LedgerRepositoryImpl) AppendSecurityEvent(ctx context.Context, input SecurityEventInput) (string, error) {
	input = normalizeSecurityEventInput(input)
	if err := validateSecurityEventInput(input); err != nil {
		return "", err
	}
	var eventID string
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureEvidenceEventOwnership(ctx, tx, input); err != nil {
			return err
		}
		var err error
		eventID, err = insertSecurityEvent(ctx, tx, input)
		if err != nil {
			return err
		}
		if input.Decision == "quarantine" {
			return insertEvidenceQuarantine(ctx, tx, CreateIngestInput{
				TeamID:         input.TeamID,
				OwnerProfileID: input.OwnerProfileID,
			}, input.IngestID, input.FragmentID, input.Reason)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("ledger: append security event: %w", err)
	}
	return eventID, nil
}

func (r *LedgerRepositoryImpl) ClaimNextPlacementRun(ctx context.Context, teamID string, workerID string, lease time.Duration) (*PlacementRun, error) {
	teamID = strings.TrimSpace(teamID)
	workerID = strings.TrimSpace(workerID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if workerID == "" {
		return nil, errors.New("worker_id is required")
	}
	if lease <= 0 {
		return nil, errors.New("lease must be greater than zero")
	}
	if lease < time.Second {
		return nil, errors.New("lease must be at least one second")
	}
	var run *PlacementRun
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH ready AS MATERIALIZED (
				SELECT placement_run_id, available_at, created_at
				FROM placement_runs AS run
				WHERE run.team_id = ?::uuid
				  AND `+activeSemanticSpaceGenerationSQL("run")+`
				  AND run.attempts < run.max_attempts
				  AND run.status IN ('queued', 'guarded')
				  AND run.available_at <= now()
				ORDER BY run.available_at ASC, run.created_at ASC, run.placement_run_id ASC
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			),
			expired AS MATERIALIZED (
				SELECT placement_run_id, available_at, created_at
				FROM placement_runs AS run
				WHERE run.team_id = ?::uuid
				  AND `+activeSemanticSpaceGenerationSQL("run")+`
				  AND run.attempts < run.max_attempts
				  AND run.status = 'processing'
				  AND run.lease_until IS NOT NULL
				  AND run.lease_until < now()
				  AND NOT EXISTS (SELECT 1 FROM ready)
				ORDER BY run.lease_until ASC, run.created_at ASC, run.placement_run_id ASC
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			),
			next AS (
				SELECT placement_run_id
				FROM (
					SELECT placement_run_id, available_at, created_at, 0 AS priority
					FROM ready
					UNION ALL
					SELECT placement_run_id, available_at, created_at, 1 AS priority
					FROM expired
				) AS candidates
				ORDER BY priority ASC, available_at ASC, created_at ASC, placement_run_id ASC
				LIMIT 1
			),
			updated AS (
				UPDATE placement_runs AS run
				SET status = 'processing',
				    attempts = attempts + 1,
				    started_at = COALESCE(started_at, now()),
				    lease_until = now() + (? * interval '1 second'),
				    worker_id = ?,
				    updated_at = now()
				FROM next
				WHERE run.team_id = ?::uuid
				  AND run.placement_run_id = next.placement_run_id
				  AND `+activeSemanticSpaceGenerationSQL("run")+`
				RETURNING run.team_id, run.placement_run_id, run.ingest_id,
				          run.owner_profile_id, run.space_id, run.space_generation,
				          run.status, run.attempts, run.max_attempts,
				          run.lease_until
			)
			SELECT updated.team_id::text, updated.placement_run_id::text, updated.ingest_id::text,
			       updated.owner_profile_id::text, updated.space_id::text, updated.space_generation,
			       COALESCE(ingest.metadata #>> '{actor,correlation_id}', ''),
			       updated.status, updated.attempts, updated.max_attempts, updated.lease_until
			FROM updated
			JOIN knowledge_ingests AS ingest
			  ON ingest.team_id = updated.team_id
			 AND ingest.ingest_id = updated.ingest_id
		`, teamID, teamID, int(lease.Seconds()), workerID, teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		loaded := PlacementRun{}
		var leaseUntil sql.NullTime
		if err := rows.Scan(&loaded.TeamID, &loaded.PlacementRunID, &loaded.IngestID, &loaded.OwnerProfileID, &loaded.SpaceID, &loaded.SpaceGeneration, &loaded.CorrelationID, &loaded.Status, &loaded.Attempts, &loaded.MaxAttempts, &leaseUntil); err != nil {
			return err
		}
		if leaseUntil.Valid {
			loaded.LeaseUntil = &leaseUntil.Time
		}
		run = &loaded
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: claim placement run: %w", err)
	}
	return run, nil
}

func (r *LedgerRepositoryImpl) FinishPlacementRun(ctx context.Context, teamID string, placementRunID string, workerID string, status string, message string) (*PlacementFirstDisposition, error) {
	teamID = strings.TrimSpace(teamID)
	placementRunID = strings.TrimSpace(placementRunID)
	workerID = strings.TrimSpace(workerID)
	status = strings.TrimSpace(status)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(placementRunID); err != nil {
		return nil, fmt.Errorf("placement_run_id is required: %w", err)
	}
	if workerID == "" {
		return nil, errors.New("worker_id is required")
	}
	if status != string(domain.PlacementRunCompleted) &&
		status != string(domain.PlacementRunFailed) &&
		status != string(domain.PlacementRunQuarantined) {
		return nil, fmt.Errorf("unsupported placement status %q", status)
	}
	var firstDisposition *PlacementFirstDisposition
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			UPDATE placement_runs
			SET status = ?,
			    error = ?,
			    lease_until = NULL,
			    completed_at = now(),
			    quarantine_expires_at = CASE
			        WHEN ? = 'quarantined' THEN now() + interval '24 hours'
			        ELSE quarantine_expires_at
			    END,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND status = 'processing'
			  AND worker_id = ?
			  AND lease_until IS NOT NULL
			  AND lease_until > now()
			RETURNING owner_profile_id::text, created_at, completed_at
		`, status, strings.TrimSpace(message), status, teamID, placementRunID, workerID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return fmt.Errorf("%w: placement run is not actively leased by worker", ErrPlacementLeaseConflict)
		}
		var ownerProfileID string
		var createdAt, completedAt time.Time
		if err := rows.Scan(&ownerProfileID, &createdAt, &completedAt); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		firstDisposition, err = appendPlacementFirstDisposition(ctx, tx, teamID, ownerProfileID, placementRunID, status, createdAt, completedAt)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: finish placement run: %w", err)
	}
	return firstDisposition, nil
}

func (r *LedgerRepositoryImpl) withTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("ledger: database is required")
	}
	if r.rls == nil {
		return errors.New("ledger: rls helper is required")
	}
	return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (r *LedgerRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("ledger: database is required")
	}
	if r.rls == nil {
		return errors.New("ledger: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (r *LedgerRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("ledger: database is required")
	}
	if r.rls == nil {
		return errors.New("ledger: rls helper is required")
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

func ensureActiveTeamForMutation(ctx context.Context, tx *gorm.DB, teamID string) error {
	row := tx.WithContext(ctx).Raw(`
		SELECT id::text
		FROM teams
		WHERE id = ?::uuid
		  AND status = 'active'
		  AND deleted_at IS NULL
		FOR SHARE
	`, teamID).Row()
	var id string
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTeamInactive
		}
		return err
	}
	return nil
}

func validateExistingIngestHash(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string) error {
	row := tx.WithContext(ctx).Raw(`
		SELECT request_hash
		FROM knowledge_ingests
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, ingestID).Row()
	var existingHash string
	if err := row.Scan(&existingHash); err != nil {
		return err
	}
	if existingHash != input.RequestHash {
		return fmt.Errorf("%w: idempotency key %q already recorded with a different request", ErrIdempotencyConflict, input.IdempotencyKey)
	}
	return nil
}

func insertKnowledgeIngest(ctx context.Context, tx *gorm.DB, input CreateIngestInput) (string, bool, error) {
	proposal, err := marshalJSON(input.Proposal)
	if err != nil {
		return "", false, err
	}
	metadata, err := marshalJSON(knowledgeIngestMetadata(input))
	if err != nil {
		return "", false, err
	}
	if input.IdempotencyKey != "" {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH inserted AS (
				INSERT INTO knowledge_ingests (
				    team_id, owner_profile_id, space_id, space_generation,
				    idempotency_key, request_hash,
				    source_summary, status, proposal, metadata
				) VALUES (
				    ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?::bigint, 0),
				    ?, ?, ?, ?, ?::jsonb, ?::jsonb
				)
				ON CONFLICT (team_id, owner_profile_id, idempotency_key)
				WHERE idempotency_key <> ''
				DO NOTHING
				RETURNING ingest_id::text, request_hash, true AS created
			)
			SELECT ingest_id, request_hash, created FROM inserted
			UNION ALL
			SELECT ingest_id::text, request_hash, false AS created
			FROM knowledge_ingests
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND idempotency_key = ?
			LIMIT 1
		`, input.TeamID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration,
			input.IdempotencyKey, input.RequestHash,
			input.SourceSummary, input.Status, string(proposal), string(metadata),
			input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Rows()
		if err != nil {
			return "", false, err
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return "", false, err
			}
			if err := rows.Close(); err != nil {
				return "", false, err
			}
			ingestID, requestHash, err := selectKnowledgeIngestByIdempotency(ctx, tx, input)
			if err == nil && requestHash != input.RequestHash {
				err = fmt.Errorf("%w: idempotency key reused with different request hash", ErrIdempotencyConflict)
			}
			return ingestID, false, err
		}
		var ingestID string
		var requestHash string
		var created bool
		if err := rows.Scan(&ingestID, &requestHash, &created); err != nil {
			_ = rows.Close()
			return "", false, err
		}
		if !created && requestHash != input.RequestHash {
			_ = rows.Close()
			return "", false, fmt.Errorf("%w: idempotency key reused with different request hash", ErrIdempotencyConflict)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return "", false, err
		}
		return ingestID, created, rows.Close()
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO knowledge_ingests (
		    team_id, owner_profile_id, space_id, space_generation,
		    request_hash, source_summary, status, proposal, metadata
		) VALUES (
		    ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?::bigint, 0),
		    ?, ?, ?, ?::jsonb, ?::jsonb
		)
		RETURNING ingest_id::text
	`, input.TeamID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration,
		input.RequestHash, input.SourceSummary,
		input.Status, string(proposal), string(metadata)).Rows()
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", false, sql.ErrNoRows
	}
	var ingestID string
	if err := rows.Scan(&ingestID); err != nil {
		return "", false, err
	}
	return ingestID, true, rows.Err()
}

const (
	ingestMetadataTelemetryOriginKey      = "_dense_mem_telemetry_origin"
	ingestMetadataTelemetryOriginRemember = "remember"
)

func knowledgeIngestMetadata(input CreateIngestInput) map[string]any {
	metadata := make(map[string]any, len(input.Metadata)+1)
	for key, value := range input.Metadata {
		if key == ingestMetadataTelemetryOriginKey {
			continue
		}
		metadata[key] = value
	}
	if input.TelemetryRemember {
		metadata[ingestMetadataTelemetryOriginKey] = ingestMetadataTelemetryOriginRemember
	}
	return metadata
}

func selectKnowledgeIngestByIdempotency(ctx context.Context, tx *gorm.DB, input CreateIngestInput) (string, string, error) {
	row := tx.WithContext(ctx).Raw(`
		SELECT ingest_id::text, request_hash
		FROM knowledge_ingests
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND idempotency_key = ?
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Row()
	var ingestID string
	var requestHash string
	if err := row.Scan(&ingestID, &requestHash); err != nil {
		return "", "", err
	}
	return ingestID, requestHash, nil
}

func insertPlacementRun(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string) (string, time.Time, *time.Time, error) {
	completedExpr := "NULL"
	if input.Status == string(domain.PlacementRunQuarantined) {
		completedExpr = "now()"
	}
	rows, err := tx.WithContext(ctx).Raw(fmt.Sprintf(`
		INSERT INTO placement_runs (
		    team_id, ingest_id, owner_profile_id, space_id, space_generation,
		    status, completed_at, quarantine_expires_at
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?::bigint, 0),
		    ?, %s,
		    CASE WHEN ? = 'quarantined' THEN now() + interval '24 hours' ELSE NULL END
		)
		RETURNING placement_run_id::text, created_at, completed_at
	`, completedExpr), input.TeamID, ingestID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration,
		input.Status, input.Status).Rows()
	if err != nil {
		return "", time.Time{}, nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", time.Time{}, nil, sql.ErrNoRows
	}
	var placementRunID string
	var createdAt time.Time
	var completedAt sql.NullTime
	if err := rows.Scan(&placementRunID, &createdAt, &completedAt); err != nil {
		return "", time.Time{}, nil, err
	}
	if err := rows.Err(); err != nil {
		return "", time.Time{}, nil, err
	}
	if completedAt.Valid {
		completed := completedAt.Time.UTC()
		return placementRunID, createdAt.UTC(), &completed, nil
	}
	return placementRunID, createdAt.UTC(), nil, nil
}

func insertEvidenceFragment(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string, index int, item EvidenceInput, source *SourceRevisionResult) (EvidenceFragment, error) {
	metadata, err := marshalJSON(item.Metadata)
	if err != nil {
		return EvidenceFragment{}, err
	}
	sourceID := ""
	sourceRevisionID := ""
	if source != nil {
		sourceID = source.SourceID
		sourceRevisionID = source.SourceRevisionID
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_fragments (
		    team_id, ingest_id, owner_profile_id, space_id, space_generation,
		    evidence_index, content,
		    content_hash, source_type, authority, source_ref, source_id,
		    source_revision_id, labels, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?::bigint, 0),
		    ?, ?, ?, ?, ?, ?,
		    NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?, ?::jsonb
		)
	RETURNING fragment_id::text, authority
	`, input.TeamID, ingestID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration,
		index, item.Content, item.ContentHash,
		item.SourceType, item.Authority, item.SourceRef, sourceID, sourceRevisionID,
		pqStringArray(item.Labels), string(metadata)).Rows()
	if err != nil {
		return EvidenceFragment{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return EvidenceFragment{}, sql.ErrNoRows
	}
	fragment := EvidenceFragment{
		EvidenceIndex:    index,
		Content:          item.Content,
		ContentHash:      item.ContentHash,
		SourceID:         sourceID,
		SourceRevisionID: sourceRevisionID,
	}
	if err := rows.Scan(&fragment.FragmentID, &fragment.Authority); err != nil {
		return EvidenceFragment{}, err
	}
	return fragment, rows.Err()
}

func insertPlacementItem(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string, placementRunID string, fragment EvidenceFragment, item EvidenceInput) (PlacementItem, error) {
	status := string(domain.PlacementRunQueued)
	category := "pending"
	if input.Status == string(domain.PlacementRunQuarantined) || (item.InitialEvent != nil && item.InitialEvent.Decision == "quarantine") {
		status = string(domain.PlacementRunQuarantined)
		category = "quarantined"
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO placement_items (
		    team_id, placement_run_id, ingest_id, owner_profile_id, fragment_id,
		    space_id, space_generation,
		    evidence_index, status, category
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
		    NULLIF(?, '')::uuid, NULLIF(?::bigint, 0), ?, ?, ?
		)
		RETURNING placement_item_id::text, claim_key::text, version
	`, input.TeamID, placementRunID, ingestID, input.OwnerProfileID, fragment.FragmentID,
		input.SpaceID, input.SpaceGeneration, fragment.EvidenceIndex, status, category).Rows()
	if err != nil {
		return PlacementItem{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return PlacementItem{}, sql.ErrNoRows
	}
	placementItem := PlacementItem{
		FragmentID:    fragment.FragmentID,
		EvidenceIndex: fragment.EvidenceIndex,
		Status:        status,
		Category:      category,
	}
	if err := rows.Scan(&placementItem.PlacementItemID, &placementItem.ClaimKey, &placementItem.Version); err != nil {
		return PlacementItem{}, err
	}
	return placementItem, rows.Err()
}

func loadCreateIngestResult(ctx context.Context, tx *gorm.DB, teamID string, ingestID string, existing bool) (*CreateIngestResult, error) {
	result := CreateIngestResult{TeamID: teamID, IngestID: ingestID, Existing: existing}
	row := tx.WithContext(ctx).Raw(`
		SELECT run.placement_run_id::text, run.owner_profile_id::text, run.status,
		       run.attempts, run.max_attempts, COALESCE(ingest.proposal, '{}'::jsonb),
		       COALESCE(ingest.metadata #>> '{actor,correlation_id}', '')
		FROM placement_runs AS run
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = run.team_id
		 AND ingest.ingest_id = run.ingest_id
		WHERE run.team_id = ?::uuid
		  AND run.ingest_id = ?::uuid
	`, teamID, ingestID).Row()
	var proposalRaw []byte
	if err := row.Scan(
		&result.PlacementRunID,
		&result.OwnerProfileID,
		&result.Status,
		&result.Attempts,
		&result.MaxAttempts,
		&proposalRaw,
		&result.CorrelationID,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(proposalRaw, &result.Proposal); err != nil {
		return nil, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
			SELECT fragment_id::text, evidence_index, content, content_hash, authority,
			       COALESCE(source_id::text, ''), COALESCE(source_revision_id::text, '')
			FROM evidence_fragments
			WHERE team_id = ?::uuid
			  AND ingest_id = ?::uuid
			ORDER BY evidence_index ASC
	`, teamID, ingestID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item EvidenceFragment
		if err := rows.Scan(&item.FragmentID, &item.EvidenceIndex, &item.Content, &item.ContentHash, &item.Authority, &item.SourceID, &item.SourceRevisionID); err != nil {
			return nil, err
		}
		result.Evidence = append(result.Evidence, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	itemRows, err := tx.WithContext(ctx).Raw(`
		SELECT placement_item_id::text, fragment_id::text, claim_key::text, evidence_index, status, category,
		       version, COALESCE(result, '{}'::jsonb)
		FROM placement_items
		WHERE team_id = ?::uuid
		  AND ingest_id = ?::uuid
		ORDER BY evidence_index ASC
	`, teamID, ingestID).Rows()
	if err != nil {
		return nil, err
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var item PlacementItem
		var resultRaw []byte
		if err := itemRows.Scan(
			&item.PlacementItemID,
			&item.FragmentID,
			&item.ClaimKey,
			&item.EvidenceIndex,
			&item.Status,
			&item.Category,
			&item.Version,
			&resultRaw,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(resultRaw, &item.Result); err != nil {
			return nil, err
		}
		result.Items = append(result.Items, item)
	}
	if err := itemRows.Err(); err != nil {
		return nil, err
	}
	if err := itemRows.Close(); err != nil {
		return nil, err
	}
	if err := hydratePlacementItemSearchStates(ctx, tx, result.TeamID, result.OwnerProfileID, result.Items); err != nil {
		return nil, err
	}
	if err := hydrateEvidenceLifecycleLineage(ctx, tx, result.TeamID, result.Evidence); err != nil {
		return nil, err
	}
	return &result, nil
}
