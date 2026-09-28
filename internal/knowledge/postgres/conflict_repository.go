package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	defaultConflictReviewTTLDays = 7
	defaultConflictTimezone      = "Local"
)

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
	commit CommitSemanticInput,
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
	if err := lockRelationshipConflictSnapshotScope(ctx, tx, teamID, scopeKey); err != nil {
		return nil, err
	}
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
