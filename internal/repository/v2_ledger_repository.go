package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var ErrV2SourceRevisionConflict = errors.New("v2 source revision conflict")

type V2LedgerRepository interface {
	CreateIngest(ctx context.Context, input V2CreateIngestInput) (*V2CreateIngestResult, error)
	AdvanceSourceRevision(ctx context.Context, input V2AdvanceSourceRevisionInput) (*V2SourceRevisionResult, error)
	AppendSecurityEvent(ctx context.Context, input V2SecurityEventInput) (string, error)
	ClaimNextPlacementRun(ctx context.Context, teamID string, workerID string, lease time.Duration) (*V2PlacementRun, error)
	FinishPlacementRun(ctx context.Context, teamID string, placementRunID string, status string, message string) error
}

type V2CreateIngestInput struct {
	TeamID         string
	OwnerProfileID string
	IdempotencyKey string
	RequestHash    string
	SourceSummary  string
	Status         string
	Proposal       map[string]any
	Metadata       map[string]any
	Evidence       []V2EvidenceInput
}

type V2EvidenceInput struct {
	Content      string
	ContentHash  string
	SourceType   string
	Authority    string
	SourceRef    string
	Labels       []string
	Metadata     map[string]any
	InitialEvent *V2SecurityEventDraft
}

type V2SecurityEventDraft struct {
	EventKind      string
	Decision       string
	ScanPolicyHash string
	Reason         string
	Signals        []V2SecuritySignalInput
	Metadata       map[string]any
}

type V2SecurityEventInput struct {
	TeamID         string
	OwnerProfileID string
	IngestID       string
	FragmentID     string
	V2SecurityEventDraft
}

type V2SecuritySignalInput struct {
	Kind      string
	Severity  string
	SpanStart int
	SpanEnd   int
	Quote     string
	Metadata  map[string]any
}

type V2CreateIngestResult struct {
	TeamID         string
	IngestID       string
	PlacementRunID string
	Existing       bool
	Evidence       []V2EvidenceFragment
}

type V2EvidenceFragment struct {
	FragmentID    string
	EvidenceIndex int
	ContentHash   string
}

type V2AdvanceSourceRevisionInput struct {
	TeamID                        string
	OwnerProfileID                string
	SourceKey                     string
	SourceKind                    string
	Authority                     string
	RevisionToken                 string
	ExpectedPreviousRevisionToken string
	ContentHash                   string
	Envelope                      map[string]any
}

type V2SourceRevisionResult struct {
	TeamID           string
	SourceID         string
	SourceRevisionID string
	RevisionToken    string
}

type V2PlacementRun struct {
	TeamID         string
	PlacementRunID string
	IngestID       string
	OwnerProfileID string
	Status         string
	Attempts       int
	LeaseUntil     *time.Time
}

type v2RLSHelper interface {
	WithTeamTx(ctx context.Context, db *gorm.DB, teamID string, fn func(tx *gorm.DB) error) error
	WithTeamProfileTx(ctx context.Context, db *gorm.DB, teamID string, profileID string, fn func(tx *gorm.DB) error) error
}

type V2LedgerRepositoryImpl struct {
	db  *gorm.DB
	rls v2RLSHelper
}

var _ V2LedgerRepository = (*V2LedgerRepositoryImpl)(nil)

func NewV2LedgerRepository(db *gorm.DB, rls *postgres.RLS) *V2LedgerRepositoryImpl {
	return &V2LedgerRepositoryImpl{db: db, rls: rls}
}

func (r *V2LedgerRepositoryImpl) CreateIngest(ctx context.Context, input V2CreateIngestInput) (*V2CreateIngestResult, error) {
	input = normalizeV2CreateIngestInput(input)
	if err := validateV2CreateIngestInput(input); err != nil {
		return nil, err
	}
	var result *V2CreateIngestResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		ingestID, created, err := insertV2KnowledgeIngest(ctx, tx, input)
		if err != nil {
			return err
		}
		if !created {
			loaded, err := loadV2CreateIngestResult(ctx, tx, input.TeamID, ingestID, true)
			if err != nil {
				return err
			}
			result = loaded
			return nil
		}
		placementRunID, err := insertV2PlacementRun(ctx, tx, input, ingestID)
		if err != nil {
			return err
		}
		evidence := make([]V2EvidenceFragment, 0, len(input.Evidence))
		for i, item := range input.Evidence {
			fragment, err := insertV2EvidenceFragment(ctx, tx, input, ingestID, i, item)
			if err != nil {
				return err
			}
			evidence = append(evidence, fragment)
			if err := insertV2PlacementItem(ctx, tx, input, ingestID, placementRunID, fragment); err != nil {
				return err
			}
			if item.InitialEvent != nil {
				eventInput := V2SecurityEventInput{
					TeamID:               input.TeamID,
					OwnerProfileID:       input.OwnerProfileID,
					IngestID:             ingestID,
					FragmentID:           fragment.FragmentID,
					V2SecurityEventDraft: *item.InitialEvent,
				}
				if _, err := insertV2SecurityEvent(ctx, tx, eventInput); err != nil {
					return err
				}
			}
		}
		result = &V2CreateIngestResult{
			TeamID:         input.TeamID,
			IngestID:       ingestID,
			PlacementRunID: placementRunID,
			Evidence:       evidence,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 ledger: create ingest: %w", err)
	}
	return result, nil
}

func (r *V2LedgerRepositoryImpl) AdvanceSourceRevision(ctx context.Context, input V2AdvanceSourceRevisionInput) (*V2SourceRevisionResult, error) {
	input = normalizeV2AdvanceSourceRevisionInput(input)
	if err := validateV2AdvanceSourceRevisionInput(input); err != nil {
		return nil, err
	}
	var result *V2SourceRevisionResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		sourceID, currentRevisionID, currentToken, err := getOrCreateV2EvidenceSource(ctx, tx, input)
		if err != nil {
			return err
		}
		if currentToken != input.ExpectedPreviousRevisionToken {
			return fmt.Errorf("%w: expected %q, got %q", ErrV2SourceRevisionConflict, input.ExpectedPreviousRevisionToken, currentToken)
		}
		revisionID, err := insertV2SourceRevision(ctx, tx, input, sourceID, currentRevisionID)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`
			UPDATE evidence_sources
			SET current_revision_id = ?::uuid,
			    current_revision_token = ?,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND source_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND current_revision_token = ?
		`, revisionID, input.RevisionToken, input.TeamID, sourceID, input.OwnerProfileID, currentToken).Error; err != nil {
			return err
		}
		result = &V2SourceRevisionResult{
			TeamID:           input.TeamID,
			SourceID:         sourceID,
			SourceRevisionID: revisionID,
			RevisionToken:    input.RevisionToken,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 ledger: advance source revision: %w", err)
	}
	return result, nil
}

func (r *V2LedgerRepositoryImpl) AppendSecurityEvent(ctx context.Context, input V2SecurityEventInput) (string, error) {
	input = normalizeV2SecurityEventInput(input)
	if err := validateV2SecurityEventInput(input); err != nil {
		return "", err
	}
	var eventID string
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		var err error
		eventID, err = insertV2SecurityEvent(ctx, tx, input)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("v2 ledger: append security event: %w", err)
	}
	return eventID, nil
}

func (r *V2LedgerRepositoryImpl) ClaimNextPlacementRun(ctx context.Context, teamID string, workerID string, lease time.Duration) (*V2PlacementRun, error) {
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
	var run *V2PlacementRun
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH next AS (
				SELECT placement_run_id
				FROM placement_runs
				WHERE team_id = ?::uuid
				  AND attempts < max_attempts
				  AND (
					(status IN ('queued', 'guarded') AND available_at <= now())
					OR (status = 'processing' AND lease_until IS NOT NULL AND lease_until < now())
				  )
				ORDER BY
					CASE WHEN status IN ('queued', 'guarded') THEN 0 ELSE 1 END,
					available_at ASC,
					created_at ASC
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			)
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
			RETURNING run.team_id::text, run.placement_run_id::text, run.ingest_id::text,
			          run.owner_profile_id::text, run.status, run.attempts, run.lease_until
		`, teamID, int(lease.Seconds()), workerID, teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		loaded := V2PlacementRun{}
		var leaseUntil sql.NullTime
		if err := rows.Scan(&loaded.TeamID, &loaded.PlacementRunID, &loaded.IngestID, &loaded.OwnerProfileID, &loaded.Status, &loaded.Attempts, &leaseUntil); err != nil {
			return err
		}
		if leaseUntil.Valid {
			loaded.LeaseUntil = &leaseUntil.Time
		}
		run = &loaded
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 ledger: claim placement run: %w", err)
	}
	return run, nil
}

func (r *V2LedgerRepositoryImpl) FinishPlacementRun(ctx context.Context, teamID string, placementRunID string, status string, message string) error {
	teamID = strings.TrimSpace(teamID)
	placementRunID = strings.TrimSpace(placementRunID)
	status = strings.TrimSpace(status)
	if _, err := uuid.Parse(teamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(placementRunID); err != nil {
		return fmt.Errorf("placement_run_id is required: %w", err)
	}
	if status != "completed" && status != "failed" && status != "quarantined" {
		return fmt.Errorf("unsupported placement status %q", status)
	}
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Exec(`
			UPDATE placement_runs
			SET status = ?,
			    error = ?,
			    lease_until = NULL,
			    completed_at = now(),
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, status, strings.TrimSpace(message), teamID, placementRunID).Error
	})
	if err != nil {
		return fmt.Errorf("v2 ledger: finish placement run: %w", err)
	}
	return nil
}

func (r *V2LedgerRepositoryImpl) withTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(tx *gorm.DB) error) error {
	if r.rls != nil {
		return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, fn)
	}
	return r.db.WithContext(ctx).Transaction(fn)
}

func (r *V2LedgerRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if r.rls != nil {
		return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
	}
	return r.db.WithContext(ctx).Transaction(fn)
}

func normalizeV2CreateIngestInput(input V2CreateIngestInput) V2CreateIngestInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	input.SourceSummary = strings.TrimSpace(input.SourceSummary)
	input.Status = strings.TrimSpace(input.Status)
	if input.Status == "" {
		input.Status = "queued"
	}
	for i := range input.Evidence {
		input.Evidence[i].Content = strings.TrimSpace(input.Evidence[i].Content)
		input.Evidence[i].ContentHash = strings.TrimSpace(input.Evidence[i].ContentHash)
		if input.Evidence[i].ContentHash == "" && input.Evidence[i].Content != "" {
			input.Evidence[i].ContentHash = sha256Hex(input.Evidence[i].Content)
		}
		input.Evidence[i].SourceType = strings.TrimSpace(input.Evidence[i].SourceType)
		if input.Evidence[i].SourceType == "" {
			input.Evidence[i].SourceType = "conversation"
		}
		input.Evidence[i].Authority = strings.TrimSpace(input.Evidence[i].Authority)
		if input.Evidence[i].Authority == "" {
			input.Evidence[i].Authority = "primary"
		}
		input.Evidence[i].SourceRef = strings.TrimSpace(input.Evidence[i].SourceRef)
	}
	return input
}

func validateV2CreateIngestInput(input V2CreateIngestInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.Status != "queued" && input.Status != "guarded" && input.Status != "quarantined" {
		return fmt.Errorf("unsupported ingest status %q", input.Status)
	}
	if len(input.Evidence) == 0 {
		return errors.New("evidence is required")
	}
	for i, item := range input.Evidence {
		if item.Content == "" {
			return fmt.Errorf("evidence[%d].content is required", i)
		}
		if item.ContentHash == "" {
			return fmt.Errorf("evidence[%d].content_hash is required", i)
		}
		if item.SourceType != "conversation" && item.SourceType != "document" && item.SourceType != "observation" && item.SourceType != "manual" {
			return fmt.Errorf("evidence[%d].source_type is unsupported", i)
		}
		if item.Authority != "primary" && item.Authority != "secondary" && item.Authority != "derived" {
			return fmt.Errorf("evidence[%d].authority is unsupported", i)
		}
		if item.InitialEvent != nil {
			if err := validateV2SecurityEventDraft(*item.InitialEvent); err != nil {
				return fmt.Errorf("evidence[%d].security_event: %w", i, err)
			}
		}
	}
	return nil
}

func ensureV2SemanticRefs(ctx context.Context, tx *gorm.DB, teamID, profileID string) error {
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO semantic_team_refs (team_id)
		VALUES (?::uuid)
		ON CONFLICT (team_id) DO NOTHING
	`, teamID).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO semantic_profile_refs (team_id, profile_id)
		VALUES (?::uuid, ?::uuid)
		ON CONFLICT (team_id, profile_id) DO NOTHING
	`, teamID, profileID).Error
}

func insertV2KnowledgeIngest(ctx context.Context, tx *gorm.DB, input V2CreateIngestInput) (string, bool, error) {
	proposal, err := marshalV2JSON(input.Proposal)
	if err != nil {
		return "", false, err
	}
	metadata, err := marshalV2JSON(input.Metadata)
	if err != nil {
		return "", false, err
	}
	if input.IdempotencyKey != "" {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH inserted AS (
				INSERT INTO knowledge_ingests (
				    team_id, owner_profile_id, idempotency_key, request_hash,
				    source_summary, status, proposal, metadata
				) VALUES (
				    ?::uuid, ?::uuid, ?, ?, ?, ?, ?::jsonb, ?::jsonb
				)
				ON CONFLICT (team_id, owner_profile_id, idempotency_key)
				WHERE idempotency_key <> ''
				DO NOTHING
				RETURNING ingest_id::text, true AS created
			)
			SELECT ingest_id, created FROM inserted
			UNION ALL
			SELECT ingest_id::text, false AS created
			FROM knowledge_ingests
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND idempotency_key = ?
			LIMIT 1
		`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey, input.RequestHash,
			input.SourceSummary, input.Status, string(proposal), string(metadata),
			input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Rows()
		if err != nil {
			return "", false, err
		}
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return "", false, err
			}
			if err := rows.Close(); err != nil {
				return "", false, err
			}
			ingestID, err := selectV2KnowledgeIngestByIdempotency(ctx, tx, input)
			return ingestID, false, err
		}
		var ingestID string
		var created bool
		if err := rows.Scan(&ingestID, &created); err != nil {
			_ = rows.Close()
			return "", false, err
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return "", false, err
		}
		return ingestID, created, rows.Close()
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO knowledge_ingests (
		    team_id, owner_profile_id, request_hash, source_summary, status, proposal, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?, ?, ?, ?::jsonb, ?::jsonb
		)
		RETURNING ingest_id::text
	`, input.TeamID, input.OwnerProfileID, input.RequestHash, input.SourceSummary,
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

func selectV2KnowledgeIngestByIdempotency(ctx context.Context, tx *gorm.DB, input V2CreateIngestInput) (string, error) {
	row := tx.WithContext(ctx).Raw(`
		SELECT ingest_id::text
		FROM knowledge_ingests
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND idempotency_key = ?
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Row()
	var ingestID string
	if err := row.Scan(&ingestID); err != nil {
		return "", err
	}
	return ingestID, nil
}

func insertV2PlacementRun(ctx context.Context, tx *gorm.DB, input V2CreateIngestInput, ingestID string) (string, error) {
	completedExpr := "NULL"
	if input.Status == "quarantined" {
		completedExpr = "now()"
	}
	rows, err := tx.WithContext(ctx).Raw(fmt.Sprintf(`
		INSERT INTO placement_runs (
		    team_id, ingest_id, owner_profile_id, status, completed_at
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, %s
		)
		RETURNING placement_run_id::text
	`, completedExpr), input.TeamID, ingestID, input.OwnerProfileID, input.Status).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", sql.ErrNoRows
	}
	var placementRunID string
	if err := rows.Scan(&placementRunID); err != nil {
		return "", err
	}
	return placementRunID, rows.Err()
}

func insertV2EvidenceFragment(ctx context.Context, tx *gorm.DB, input V2CreateIngestInput, ingestID string, index int, item V2EvidenceInput) (V2EvidenceFragment, error) {
	metadata, err := marshalV2JSON(item.Metadata)
	if err != nil {
		return V2EvidenceFragment{}, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_fragments (
		    team_id, ingest_id, owner_profile_id, evidence_index, content,
		    content_hash, source_type, authority, source_ref, labels, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?::jsonb
		)
		RETURNING fragment_id::text
	`, input.TeamID, ingestID, input.OwnerProfileID, index, item.Content, item.ContentHash,
		item.SourceType, item.Authority, item.SourceRef, pqStringArray(item.Labels), string(metadata)).Rows()
	if err != nil {
		return V2EvidenceFragment{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return V2EvidenceFragment{}, sql.ErrNoRows
	}
	fragment := V2EvidenceFragment{EvidenceIndex: index, ContentHash: item.ContentHash}
	if err := rows.Scan(&fragment.FragmentID); err != nil {
		return V2EvidenceFragment{}, err
	}
	return fragment, rows.Err()
}

func insertV2PlacementItem(ctx context.Context, tx *gorm.DB, input V2CreateIngestInput, ingestID string, placementRunID string, fragment V2EvidenceFragment) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO placement_items (
		    team_id, placement_run_id, ingest_id, owner_profile_id, fragment_id, evidence_index
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?
		)
	`, input.TeamID, placementRunID, ingestID, input.OwnerProfileID, fragment.FragmentID, fragment.EvidenceIndex).Error
}

func loadV2CreateIngestResult(ctx context.Context, tx *gorm.DB, teamID string, ingestID string, existing bool) (*V2CreateIngestResult, error) {
	result := V2CreateIngestResult{TeamID: teamID, IngestID: ingestID, Existing: existing}
	if err := tx.WithContext(ctx).Raw(`
		SELECT placement_run_id::text
		FROM placement_runs
		WHERE team_id = ?::uuid
		  AND ingest_id = ?::uuid
	`, teamID, ingestID).Scan(&result.PlacementRunID).Error; err != nil {
		return nil, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT fragment_id::text, evidence_index, content_hash
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
		var item V2EvidenceFragment
		if err := rows.Scan(&item.FragmentID, &item.EvidenceIndex, &item.ContentHash); err != nil {
			return nil, err
		}
		result.Evidence = append(result.Evidence, item)
	}
	return &result, rows.Err()
}

func normalizeV2AdvanceSourceRevisionInput(input V2AdvanceSourceRevisionInput) V2AdvanceSourceRevisionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SourceKey = strings.TrimSpace(input.SourceKey)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	if input.SourceKind == "" {
		input.SourceKind = "conversation"
	}
	input.Authority = strings.TrimSpace(input.Authority)
	if input.Authority == "" {
		input.Authority = "primary"
	}
	input.RevisionToken = strings.TrimSpace(input.RevisionToken)
	input.ExpectedPreviousRevisionToken = strings.TrimSpace(input.ExpectedPreviousRevisionToken)
	input.ContentHash = strings.TrimSpace(input.ContentHash)
	return input
}

func validateV2AdvanceSourceRevisionInput(input V2AdvanceSourceRevisionInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.SourceKey == "" {
		return errors.New("source_key is required")
	}
	if input.RevisionToken == "" {
		return errors.New("revision_token is required")
	}
	if input.ContentHash == "" {
		return errors.New("content_hash is required")
	}
	return nil
}

func getOrCreateV2EvidenceSource(ctx context.Context, tx *gorm.DB, input V2AdvanceSourceRevisionInput) (string, string, string, error) {
	var sourceID string
	var currentRevisionID sql.NullString
	var currentToken string
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT source_id::text, current_revision_id::text, current_revision_token
		FROM evidence_sources
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND source_key = ?
		LIMIT 1
		FOR UPDATE
	`, input.TeamID, input.OwnerProfileID, input.SourceKey).Rows()
	if err != nil {
		return "", "", "", err
	}
	if rows.Next() {
		if err := rows.Scan(&sourceID, &currentRevisionID, &currentToken); err != nil {
			_ = rows.Close()
			return "", "", "", err
		}
		if err := rows.Close(); err != nil {
			return "", "", "", err
		}
		return sourceID, currentRevisionID.String, currentToken, nil
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", "", "", err
	}
	if err := rows.Close(); err != nil {
		return "", "", "", err
	}
	if input.ExpectedPreviousRevisionToken != "" {
		return "", "", "", fmt.Errorf("%w: source does not exist", ErrV2SourceRevisionConflict)
	}
	metadata, err := marshalV2JSON(nil)
	if err != nil {
		return "", "", "", err
	}
	insertRows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_sources (
		    team_id, owner_profile_id, source_key, source_kind, authority, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?, ?, ?, ?::jsonb
		)
		RETURNING source_id::text
	`, input.TeamID, input.OwnerProfileID, input.SourceKey, input.SourceKind, input.Authority, string(metadata)).Rows()
	if err != nil {
		return "", "", "", err
	}
	defer insertRows.Close()
	if !insertRows.Next() {
		return "", "", "", sql.ErrNoRows
	}
	if err := insertRows.Scan(&sourceID); err != nil {
		return "", "", "", err
	}
	return sourceID, "", "", insertRows.Err()
}

func insertV2SourceRevision(ctx context.Context, tx *gorm.DB, input V2AdvanceSourceRevisionInput, sourceID string, supersedesRevisionID string) (string, error) {
	envelope, err := marshalV2JSON(input.Envelope)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_source_revisions (
		    team_id, source_id, owner_profile_id, revision_token,
		    expected_previous_revision_token, supersedes_revision_id, content_hash, envelope
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?, NULLIF(?, '')::uuid, ?, ?::jsonb
		)
		RETURNING source_revision_id::text
	`, input.TeamID, sourceID, input.OwnerProfileID, input.RevisionToken,
		input.ExpectedPreviousRevisionToken, supersedesRevisionID, input.ContentHash, string(envelope)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", sql.ErrNoRows
	}
	var revisionID string
	if err := rows.Scan(&revisionID); err != nil {
		return "", err
	}
	return revisionID, rows.Err()
}

func normalizeV2SecurityEventInput(input V2SecurityEventInput) V2SecurityEventInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.FragmentID = strings.TrimSpace(input.FragmentID)
	input.V2SecurityEventDraft = normalizeV2SecurityEventDraft(input.V2SecurityEventDraft)
	return input
}

func normalizeV2SecurityEventDraft(input V2SecurityEventDraft) V2SecurityEventDraft {
	input.EventKind = strings.TrimSpace(input.EventKind)
	input.Decision = strings.TrimSpace(input.Decision)
	input.ScanPolicyHash = strings.TrimSpace(input.ScanPolicyHash)
	input.Reason = strings.TrimSpace(input.Reason)
	for i := range input.Signals {
		input.Signals[i].Kind = strings.TrimSpace(input.Signals[i].Kind)
		input.Signals[i].Severity = strings.TrimSpace(input.Signals[i].Severity)
		input.Signals[i].Quote = strings.TrimSpace(input.Signals[i].Quote)
	}
	return input
}

func validateV2SecurityEventInput(input V2SecurityEventInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.IngestID); err != nil {
		return fmt.Errorf("ingest_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.FragmentID); err != nil {
		return fmt.Errorf("fragment_id is required: %w", err)
	}
	return validateV2SecurityEventDraft(input.V2SecurityEventDraft)
}

func validateV2SecurityEventDraft(input V2SecurityEventDraft) error {
	switch input.EventKind {
	case "deterministic_scan", "reviewer_signal", "verifier_signal", "quarantine_release":
	default:
		return fmt.Errorf("unsupported event_kind %q", input.EventKind)
	}
	switch input.Decision {
	case "pass", "guarded", "quarantine", "released":
	default:
		return fmt.Errorf("unsupported decision %q", input.Decision)
	}
	for i, signal := range input.Signals {
		switch signal.Kind {
		case "role_control_spoofing", "instruction_override", "prompt_secret_extraction", "tool_exfiltration", "obfuscated_instruction", "hidden_control_markup":
		default:
			return fmt.Errorf("signals[%d].kind is unsupported", i)
		}
		switch signal.Severity {
		case "low", "medium", "high", "critical":
		default:
			return fmt.Errorf("signals[%d].severity is unsupported", i)
		}
		if signal.SpanStart < 0 || signal.SpanEnd <= signal.SpanStart {
			return fmt.Errorf("signals[%d].span is invalid", i)
		}
	}
	return nil
}

func insertV2SecurityEvent(ctx context.Context, tx *gorm.DB, input V2SecurityEventInput) (string, error) {
	metadata, err := marshalV2JSON(input.Metadata)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_security_events (
		    team_id, fragment_id, ingest_id, owner_profile_id, event_kind, decision,
		    scan_policy_hash, reason, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?::jsonb
		)
		RETURNING security_event_id::text
	`, input.TeamID, input.FragmentID, input.IngestID, input.OwnerProfileID,
		input.EventKind, input.Decision, input.ScanPolicyHash, input.Reason, string(metadata)).Rows()
	if err != nil {
		return "", err
	}
	if !rows.Next() {
		_ = rows.Close()
		return "", sql.ErrNoRows
	}
	var eventID string
	if err := rows.Scan(&eventID); err != nil {
		_ = rows.Close()
		return "", err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	for i, signal := range input.Signals {
		if err := insertV2SecuritySignal(ctx, tx, input.TeamID, input.OwnerProfileID, eventID, i, signal); err != nil {
			return "", err
		}
	}
	return eventID, nil
}

func insertV2SecuritySignal(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, eventID string, index int, signal V2SecuritySignalInput) error {
	metadata, err := marshalV2JSON(signal.Metadata)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO evidence_security_signals (
		    team_id, security_event_id, signal_index, owner_profile_id,
		    kind, severity, span_start, span_end, quote, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?, ?::uuid, ?, ?, ?, ?, ?, ?::jsonb
		)
	`, teamID, eventID, index, ownerProfileID, signal.Kind, signal.Severity,
		signal.SpanStart, signal.SpanEnd, signal.Quote, string(metadata)).Error
}

func marshalV2JSON(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return data, nil
}

func pqStringArray(values []string) any {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	return pq.Array(normalized)
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}
