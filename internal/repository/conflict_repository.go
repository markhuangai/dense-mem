package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	defaultConflictReviewTTLDays = 7
	defaultConflictTimezone      = "Local"
)

type ConflictRuntimeConfig struct {
	ReviewTTLDays int
	Timezone      string
}

type ConflictReviewRunInput struct {
	TeamID       string
	WorkerID     string
	LocalRunDate time.Time
	Timezone     string
	Lease        time.Duration
}

type ConflictReviewRunCompleteInput struct {
	TeamID        string
	ReviewRunID   string
	WorkerID      string
	Status        string
	ClaimedCases  int
	ResolvedCases int
	OverdueCases  int
	NoOpCases     int
	FailedCases   int
	LastError     string
}

type ConflictReviewRunRecord struct {
	TeamID       string
	ReviewRunID  string
	LocalRunDate time.Time
	Status       string
	WorkerID     string
}

type ClaimRelationshipConflictCasesInput struct {
	TeamID              string
	WorkerID            string
	ReviewRunID         string
	Limit               int
	Lease               time.Duration
	MaxAttempts         int
	Now                 time.Time
	ExcludedConflictIDs []string
}

// ReleaseRelationshipConflictCaseClaimInput identifies the worker claim that
// should be returned to the conflict queue after retryable processing fails.
type ReleaseRelationshipConflictCaseClaimInput struct {
	TeamID      string
	ConflictID  string
	WorkerID    string
	ReviewRunID string
	Now         time.Time
}

type ReviewRelationshipConflictCaseInput struct {
	TeamID      string
	WorkerID    string
	ReviewRunID string
	ConflictID  string
	Now         time.Time
}

type ReviewRelationshipConflictCaseResult struct {
	ConflictID           string
	Outcome              string
	Stage                string
	PreferredPositionID  string
	Resolution           *RelationshipConflictResolutionInput
	UpdatedRelationships []string
	RetractedEvidenceIDs []string
	AssessmentAttemptID  string
	ResolutionMethod     string
	ResolutionPending    bool
}

// RelationshipConflictResolutionInput identifies one selected resolution. The
// repository revalidates every field before it mutates durable state.
type RelationshipConflictResolutionInput struct {
	TeamID              string
	ConflictID          string
	ReviewRunID         string
	WorkerID            string
	ExpectedCaseVersion int
	PreferredPositionID string
	AssessmentAttemptID string
	Method              string
	Now                 time.Time
}

type RelationshipConflictResolutionDocument struct {
	TeamID          string
	RelationshipID  string
	OwnerProfileID  string
	SpaceID         string
	SpaceGeneration int64
	SourceVersion   int64
	DocumentHash    string
	DocumentText    string
}

type RelationshipConflictResolutionFence struct {
	EmbeddingContractID     string
	EmbeddingDimensions     int
	EmbeddingModel          string
	SearchIndexGenerationID string
	IndexGeneration         int
}

type RelationshipConflictResolutionPlan struct {
	Resolution          RelationshipConflictResolutionInput
	Fence               RelationshipConflictResolutionFence
	Documents           []RelationshipConflictResolutionDocument
	EffectiveAt         time.Time
	EffectiveTimeBasis  string
	Reason              string
	ResolutionPlanID    string
	Pending             bool
	PendingTransitioned bool
	Stale               bool
}

type RelationshipConflictResolutionEmbedding struct {
	DocumentHash string
	Embedding    []float32
}

type CommitRelationshipConflictResolutionInput struct {
	Plan       RelationshipConflictResolutionPlan
	Embeddings []RelationshipConflictResolutionEmbedding
}

type RelationshipConflictCaseRecord struct {
	TeamID              string
	ConflictID          string
	SpaceID             string `json:"-"`
	SemanticScopeKey    string
	Kind                string
	Status              string
	SubjectEntityID     string
	PredicateKey        string
	PredicateVersion    int
	RelationshipKind    string
	CurrentCardinality  string
	Polarity            string
	ScopeKey            string
	Question            string
	PolicyVersion       string
	ReviewDueAt         time.Time
	NextReviewAt        time.Time
	ReviewTTLDays       int
	Timezone            string
	PreferredPositionID string
	ResolvedAt          *time.Time
	EffectiveAt         *time.Time
	EffectiveTimeBasis  string
	ResolutionReason    string
	Version             int
	Attempts            int
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Positions           []RelationshipConflictPositionRecord
	DismissedAt         *time.Time
}

type ValidateRelationshipConflictContextInput struct {
	TeamID          string
	OwnerProfileID  string
	ConflictID      string
	ExpectedVersion int
}

type conflictPlacement struct {
	scopeKey  string
	question  string
	spaceID   string
	spaceKind string
	rows      []conflictPlacementRow
}

type conflictPlacementRow struct {
	RelationshipID      string
	OwnerProfileID      string
	SpaceID             string
	SpaceGeneration     int64
	SubjectEntityID     string
	PredicateKey        string
	PredicateVersion    int
	RelationshipKind    string
	CurrentCardinality  string
	Polarity            string
	ScopeKey            string
	ObjectEntityID      string
	ObjectValueID       string
	PositionKey         string
	SupportID           string
	VerificationEventID string
	FragmentID          string
	SourceGroupKey      string
	Authority           string
	AcceptedAt          time.Time
	EffectiveAt         *time.Time
	EffectiveTimeBasis  string
	RecordedFallback    bool
}

func normalizeConflictRuntimeConfig(input ConflictRuntimeConfig) ConflictRuntimeConfig {
	input.Timezone = strings.TrimSpace(input.Timezone)
	if input.Timezone == "" {
		input.Timezone = defaultConflictTimezone
	}
	if input.ReviewTTLDays <= 0 {
		input.ReviewTTLDays = defaultConflictReviewTTLDays
	}
	if input.ReviewTTLDays > 30 {
		input.ReviewTTLDays = 30
	}
	return input
}

func relationshipEligibleForConflictPlacement(record *RelationshipRecord) bool {
	if record == nil {
		return false
	}
	return record.Status == string(domain.RelationshipStatusActive) &&
		record.SupportCount > 0 &&
		record.RelationshipKind == string(domain.RelationshipKindState) &&
		record.CurrentCardinality == string(domain.CurrentCardinalityOne)
}

func applyRelationshipConflictPlacement(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	applied *RelationshipDecisionResult,
	config ConflictRuntimeConfig,
) error {
	if applied == nil || !relationshipEligibleForConflictPlacement(applied.Relationship) {
		return nil
	}
	config = normalizeConflictRuntimeConfig(config)
	placement, err := loadRelationshipConflictPlacement(ctx, tx, commit.TeamID, applied.Relationship)
	if err != nil {
		return err
	}
	if !conflictPlacementHasConflict(placement.rows) {
		return nil
	}
	return upsertRelationshipConflictCase(ctx, tx, commit.TeamID, placement, config)
}

func loadRelationshipConflictPlacement(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	source *RelationshipRecord,
) (*conflictPlacement, error) {
	spaceID, spaceKind, err := loadRelationshipConflictSpace(ctx, tx, teamID, source)
	if err != nil {
		return nil, err
	}
	scopeKey := relationshipConflictScopeKey(source, spaceID, spaceKind)
	rows, err := tx.WithContext(ctx).Raw(`
		WITH active_relationships AS (
			SELECT relationship.relationship_id,
			       relationship.owner_profile_id,
			       relationship.space_id::text,
			       relationship.space_generation,
			       relationship.subject_entity_id,
			       relationship.predicate_key,
			       relationship.predicate_version,
			       relationship.relationship_kind,
			       relationship.current_cardinality,
			       relationship.polarity,
			       relationship.scope_key,
			       relationship.object_entity_id,
			       relationship.object_value_id,
			       relationship.valid_from
			FROM relationship_records AS relationship
			WHERE relationship.team_id = ?::uuid
			  AND relationship.subject_entity_id = ?::uuid
			  AND relationship.predicate_key = ?
			  AND relationship.relationship_kind = ?
			  AND relationship.current_cardinality = ?
			  AND relationship.polarity = ?
			  AND relationship.scope_key IS NOT DISTINCT FROM NULLIF(?, '')
			  AND relationship.status = 'active'
			  AND relationship.support_count > 0
			  AND relationship.space_id = ?::uuid
			  AND (relationship.valid_from IS NULL OR relationship.valid_from <= now())
			  AND (relationship.valid_to IS NULL OR relationship.valid_to > now())
		),
		latest_support_decision AS (
			SELECT DISTINCT ON (support.team_id, support.support_id)
			       support.team_id,
			       support.support_id,
			       decision.decision
			FROM active_relationships AS active
			JOIN relationship_evidence_supports AS support
			  ON support.team_id = ?::uuid
			 AND support.relationship_id = active.relationship_id
			JOIN relationship_support_decision_events AS decision
			  ON decision.team_id = support.team_id
			 AND decision.support_id = support.support_id
			ORDER BY support.team_id, support.support_id, decision.created_at DESC, decision.support_decision_id DESC
		),
		effective_supports AS (
			SELECT active.relationship_id,
			       support.support_id,
			       support.verification_event_id,
			       support.fragment_id,
			       support.created_at AS accepted_at,
			       COALESCE(
			           NULLIF(source.source_key, ''),
			           NULLIF(fragment.metadata->>'contract_source_group', ''),
			           NULLIF(fragment.metadata->>'v2_contract_source_group', ''),
			           NULLIF(support.source_group_key, ''),
			           support.support_id::text
			       ) AS source_group_key,
			       support.authority
			FROM active_relationships AS active
			JOIN relationship_evidence_supports AS support
			  ON support.team_id = ?::uuid
			 AND support.relationship_id = active.relationship_id
			JOIN latest_support_decision AS latest
			  ON latest.team_id = support.team_id
			 AND latest.support_id = support.support_id
			 AND latest.decision IN ('grant', 'reinstate')
			JOIN evidence_fragments AS fragment
			  ON fragment.team_id = support.team_id
			 AND fragment.fragment_id = support.fragment_id
			LEFT JOIN evidence_quarantines AS quarantine
			  ON quarantine.team_id = support.team_id
			 AND quarantine.fragment_id = support.fragment_id
			 AND quarantine.status = 'active'
			LEFT JOIN evidence_sources AS source
			  ON source.team_id = support.team_id
			 AND source.source_id = support.source_id
			LEFT JOIN evidence_lifecycle_events AS lifecycle
			  ON lifecycle.team_id = support.team_id
			 AND lifecycle.target_fragment_id = support.fragment_id
			WHERE quarantine.quarantine_id IS NULL
			  AND lifecycle.lifecycle_event_id IS NULL
			  AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
			  AND (
			      support.source_id IS NULL
			      OR source.current_revision_id = support.source_revision_id
			  )
		),
		support_groups AS (
			SELECT DISTINCT ON (support.relationship_id, support.source_group_key)
			       support.relationship_id,
			       support.support_id,
			       support.verification_event_id,
			       support.fragment_id,
			       MAX(support.accepted_at) OVER (
			           PARTITION BY support.relationship_id, support.source_group_key
			       ) AS accepted_at,
			       support.source_group_key,
			       support.authority
			FROM effective_supports AS support
			ORDER BY support.relationship_id,
			         support.source_group_key,
			         CASE support.authority
			             WHEN 'authoritative' THEN 0
			             WHEN 'primary' THEN 1
			             WHEN 'secondary' THEN 2
			             WHEN 'inferred' THEN 3
			             ELSE 4
			         END,
			         support.accepted_at DESC,
				         support.support_id
		)
		SELECT active.relationship_id::text,
		       active.owner_profile_id::text,
		       active.space_id,
		       active.space_generation,
		       active.subject_entity_id::text,
		       active.predicate_key,
		       active.predicate_version,
		       active.relationship_kind,
		       active.current_cardinality,
		       active.polarity,
		       COALESCE(active.scope_key, ''),
		       COALESCE(active.object_entity_id::text, ''),
		       COALESCE(active.object_value_id::text, ''),
		       CASE
		           WHEN active.object_entity_id IS NOT NULL THEN 'entity:' || active.object_entity_id::text
		           ELSE 'value:' || active.object_value_id::text
		       END AS position_key,
		       support.support_id::text,
		       support.verification_event_id::text,
			       support.fragment_id::text,
			       support.accepted_at,
			       support.source_group_key,
		       support.authority,
		       active.valid_from,
		       CASE WHEN active.valid_from IS NULL THEN 'recorded_at' ELSE 'valid_from' END,
		       active.valid_from IS NULL
		FROM active_relationships AS active
		JOIN support_groups AS support
		  ON support.relationship_id = active.relationship_id
		ORDER BY position_key, active.owner_profile_id, active.relationship_id
		`, teamID, source.SubjectEntityID, source.PredicateKey, source.RelationshipKind,
		source.CurrentCardinality, source.Polarity, source.ScopeKey,
		spaceID, teamID, teamID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &conflictPlacement{
		scopeKey: scopeKey,
		question: fmt.Sprintf(
			"Which value is current for predicate %q on subject %s?",
			source.PredicateKey,
			source.SubjectEntityID,
		),
		spaceID:   spaceID,
		spaceKind: spaceKind,
	}
	for rows.Next() {
		var row conflictPlacementRow
		if err := rows.Scan(
			&row.RelationshipID,
			&row.OwnerProfileID,
			&row.SpaceID,
			&row.SpaceGeneration,
			&row.SubjectEntityID,
			&row.PredicateKey,
			&row.PredicateVersion,
			&row.RelationshipKind,
			&row.CurrentCardinality,
			&row.Polarity,
			&row.ScopeKey,
			&row.ObjectEntityID,
			&row.ObjectValueID,
			&row.PositionKey,
			&row.SupportID,
			&row.VerificationEventID,
			&row.FragmentID,
			&row.AcceptedAt,
			&row.SourceGroupKey,
			&row.Authority,
			&row.EffectiveAt,
			&row.EffectiveTimeBasis,
			&row.RecordedFallback,
		); err != nil {
			return nil, err
		}
		out.rows = append(out.rows, row)
	}
	return out, rows.Err()
}

func upsertRelationshipConflictCase(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	placement *conflictPlacement,
	config ConflictRuntimeConfig,
) error {
	if placement == nil || len(placement.rows) == 0 {
		return nil
	}
	first := placement.rows[0]
	now := time.Now().UTC()
	ttlDays, err := loadConflictReviewTTLDays(ctx, tx, teamID, config.ReviewTTLDays)
	if err != nil {
		return err
	}
	reviewDueAt := now.Add(time.Duration(ttlDays) * 24 * time.Hour)
	var conflictID string
	var created bool
	rows, err := tx.WithContext(ctx).Raw(`
		WITH inserted AS (
		INSERT INTO relationship_conflict_cases (
		    team_id, space_id, space_generation, semantic_scope_key, kind, status, subject_entity_id,
			    predicate_key, predicate_version, relationship_kind, current_cardinality,
			    polarity, scope_key, question, policy_version, review_due_at,
			    next_review_at, review_ttl_days, timezone, metadata
			) VALUES (
		    ?::uuid, ?::uuid, ?, ?, 'cross_profile_current_state', 'open', ?::uuid,
			    ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, '{}'::jsonb
			)
			ON CONFLICT (team_id, semantic_scope_key)
			WHERE status IN ('open', 'overdue')
			DO NOTHING
			RETURNING conflict_id::text, true AS created
		)
		SELECT conflict_id, created FROM inserted
		UNION ALL
		SELECT conflict_id::text, false AS created
		FROM relationship_conflict_cases
		WHERE team_id = ?::uuid
		  AND semantic_scope_key = ?
		  AND status IN ('open', 'overdue')
		LIMIT 1
	`, teamID, placement.spaceID, first.SpaceGeneration, placement.scopeKey, first.SubjectEntityID, first.PredicateKey,
		first.PredicateVersion, first.RelationshipKind, first.CurrentCardinality,
		first.Polarity, first.ScopeKey, placement.question, string(domain.ConflictPolicyVersion),
		reviewDueAt, now, ttlDays, config.Timezone,
		teamID, placement.scopeKey).Rows()
	if err != nil {
		return err
	}
	if rows.Next() {
		if err := rows.Scan(&conflictID, &created); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if conflictID == "" {
		return sql.ErrNoRows
	}
	if created {
		if err := appendRelationshipConflictEvent(ctx, tx, teamID, conflictID, "", "", "", string(domain.RelationshipConflictEventOpened), "open", "case:"+conflictID+":opened", map[string]any{
			"semantic_scope_key": placement.scopeKey,
			"policy_version":     domain.ConflictPolicyVersion,
		}); err != nil {
			return err
		}
	}
	changed, err := refreshExistingRelationshipConflictCaseSnapshot(ctx, tx, teamID, conflictID, placement.rows)
	if err != nil {
		return err
	}
	if !created && changed {
		if err := bumpRelationshipConflictCaseVersion(ctx, tx, teamID, conflictID); err != nil {
			return err
		}
	}
	return nil
}

func loadConflictReviewTTLDays(ctx context.Context, tx *gorm.DB, teamID string, defaultDays int) (int, error) {
	var days sql.NullInt64
	if err := tx.WithContext(ctx).Raw(`
		SELECT CASE
		    WHEN COALESCE(config #>> '{conflict_review,review_ttl_days}', '') ~ '^[0-9]+$'
		    THEN (config #>> '{conflict_review,review_ttl_days}')::int
		    ELSE NULL
		END
		FROM teams
		WHERE id = ?::uuid
	`, teamID).Scan(&days).Error; err != nil {
		return 0, err
	}
	if days.Valid && days.Int64 >= 1 && days.Int64 <= 30 {
		return int(days.Int64), nil
	}
	if defaultDays < 1 {
		return defaultConflictReviewTTLDays, nil
	}
	if defaultDays > 30 {
		return 30, nil
	}
	return defaultDays, nil
}

func bumpRelationshipConflictCaseVersion(ctx context.Context, tx *gorm.DB, teamID string, conflictID string) error {
	if err := supersedeReservedOverdueConflictAssessments(ctx, tx, teamID, conflictID); err != nil {
		return err
	}
	if err := supersedePendingOverdueConflictResolutions(ctx, tx, teamID, conflictID); err != nil {
		return err
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE relationship_conflict_cases
		SET version = version + 1,
		    attempts = 0,
		    lease_worker_id = '',
		    lease_until = NULL,
		    last_error = '',
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND status IN ('open', 'overdue')
	`, teamID, conflictID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func appendRelationshipConflictEvent(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	positionID string,
	relationshipID string,
	ownerProfileID string,
	action string,
	outcome string,
	idempotencyKey string,
	metadata map[string]any,
) error {
	if strings.TrimSpace(conflictID) == "" {
		return errors.New("conflict_id is required")
	}
	metadataJSON, err := marshalJSON(metadata)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO relationship_conflict_events (
		    team_id, conflict_id, position_id, relationship_id, owner_profile_id,
		    action, outcome, actor_kind, policy_version, idempotency_key, metadata,
		    space_id, space_generation
		) VALUES (
		    ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid,
		    ?, ?, 'system', ?, ?, ?::jsonb,
		    (SELECT space_id FROM relationship_conflict_cases
		     WHERE team_id = ?::uuid AND conflict_id = ?::uuid),
		    (SELECT space_generation FROM relationship_conflict_cases
		     WHERE team_id = ?::uuid AND conflict_id = ?::uuid)
		)
		ON CONFLICT (team_id, idempotency_key)
		WHERE idempotency_key <> ''
		DO NOTHING
	`, teamID, conflictID, positionID, relationshipID, ownerProfileID,
		action, outcome, string(domain.ConflictPolicyVersion), idempotencyKey,
		string(metadataJSON), teamID, conflictID, teamID, conflictID).Error
}

func loadRelationshipConflictRecords(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipIDs []string,
	knownAt *time.Time,
) ([]RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictRecordsInSpace(ctx, tx, teamID, relationshipIDs, knownAt, "")
}

func loadRelationshipConflictRecordsInSpace(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipIDs []string,
	knownAt *time.Time,
	spaceID string,
) ([]RelationshipConflictCaseRecord, error) {
	relationshipIDs = normalizeRecallUUIDList(relationshipIDs)
	if len(relationshipIDs) == 0 {
		return []RelationshipConflictCaseRecord{}, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT DISTINCT conflict.conflict_id::text
		FROM relationship_conflict_cases AS conflict
		JOIN relationship_conflict_position_members AS member
		  ON member.team_id = conflict.team_id
		 AND member.conflict_id = conflict.conflict_id
		WHERE conflict.team_id = ?::uuid
		  AND member.relationship_id = ANY(?::uuid[])
		  AND (
		      ? = ''
		      OR (
		          conflict.space_id = NULLIF(?, '')::uuid
		          AND member.space_id = NULLIF(?, '')::uuid
		      )
		  )
		  AND (?::timestamptz IS NULL OR conflict.created_at <= ?::timestamptz)
		  AND (
		      (?::timestamptz IS NULL AND member.active)
		      OR (
		          ?::timestamptz IS NOT NULL
		          AND member.first_seen_at <= ?::timestamptz
		          AND (member.retired_at IS NULL OR member.retired_at > ?::timestamptz)
		      )
		  )
		  AND (
		      (?::timestamptz IS NULL AND conflict.status IN ('open', 'overdue', 'resolved'))
		      OR (?::timestamptz IS NOT NULL AND conflict.status IN ('open', 'overdue', 'resolved', 'dismissed'))
		  )
		ORDER BY conflict.conflict_id::text
	`, teamID, pq.Array(relationshipIDs), spaceID, spaceID, spaceID,
		knownAt, knownAt, knownAt, knownAt, knownAt, knownAt, knownAt, knownAt).Rows()
	if err != nil {
		return nil, err
	}
	conflictIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		conflictIDs = append(conflictIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return loadRelationshipConflictRecordsByID(ctx, tx, teamID, conflictIDs, knownAt)
}

func loadRelationshipConflictRecordsByID(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
) ([]RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictRecordsByIDBounded(ctx, tx, teamID, conflictIDs, knownAt, 0, relationshipConflictSupporterLimit)
}

func loadRelationshipConflictRecordsByIDBounded(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	positionLimit int,
	supporterLimit int,
) ([]RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictRecordsByIDBoundedWithFence(ctx, tx, teamID, conflictIDs, knownAt, positionLimit, supporterLimit, false)
}

func loadRelationshipConflictPositionRows(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
) ([]RelationshipConflictPositionRecord, error) {
	return loadRelationshipConflictPositionRowsWithLimit(ctx, tx, teamID, conflictIDs, knownAt, 0)
}

func loadRelationshipConflictPositionRowsWithLimit(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	positionLimit int,
) ([]RelationshipConflictPositionRecord, error) {
	return loadRelationshipConflictPositionRowsWithLimitAndFence(ctx, tx, teamID, conflictIDs, knownAt, positionLimit, false)
}

func loadRelationshipConflictPositionRowsWithLimitAndFence(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	positionLimit int,
	activeOnly bool,
) ([]RelationshipConflictPositionRecord, error) {
	dispositionSelect := "position.disposition"
	dispositionGroup := ", position.disposition"
	if knownAt != nil {
		dispositionSelect = "'candidate'"
		dispositionGroup = ""
	}
	activeMemberFence := ""
	activePositionFence := ""
	if activeOnly {
		activeMemberFence = "\n\t\t\t AND " + activeSemanticSpaceGenerationSQL("member")
		activePositionFence = "\n\t\t\t  AND " + activeSemanticSpaceGenerationSQL("position")
	}
	rows, err := tx.WithContext(ctx).Raw(fmt.Sprintf(`
		WITH grouped AS (
			SELECT position.conflict_id::text AS conflict_id,
			       position.position_id::text AS position_id,
			       position.position_key,
			       COALESCE(position.object_entity_id::text, '') AS object_entity_id,
			       COALESCE(position.object_value_id::text, '') AS object_value_id,
			       %s AS disposition,
			       COALESCE(array_remove(array_agg(DISTINCT member.relationship_id::text ORDER BY member.relationship_id::text), NULL), ARRAY[]::text[]) AS relationship_ids,
			       COALESCE(array_remove(array_agg(DISTINCT member.owner_profile_id::text ORDER BY member.owner_profile_id::text), NULL), ARRAY[]::text[]) AS owner_profile_ids,
			       COALESCE(array_remove(array_agg(DISTINCT member.fragment_id::text ORDER BY member.fragment_id::text), NULL), ARRAY[]::text[]) AS evidence_ids,
			       max(member.effective_at) AS effective_at,
			       max(member.effective_time_basis) AS effective_time_basis,
			       COALESCE(bool_and(member.recorded_fallback), false) AS recorded_fallback
			FROM relationship_conflict_positions AS position
			LEFT JOIN relationship_conflict_position_members AS member
			 ON member.team_id = position.team_id
			 AND member.position_id = position.position_id
				 AND (
				     (?::timestamptz IS NULL AND member.active)
				     OR (
			         ?::timestamptz IS NOT NULL
			         AND member.first_seen_at <= ?::timestamptz
				         AND (member.retired_at IS NULL OR member.retired_at > ?::timestamptz)
				     )
				 )
				 %s
			WHERE position.team_id = ?::uuid
			  AND position.conflict_id = ANY(?::uuid[])
			  AND (
			      (?::timestamptz IS NULL AND position.active)
			      OR (
			          ?::timestamptz IS NOT NULL
			          AND position.first_seen_at <= ?::timestamptz
			          AND (position.retired_at IS NULL OR position.retired_at > ?::timestamptz)
			      )
			  )
			  %s
			GROUP BY position.conflict_id, position.position_id, position.position_key,
			         position.object_entity_id, position.object_value_id%s
		), ranked AS (
			SELECT grouped.*,
			       COUNT(*) OVER (PARTITION BY conflict_id)::int AS position_count,
			       row_number() OVER (PARTITION BY conflict_id ORDER BY position_key, position_id) AS position_rank
			FROM grouped
		)
		SELECT conflict_id, position_id, position_key, object_entity_id, object_value_id,
		       disposition,
		       relationship_ids, owner_profile_ids, evidence_ids, effective_at,
		       effective_time_basis, recorded_fallback, position_count
		FROM ranked
		WHERE ?::int <= 0 OR position_rank <= ?::int
		ORDER BY conflict_id, position_key, position_id
		`, dispositionSelect, activeMemberFence, activePositionFence, dispositionGroup),
		knownAt, knownAt, knownAt, knownAt,
		teamID, pq.Array(conflictIDs),
		knownAt, knownAt, knownAt, knownAt,
		positionLimit, positionLimit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RelationshipConflictPositionRecord{}
	for rows.Next() {
		var relationshipIDs, ownerProfileIDs, evidenceIDs pq.StringArray
		var record RelationshipConflictPositionRecord
		if err := rows.Scan(
			&record.ConflictID,
			&record.PositionID,
			&record.PositionKey,
			&record.ObjectEntityID,
			&record.ObjectValueID,
			&record.Disposition,
			&relationshipIDs,
			&ownerProfileIDs,
			&evidenceIDs,
			&record.EffectiveAt,
			&record.EffectiveTimeBasis,
			&record.RecordedFallback,
			&record.PositionCount,
		); err != nil {
			return nil, err
		}
		record.RelationshipIDs = []string(relationshipIDs)
		record.OwnerProfileIDs = []string(ownerProfileIDs)
		record.EvidenceIDs = []string(evidenceIDs)
		record.PositionsTruncated = positionLimit > 0 && record.PositionCount > positionLimit
		out = append(out, record)
	}
	return out, rows.Err()
}

func positionsForConflict(
	conflictID string,
	positions []RelationshipConflictPositionRecord,
) []RelationshipConflictPositionRecord {
	out := []RelationshipConflictPositionRecord{}
	for _, position := range positions {
		if position.ConflictID == conflictID {
			out = append(out, position)
		}
	}
	return out
}

func applyConflictKnownAt(record *RelationshipConflictCaseRecord, knownAt *time.Time) {
	if record == nil || knownAt == nil {
		return
	}
	rewound := false
	if record.ResolvedAt != nil && record.ResolvedAt.After(*knownAt) {
		if knownAt.Before(record.ReviewDueAt) {
			record.Status = string(domain.RelationshipConflictOpen)
		} else {
			record.Status = string(domain.RelationshipConflictOverdue)
		}
		record.PreferredPositionID = ""
		record.ResolvedAt = nil
		record.EffectiveAt = nil
		record.EffectiveTimeBasis = ""
		record.ResolutionReason = ""
		record.DismissedAt = nil
		rewound = true
	}
	if record.Status == string(domain.RelationshipConflictOverdue) && knownAt.Before(record.ReviewDueAt) {
		record.Status = string(domain.RelationshipConflictOpen)
		rewound = true
	}
	if record.Status == string(domain.RelationshipConflictDismissed) && conflictDismissedAfterKnownAt(record, knownAt) {
		record.DismissedAt = nil
		if record.ResolvedAt != nil && !record.ResolvedAt.After(*knownAt) {
			record.Status = string(domain.RelationshipConflictResolved)
			rewound = true
			applyConflictKnownAtNextReview(record, knownAt, rewound)
			return
		}
		if knownAt.Before(record.ReviewDueAt) {
			record.Status = string(domain.RelationshipConflictOpen)
		} else {
			record.Status = string(domain.RelationshipConflictOverdue)
		}
		record.PreferredPositionID = ""
		record.ResolvedAt = nil
		record.EffectiveAt = nil
		record.EffectiveTimeBasis = ""
		record.ResolutionReason = ""
		rewound = true
	}
	applyConflictKnownAtNextReview(record, knownAt, rewound)
}

func applyConflictKnownAtNextReview(record *RelationshipConflictCaseRecord, knownAt *time.Time, rewound bool) {
	if record == nil || knownAt == nil || !rewound {
		return
	}
	record.NextReviewAt = time.Time{}
}

func applyConflictPositionKnownAtDispositions(record *RelationshipConflictCaseRecord, knownAt *time.Time) {
	if record == nil || knownAt == nil {
		return
	}
	switch record.Status {
	case string(domain.RelationshipConflictResolved):
		for i := range record.Positions {
			if record.Positions[i].PositionID == record.PreferredPositionID {
				record.Positions[i].Disposition = string(domain.RelationshipConflictPositionPreferred)
			} else {
				record.Positions[i].Disposition = string(domain.RelationshipConflictPositionSuppressedCurrent)
			}
		}
	case string(domain.RelationshipConflictOpen), string(domain.RelationshipConflictOverdue):
		for i := range record.Positions {
			record.Positions[i].Disposition = string(domain.RelationshipConflictPositionCandidate)
		}
	}
}

func conflictDismissedAfterKnownAt(record *RelationshipConflictCaseRecord, knownAt *time.Time) bool {
	if record == nil || knownAt == nil {
		return false
	}
	if record.DismissedAt != nil {
		return record.DismissedAt.After(*knownAt)
	}
	return record.UpdatedAt.After(*knownAt)
}
