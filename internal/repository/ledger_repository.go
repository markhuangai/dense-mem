package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var (
	ErrIdempotencyConflict       = errors.New("idempotency conflict")
	ErrSourceRevisionConflict    = errors.New("source revision conflict")
	ErrEvidenceLifecycleNotFound = errors.New("evidence lifecycle target not found")
	ErrEvidenceLifecycleConflict = errors.New("evidence lifecycle conflict")
	ErrTeamInactive              = errors.New("team is not active")
)

// LedgerRepository contains only durable, non-workflow operations. Semantic
// claiming and status mutation are intentionally not part of the runtime API.
type LedgerRepository interface {
	AdvanceSourceRevision(context.Context, AdvanceSourceRevisionInput) (*SourceRevisionResult, error)
	AppendSecurityEvent(context.Context, SecurityEventInput) (string, error)
}

// CreateIngestInput is the low-level evidence input used by conflict-derived
// evidence and synchronous commit helpers. It never creates placement state.
type CreateIngestInput struct {
	TeamID            string
	OwnerProfileID    string
	IngestID          string
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

type EvidenceInput struct {
	FragmentID                    string
	Content                       string
	ForceInsert                   bool
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
	TeamID                 string
	OwnerProfileID         string
	IngestID               string
	FragmentID             string
	OccurrenceID           string
	EvidenceOwnerProfileID string
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

type EvidenceIngestResult struct {
	TeamID              string
	OwnerProfileID      string
	IngestID            string
	Status              string
	CorrelationID       string
	Existing            bool
	Proposal            map[string]any
	Evidence            []EvidenceFragment
	RelationshipResults []SubmissionRelationshipResult
}

type EvidenceFragment struct {
	FragmentID            string
	SubmittedFragmentID   string
	OccurrenceID          string
	CanonicalOwnerID      string
	OccurrenceOwnerID     string
	EvidenceIndex         int
	Content               string
	ContentHash           string
	Authority             string
	SourceID              string
	SourceRevisionID      string
	SupersededEvidenceIDs []string
}

type rLSHelper = postgres.RLSHelper

type LedgerRepositoryImpl struct {
	db                     *gorm.DB
	rls                    rLSHelper
	conflictReviewTTLDays  int
	conflictReviewTimezone string

	rememberIdempotencyLockMu sync.Mutex
	rememberIdempotencyLocks  map[string]*rememberIdempotencyLockEntry
}

var _ LedgerRepository = (*LedgerRepositoryImpl)(nil)

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
				TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID,
			}, input.IngestID, input.FragmentID, input.Reason)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("ledger: append security event: %w", err)
	}
	return eventID, nil
}

func (r *LedgerRepositoryImpl) withTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(*gorm.DB) error) error {
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

func (r *LedgerRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(*gorm.DB) error) error {
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

func (r *LedgerRepositoryImpl) withSystemTx(ctx context.Context, fn func(*gorm.DB) error) error {
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
				    ingest_id, team_id, owner_profile_id, space_id, space_generation,
				    idempotency_key, request_hash, source_summary, status, proposal, metadata
				) VALUES (
				    COALESCE(NULLIF(?, '')::uuid, gen_random_uuid()),
				    ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?::bigint, 0),
				    ?, ?, ?, ?, ?::jsonb, ?::jsonb
				)
				ON CONFLICT (team_id, owner_profile_id, idempotency_key)
				WHERE idempotency_key <> '' AND status <> 'failed'
				DO NOTHING
				RETURNING ingest_id::text, request_hash, true AS created
			)
			SELECT ingest_id, request_hash, created FROM inserted
			UNION ALL
			SELECT ingest_id::text, request_hash, false AS created
			FROM knowledge_ingests
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
			  AND idempotency_key = ? AND status <> 'failed'
			LIMIT 1
		`, input.IngestID, input.TeamID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration,
			input.IdempotencyKey, input.RequestHash, input.SourceSummary, input.Status, string(proposal), string(metadata),
			input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Rows()
		if err != nil {
			return "", false, err
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return "", false, err
			}
			return selectKnowledgeIngestByIdempotency(ctx, tx, input)
		}
		var ingestID, requestHash string
		var created bool
		if err := rows.Scan(&ingestID, &requestHash, &created); err != nil {
			return "", false, err
		}
		if !created && strings.TrimSpace(requestHash) != strings.TrimSpace(input.RequestHash) {
			return "", false, fmt.Errorf("%w: idempotency key reused with a different request hash", ErrIdempotencyConflict)
		}
		return ingestID, created, rows.Err()
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO knowledge_ingests (
		    ingest_id, team_id, owner_profile_id, space_id, space_generation,
		    request_hash, source_summary, status, proposal, metadata
		) VALUES (
		    COALESCE(NULLIF(?, '')::uuid, gen_random_uuid()),
		    ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?::bigint, 0),
		    ?, ?, ?, ?::jsonb, ?::jsonb
		)
		RETURNING ingest_id::text
	`, input.IngestID, input.TeamID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration,
		input.RequestHash, input.SourceSummary, input.Status, string(proposal), string(metadata)).Rows()
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
		if key != ingestMetadataTelemetryOriginKey {
			metadata[key] = value
		}
	}
	if input.TelemetryRemember {
		metadata[ingestMetadataTelemetryOriginKey] = ingestMetadataTelemetryOriginRemember
	}
	return metadata
}

func selectKnowledgeIngestByIdempotency(ctx context.Context, tx *gorm.DB, input CreateIngestInput) (string, bool, error) {
	row := tx.WithContext(ctx).Raw(`
		SELECT ingest_id::text, request_hash
		FROM knowledge_ingests
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
		  AND idempotency_key = ? AND status <> 'failed'
		ORDER BY created_at DESC, ingest_id DESC
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Row()
	var ingestID, requestHash string
	if err := row.Scan(&ingestID, &requestHash); err != nil {
		return "", false, err
	}
	if strings.TrimSpace(requestHash) != strings.TrimSpace(input.RequestHash) {
		return "", false, fmt.Errorf("%w: idempotency key reused with a different request hash", ErrIdempotencyConflict)
	}
	return ingestID, false, nil
}

func insertEvidenceFragment(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string, index int, item EvidenceInput, source *SourceRevisionResult) (EvidenceFragment, error) {
	metadata, err := marshalJSON(item.Metadata)
	if err != nil {
		return EvidenceFragment{}, err
	}
	sourceID, sourceRevisionID := "", ""
	if source != nil {
		sourceID, sourceRevisionID = source.SourceID, source.SourceRevisionID
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_fragments (
		    fragment_id, team_id, ingest_id, owner_profile_id, space_id, space_generation,
		    evidence_index, content, content_hash, source_type, authority, source_ref,
		    source_id, source_revision_id, labels, metadata, force_insert
		) VALUES (
		    COALESCE(NULLIF(?, '')::uuid, gen_random_uuid()), ?::uuid, ?::uuid, ?::uuid,
		    NULLIF(?, '')::uuid, NULLIF(?::bigint, 0), ?, ?, ?, ?, ?, ?,
		    NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?, ?::jsonb, ?
		)
		RETURNING fragment_id::text, authority
	`, item.FragmentID, input.TeamID, ingestID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration,
		index, item.Content, item.ContentHash, item.SourceType, item.Authority, item.SourceRef,
		sourceID, sourceRevisionID, pqStringArray(item.Labels), string(metadata), item.ForceInsert).Rows()
	if err != nil {
		return EvidenceFragment{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return EvidenceFragment{}, sql.ErrNoRows
	}
	fragment := EvidenceFragment{FragmentID: item.FragmentID, SubmittedFragmentID: item.FragmentID, EvidenceIndex: index, Content: item.Content, ContentHash: item.ContentHash, SourceID: sourceID, SourceRevisionID: sourceRevisionID, CanonicalOwnerID: input.OwnerProfileID, OccurrenceOwnerID: input.OwnerProfileID}
	if err := rows.Scan(&fragment.FragmentID, &fragment.Authority); err != nil {
		return EvidenceFragment{}, err
	}
	return fragment, rows.Err()
}
