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
	defaultV2ConflictReviewTTLDays = 7
	defaultV2ConflictTimezone      = "Local"
)

type V2ConflictRuntimeConfig struct {
	ReviewTTLDays int
	Timezone      string
}

type V2ConflictReviewRunInput struct {
	TeamID       string
	WorkerID     string
	LocalRunDate time.Time
	Timezone     string
	Lease        time.Duration
}

type V2ConflictReviewRunCompleteInput struct {
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

type V2ConflictReviewRunRecord struct {
	TeamID       string
	ReviewRunID  string
	LocalRunDate time.Time
	Status       string
	WorkerID     string
}

type V2ClaimRelationshipConflictCasesInput struct {
	TeamID              string
	WorkerID            string
	ReviewRunID         string
	Limit               int
	Lease               time.Duration
	MaxAttempts         int
	Now                 time.Time
	ExcludedConflictIDs []string
}

type V2ReviewRelationshipConflictCaseInput struct {
	TeamID      string
	WorkerID    string
	ReviewRunID string
	ConflictID  string
	Now         time.Time
}

type V2ReviewRelationshipConflictCaseResult struct {
	ConflictID           string
	Outcome              string
	Stage                string
	PreferredPositionID  string
	UpdatedRelationships []string
}

type V2RelationshipConflictCaseRecord struct {
	TeamID              string
	ConflictID          string
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
	Positions           []V2RelationshipConflictPositionRecord
	DismissedAt         *time.Time
}

type V2ValidateRelationshipConflictContextInput struct {
	TeamID          string
	OwnerProfileID  string
	ConflictID      string
	ExpectedVersion int
}

type v2ConflictPlacement struct {
	scopeKey string
	question string
	rows     []v2ConflictPlacementRow
}

type v2ConflictPlacementRow struct {
	RelationshipID          string
	OwnerProfileID          string
	SubjectEntityID         string
	PredicateKey            string
	PredicateVersion        int
	RelationshipKind        string
	CurrentCardinality      string
	Polarity                string
	ScopeKey                string
	ObjectEntityID          string
	ObjectValueID           string
	PositionKey             string
	SupportID               string
	VerificationEventID     string
	FragmentID              string
	SourceGroupKey          string
	Authority               string
	EffectiveAt             *time.Time
	EffectiveTimeBasis      string
	RecordedFallback        bool
	SupportGroupCount       int
	AuthoritativeGroupCount int
}

func normalizeV2ConflictRuntimeConfig(input V2ConflictRuntimeConfig) V2ConflictRuntimeConfig {
	input.Timezone = strings.TrimSpace(input.Timezone)
	if input.Timezone == "" {
		input.Timezone = defaultV2ConflictTimezone
	}
	if input.ReviewTTLDays <= 0 {
		input.ReviewTTLDays = defaultV2ConflictReviewTTLDays
	}
	if input.ReviewTTLDays > 30 {
		input.ReviewTTLDays = 30
	}
	return input
}

func v2RelationshipEligibleForConflictPlacement(record *V2RelationshipRecord) bool {
	if record == nil {
		return false
	}
	return record.Status == string(domain.V2RelationshipStatusActive) &&
		(record.Tier == string(domain.V2RelationshipTierValidatedClaim) || record.Tier == string(domain.V2RelationshipTierFact)) &&
		record.RelationshipKind == string(domain.V2RelationshipKindState) &&
		record.CurrentCardinality == string(domain.V2CurrentCardinalityOne)
}

func applyV2RelationshipConflictPlacement(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	applied *V2RelationshipDecisionResult,
	config V2ConflictRuntimeConfig,
) error {
	if applied == nil || !v2RelationshipEligibleForConflictPlacement(applied.Relationship) {
		return nil
	}
	config = normalizeV2ConflictRuntimeConfig(config)
	placement, err := loadV2RelationshipConflictPlacement(ctx, tx, commit.TeamID, applied.Relationship)
	if err != nil {
		return err
	}
	if !v2ConflictPlacementHasConflict(placement.rows) {
		return nil
	}
	return upsertV2RelationshipConflictCase(ctx, tx, commit.TeamID, placement, config)
}

func loadV2RelationshipConflictPlacement(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	source *V2RelationshipRecord,
) (*v2ConflictPlacement, error) {
	scopeKey := v2RelationshipConflictScopeKey(source)
	rows, err := tx.WithContext(ctx).Raw(`
		WITH active_relationships AS (
			SELECT relationship.relationship_id,
			       relationship.owner_profile_id,
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
			  AND relationship.tier IN ('validated_claim', 'fact')
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
			       COALESCE(
			           NULLIF(source.source_key, ''),
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
			WHERE quarantine.quarantine_id IS NULL
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
			       support.source_group_key,
			       support.authority
			FROM effective_supports AS support
			ORDER BY support.relationship_id,
			         support.source_group_key,
			         CASE WHEN support.authority = 'authoritative' THEN 0 ELSE 1 END,
			         support.support_id
		),
		position_counts AS (
			SELECT active.object_entity_id,
			       active.object_value_id,
			       COUNT(DISTINCT support.source_group_key)::int AS support_group_count,
			       COUNT(DISTINCT support.source_group_key) FILTER (WHERE support.authority = 'authoritative')::int AS authoritative_group_count
			FROM active_relationships AS active
			JOIN support_groups AS support
			  ON support.relationship_id = active.relationship_id
			GROUP BY active.object_entity_id, active.object_value_id
		)
		SELECT active.relationship_id::text,
		       active.owner_profile_id::text,
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
		       support.source_group_key,
		       support.authority,
		       active.valid_from,
		       CASE WHEN active.valid_from IS NULL THEN 'recorded_at' ELSE 'valid_from' END,
		       active.valid_from IS NULL,
		       counts.support_group_count,
		       counts.authoritative_group_count
		FROM active_relationships AS active
		JOIN support_groups AS support
		  ON support.relationship_id = active.relationship_id
		JOIN position_counts AS counts
		  ON counts.object_entity_id IS NOT DISTINCT FROM active.object_entity_id
		 AND counts.object_value_id IS NOT DISTINCT FROM active.object_value_id
		ORDER BY position_key, active.owner_profile_id, active.relationship_id
	`, teamID, source.SubjectEntityID, source.PredicateKey, source.RelationshipKind,
		source.CurrentCardinality, source.Polarity, source.ScopeKey,
		teamID, teamID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &v2ConflictPlacement{
		scopeKey: scopeKey,
		question: fmt.Sprintf(
			"Which value is current for predicate %q on subject %s?",
			source.PredicateKey,
			source.SubjectEntityID,
		),
	}
	for rows.Next() {
		var row v2ConflictPlacementRow
		if err := rows.Scan(
			&row.RelationshipID,
			&row.OwnerProfileID,
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
			&row.SourceGroupKey,
			&row.Authority,
			&row.EffectiveAt,
			&row.EffectiveTimeBasis,
			&row.RecordedFallback,
			&row.SupportGroupCount,
			&row.AuthoritativeGroupCount,
		); err != nil {
			return nil, err
		}
		out.rows = append(out.rows, row)
	}
	return out, rows.Err()
}

func v2ConflictPlacementHasConflict(rows []v2ConflictPlacementRow) bool {
	positions := map[string]struct{}{}
	owners := map[string]struct{}{}
	for _, row := range rows {
		if row.PositionKey != "" {
			positions[row.PositionKey] = struct{}{}
		}
		if row.OwnerProfileID != "" {
			owners[row.OwnerProfileID] = struct{}{}
		}
	}
	return len(positions) >= 2 && len(owners) >= 2
}

func v2RelationshipConflictScopeKey(record *V2RelationshipRecord) string {
	parts := []string{
		"cross_profile_current_state",
		record.TeamID,
		record.SubjectEntityID,
		record.PredicateKey,
		record.RelationshipKind,
		record.CurrentCardinality,
		record.Polarity,
		record.ScopeKey,
	}
	return "rc:" + strings.TrimPrefix(sha256Hex(strings.Join(parts, "\x00")), "sha256:")
}

func upsertV2RelationshipConflictCase(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	placement *v2ConflictPlacement,
	config V2ConflictRuntimeConfig,
) error {
	if placement == nil || len(placement.rows) == 0 {
		return nil
	}
	first := placement.rows[0]
	now := time.Now().UTC()
	ttlDays, err := loadV2ConflictReviewTTLDays(ctx, tx, teamID, config.ReviewTTLDays)
	if err != nil {
		return err
	}
	reviewDueAt := now.Add(time.Duration(ttlDays) * 24 * time.Hour)
	var conflictID string
	var created bool
	rows, err := tx.WithContext(ctx).Raw(`
		WITH inserted AS (
			INSERT INTO relationship_conflict_cases (
			    team_id, semantic_scope_key, kind, status, subject_entity_id,
			    predicate_key, predicate_version, relationship_kind, current_cardinality,
			    polarity, scope_key, question, policy_version, review_due_at,
			    next_review_at, review_ttl_days, timezone, metadata
			) VALUES (
			    ?::uuid, ?, 'cross_profile_current_state', 'open', ?::uuid,
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
	`, teamID, placement.scopeKey, first.SubjectEntityID, first.PredicateKey,
		first.PredicateVersion, first.RelationshipKind, first.CurrentCardinality,
		first.Polarity, first.ScopeKey, placement.question, string(domain.V2ConflictPolicyVersion),
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
		if err := appendV2RelationshipConflictEvent(ctx, tx, teamID, conflictID, "", "", "", string(domain.V2RelationshipConflictEventOpened), "open", "case:"+conflictID+":opened", map[string]any{
			"semantic_scope_key": placement.scopeKey,
			"policy_version":     domain.V2ConflictPolicyVersion,
		}); err != nil {
			return err
		}
	}
	changed, err := refreshV2ExistingRelationshipConflictCaseSnapshot(ctx, tx, teamID, conflictID, placement.rows)
	if err != nil {
		return err
	}
	if !created && changed {
		if err := bumpV2RelationshipConflictCaseVersion(ctx, tx, teamID, conflictID); err != nil {
			return err
		}
	}
	return nil
}

func loadV2ConflictReviewTTLDays(ctx context.Context, tx *gorm.DB, teamID string, defaultDays int) (int, error) {
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
		return defaultV2ConflictReviewTTLDays, nil
	}
	if defaultDays > 30 {
		return 30, nil
	}
	return defaultDays, nil
}

func bumpV2RelationshipConflictCaseVersion(ctx context.Context, tx *gorm.DB, teamID string, conflictID string) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE relationship_conflict_cases
		SET version = version + 1,
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

func appendV2RelationshipConflictEvent(
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
	metadataJSON, err := marshalV2JSON(metadata)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO relationship_conflict_events (
		    team_id, conflict_id, position_id, relationship_id, owner_profile_id,
		    action, outcome, actor_kind, policy_version, idempotency_key, metadata
		) VALUES (
		    ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid,
		    ?, ?, 'system', ?, ?, ?::jsonb
		)
		ON CONFLICT (team_id, idempotency_key)
		WHERE idempotency_key <> ''
		DO NOTHING
	`, teamID, conflictID, positionID, relationshipID, ownerProfileID,
		action, outcome, string(domain.V2ConflictPolicyVersion), idempotencyKey,
		string(metadataJSON)).Error
}

func loadV2RelationshipConflictRecords(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipIDs []string,
	knownAt *time.Time,
) ([]V2RelationshipConflictCaseRecord, error) {
	relationshipIDs = normalizeV2RecallUUIDList(relationshipIDs)
	if len(relationshipIDs) == 0 {
		return []V2RelationshipConflictCaseRecord{}, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT DISTINCT conflict.conflict_id::text
		FROM relationship_conflict_cases AS conflict
		JOIN relationship_conflict_position_members AS member
		  ON member.team_id = conflict.team_id
		 AND member.conflict_id = conflict.conflict_id
		WHERE conflict.team_id = ?::uuid
		  AND member.relationship_id = ANY(?::uuid[])
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
	`, teamID, pq.Array(relationshipIDs),
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
	return loadV2RelationshipConflictRecordsByID(ctx, tx, teamID, conflictIDs, knownAt)
}

func loadV2RelationshipConflictRecordsByID(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
) ([]V2RelationshipConflictCaseRecord, error) {
	conflictIDs = normalizeV2RecallUUIDList(conflictIDs)
	if len(conflictIDs) == 0 {
		return []V2RelationshipConflictCaseRecord{}, nil
	}
	cases, err := loadV2RelationshipConflictCaseRows(ctx, tx, teamID, conflictIDs, knownAt)
	if err != nil {
		return nil, err
	}
	positions, err := loadV2RelationshipConflictPositionRows(ctx, tx, teamID, conflictIDs, knownAt)
	if err != nil {
		return nil, err
	}
	for i := range cases {
		cases[i].Positions = positionsForV2Conflict(cases[i].ConflictID, positions)
		applyV2ConflictPositionKnownAtDispositions(&cases[i], knownAt)
	}
	return cases, nil
}

func loadV2RelationshipConflictCaseRows(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
) ([]V2RelationshipConflictCaseRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, conflict_id::text, semantic_scope_key, kind, status,
		       subject_entity_id::text, predicate_key, predicate_version,
		       relationship_kind, current_cardinality, polarity, COALESCE(scope_key, ''),
		       question, policy_version, review_due_at, next_review_at, review_ttl_days,
		       timezone, COALESCE(preferred_position_id::text, ''),
		       resolved_at, effective_at, effective_time_basis, resolution_reason,
		       version, attempts, created_at, updated_at,
		       (
		           SELECT max(event.created_at)
		           FROM relationship_conflict_events AS event
		           WHERE event.team_id = relationship_conflict_cases.team_id
		             AND event.conflict_id = relationship_conflict_cases.conflict_id
		             AND event.action = 'dismissed'
		       ) AS dismissed_at
		FROM relationship_conflict_cases
		WHERE team_id = ?::uuid
		  AND conflict_id = ANY(?::uuid[])
		  AND (?::timestamptz IS NULL OR created_at <= ?::timestamptz)
		ORDER BY created_at, conflict_id
	`, teamID, pq.Array(conflictIDs), knownAt, knownAt).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []V2RelationshipConflictCaseRecord{}
	for rows.Next() {
		var record V2RelationshipConflictCaseRecord
		if err := rows.Scan(
			&record.TeamID,
			&record.ConflictID,
			&record.SemanticScopeKey,
			&record.Kind,
			&record.Status,
			&record.SubjectEntityID,
			&record.PredicateKey,
			&record.PredicateVersion,
			&record.RelationshipKind,
			&record.CurrentCardinality,
			&record.Polarity,
			&record.ScopeKey,
			&record.Question,
			&record.PolicyVersion,
			&record.ReviewDueAt,
			&record.NextReviewAt,
			&record.ReviewTTLDays,
			&record.Timezone,
			&record.PreferredPositionID,
			&record.ResolvedAt,
			&record.EffectiveAt,
			&record.EffectiveTimeBasis,
			&record.ResolutionReason,
			&record.Version,
			&record.Attempts,
			&record.CreatedAt,
			&record.UpdatedAt,
			&record.DismissedAt,
		); err != nil {
			return nil, err
		}
		applyV2ConflictKnownAt(&record, knownAt)
		out = append(out, record)
	}
	return out, rows.Err()
}

func loadV2RelationshipConflictPositionRows(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
) ([]V2RelationshipConflictPositionRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT position.conflict_id::text,
		       position.position_id::text,
		       position.position_key,
		       COALESCE(position.object_entity_id::text, ''),
		       COALESCE(position.object_value_id::text, ''),
		       position.disposition,
		       position.support_group_count,
		       position.authoritative_group_count,
		       COALESCE(array_remove(array_agg(DISTINCT member.relationship_id::text ORDER BY member.relationship_id::text), NULL), ARRAY[]::text[]),
		       COALESCE(array_remove(array_agg(DISTINCT member.owner_profile_id::text ORDER BY member.owner_profile_id::text), NULL), ARRAY[]::text[]),
			       COALESCE(array_remove(array_agg(DISTINCT member.fragment_id::text ORDER BY member.fragment_id::text), NULL), ARRAY[]::text[]),
			       max(member.effective_at),
			       max(member.effective_time_basis),
			       COALESCE(bool_and(member.recorded_fallback), false)
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
			GROUP BY position.conflict_id, position.position_id, position.position_key,
			         position.object_entity_id, position.object_value_id, position.disposition,
			         position.support_group_count, position.authoritative_group_count
			ORDER BY position.conflict_id, position.position_key
		`, knownAt, knownAt, knownAt, knownAt,
		teamID, pq.Array(conflictIDs),
		knownAt, knownAt, knownAt, knownAt).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []V2RelationshipConflictPositionRecord{}
	for rows.Next() {
		var relationshipIDs, ownerProfileIDs, evidenceIDs pq.StringArray
		var record V2RelationshipConflictPositionRecord
		if err := rows.Scan(
			&record.ConflictID,
			&record.PositionID,
			&record.PositionKey,
			&record.ObjectEntityID,
			&record.ObjectValueID,
			&record.Disposition,
			&record.SupportGroupCount,
			&record.AuthoritativeGroupCount,
			&relationshipIDs,
			&ownerProfileIDs,
			&evidenceIDs,
			&record.EffectiveAt,
			&record.EffectiveTimeBasis,
			&record.RecordedFallback,
		); err != nil {
			return nil, err
		}
		record.RelationshipIDs = []string(relationshipIDs)
		record.OwnerProfileIDs = []string(ownerProfileIDs)
		record.EvidenceIDs = []string(evidenceIDs)
		out = append(out, record)
	}
	return out, rows.Err()
}

func positionsForV2Conflict(
	conflictID string,
	positions []V2RelationshipConflictPositionRecord,
) []V2RelationshipConflictPositionRecord {
	out := []V2RelationshipConflictPositionRecord{}
	for _, position := range positions {
		if position.ConflictID == conflictID {
			out = append(out, position)
		}
	}
	return out
}

func applyV2ConflictKnownAt(record *V2RelationshipConflictCaseRecord, knownAt *time.Time) {
	if record == nil || knownAt == nil {
		return
	}
	if record.ResolvedAt != nil && record.ResolvedAt.After(*knownAt) {
		if knownAt.Before(record.ReviewDueAt) {
			record.Status = string(domain.V2RelationshipConflictOpen)
		} else {
			record.Status = string(domain.V2RelationshipConflictOverdue)
		}
		record.PreferredPositionID = ""
		record.ResolvedAt = nil
		record.EffectiveAt = nil
		record.EffectiveTimeBasis = ""
		record.ResolutionReason = ""
	}
	if record.Status == string(domain.V2RelationshipConflictDismissed) && v2ConflictDismissedAfterKnownAt(record, knownAt) {
		record.DismissedAt = nil
		if record.ResolvedAt != nil && !record.ResolvedAt.After(*knownAt) {
			record.Status = string(domain.V2RelationshipConflictResolved)
			return
		}
		if knownAt.Before(record.ReviewDueAt) {
			record.Status = string(domain.V2RelationshipConflictOpen)
		} else {
			record.Status = string(domain.V2RelationshipConflictOverdue)
		}
		record.PreferredPositionID = ""
		record.ResolvedAt = nil
		record.EffectiveAt = nil
		record.EffectiveTimeBasis = ""
		record.ResolutionReason = ""
	}
}

func applyV2ConflictPositionKnownAtDispositions(record *V2RelationshipConflictCaseRecord, knownAt *time.Time) {
	if record == nil || knownAt == nil {
		return
	}
	switch record.Status {
	case string(domain.V2RelationshipConflictResolved):
		for i := range record.Positions {
			if record.Positions[i].PositionID == record.PreferredPositionID {
				record.Positions[i].Disposition = string(domain.V2RelationshipConflictPositionPreferred)
			} else {
				record.Positions[i].Disposition = string(domain.V2RelationshipConflictPositionSuppressedCurrent)
			}
		}
	case string(domain.V2RelationshipConflictOpen), string(domain.V2RelationshipConflictOverdue):
		for i := range record.Positions {
			record.Positions[i].Disposition = string(domain.V2RelationshipConflictPositionCandidate)
		}
	}
}

func v2ConflictDismissedAfterKnownAt(record *V2RelationshipConflictCaseRecord, knownAt *time.Time) bool {
	if record == nil || knownAt == nil {
		return false
	}
	if record.DismissedAt != nil {
		return record.DismissedAt.After(*knownAt)
	}
	return record.UpdatedAt.After(*knownAt)
}
