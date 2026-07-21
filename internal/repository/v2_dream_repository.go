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
	"github.com/lib/pq"
	"gorm.io/gorm"
)

var (
	ErrV2DreamCycleAlreadyClaimed     = errors.New("v2 dream cycle already claimed")
	ErrV2DreamHypothesisNotFound      = errors.New("v2 dream hypothesis not found")
	ErrV2DreamSourceStale             = errors.New("v2 dream source is stale")
	ErrV2DreamExactRelationshipExists = errors.New("v2 dream exact relationship already exists")
)

type V2DreamRepository interface {
	ClaimV2DreamCycle(ctx context.Context, input V2DreamCycleClaimInput) (*V2DreamCycleRun, error)
	CompleteV2DreamCycle(ctx context.Context, input V2DreamCycleCompleteInput) error
	ListV2DreamInputs(ctx context.Context, input V2DreamInputListInput) ([]V2DreamInput, error)
	UpsertV2Hypothesis(ctx context.Context, input V2UpsertHypothesisInput) (*V2HypothesisRecord, bool, error)
	ListV2Hypotheses(ctx context.Context, input V2ListHypothesesInput) ([]V2HypothesisRecord, string, error)
	GetV2Hypothesis(ctx context.Context, input V2GetHypothesisInput) (*V2HypothesisRecord, error)
	RecallV2Hypotheses(ctx context.Context, input V2RecallHypothesesInput) ([]V2HypothesisRecord, error)
	RefreshV2HypothesisStaleness(ctx context.Context, input V2RefreshHypothesisStalenessInput) (int, error)
	UpdateV2HypothesisStatus(ctx context.Context, input V2UpdateHypothesisStatusInput) (*V2HypothesisRecord, error)
	SubmitV2Hypothesis(ctx context.Context, input V2SubmitHypothesisInput) (*V2HypothesisRecord, error)
	LatestV2DreamCycle(ctx context.Context, teamID, ownerProfileID string) (*V2DreamCycleRun, error)
}

type V2DreamCycleClaimInput struct {
	TeamID         string
	OwnerProfileID string
	RunDate        string
	WindowKey      string
	LeaseUntil     time.Time
	SourceSnapshot []map[string]any
}

type V2DreamCycleCompleteInput struct {
	TeamID             string
	OwnerProfileID     string
	RunID              string
	Status             string
	InputCount         int
	CreatedHypotheses  int
	RejectedHypotheses int
	Error              string
}

type V2DreamCycleRun struct {
	TeamID             string
	RunID              string
	OwnerProfileID     string
	RunDate            string
	WindowKey          string
	Status             string
	InputCount         int
	CreatedHypotheses  int
	RejectedHypotheses int
	Error              string
	StartedAt          time.Time
	CompletedAt        *time.Time
	Claimed            bool
}

type V2DreamInputListInput struct {
	TeamID string
	Limit  int
}

type V2DreamInput struct {
	RelationshipID     string
	OwnerProfileID     string
	Version            int
	Tier               string
	Status             string
	SubjectEntityID    string
	SubjectName        string
	PredicateKey       string
	PredicateVersion   int
	ObjectEntityID     string
	ObjectValueID      string
	ObjectName         string
	RelationshipKind   string
	CurrentCardinality string
}

type V2UpsertHypothesisInput struct {
	TeamID                string
	OwnerProfileID        string
	RunID                 string
	Statement             string
	Rationale             string
	Likelihood            *float64
	Confidence            *float64
	SubjectEntityID       string
	PredicateKey          string
	PredicateVersion      int
	ObjectEntityID        string
	ObjectValueID         string
	SourceRefs            []map[string]any
	SourceVersions        map[string]int
	SourceOwnerProfileIDs []string
	ContentHash           string
	GeneratorKind         string
	GeneratorVersion      string
	Payload               map[string]any
}

type V2HypothesisRecord struct {
	TeamID                string
	HypothesisID          string
	OwnerProfileID        string
	Status                string
	Statement             string
	Rationale             string
	Likelihood            *float64
	Confidence            *float64
	SubjectEntityID       string
	PredicateKey          string
	PredicateVersion      int
	ObjectEntityID        string
	ObjectValueID         string
	SourceRefs            []map[string]any
	SourceVersions        map[string]int
	SourceOwnerProfileIDs []string
	ContentHash           string
	CycleRunID            string
	GeneratorKind         string
	GeneratorVersion      string
	InvalidatedReason     string
	SubmittedIngestID     string
	SubmittedAt           *time.Time
	Payload               map[string]any
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type V2ListHypothesesInput struct {
	TeamID string
	Status string
	Limit  int
	Cursor string
}

type V2GetHypothesisInput struct {
	TeamID       string
	HypothesisID string
}

type V2RecallHypothesesInput struct {
	TeamID string
	Query  string
	Limit  int
}

type V2RefreshHypothesisStalenessInput struct {
	TeamID         string
	OwnerProfileID string
	Limit          int
}

type V2UpdateHypothesisStatusInput struct {
	TeamID            string
	OwnerProfileID    string
	HypothesisID      string
	Status            string
	InvalidatedReason string
}

type V2SubmitHypothesisInput struct {
	TeamID            string
	OwnerProfileID    string
	HypothesisID      string
	SubmittedIngestID string
	InvalidatedReason string
}

var _ V2DreamRepository = (*V2SemanticRepositoryImpl)(nil)

func (r *V2SemanticRepositoryImpl) ClaimV2DreamCycle(ctx context.Context, input V2DreamCycleClaimInput) (*V2DreamCycleRun, error) {
	input = normalizeV2DreamCycleClaimInput(input)
	if err := validateV2DreamCycleClaimInput(input); err != nil {
		return nil, err
	}
	snapshot, err := marshalV2JSONArray(input.SourceSnapshot)
	if err != nil {
		return nil, err
	}
	var run *V2DreamCycleRun
	err = r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			INSERT INTO dream_cycle_runs (
			    team_id, owner_profile_id, run_date, window_key, lease_until,
			    source_snapshot
			) VALUES (
			    ?::uuid, ?::uuid, ?, ?, ?, ?::jsonb
			)
			ON CONFLICT ON CONSTRAINT dream_cycle_runs_window_unique DO NOTHING
			RETURNING team_id::text, run_id::text, owner_profile_id::text,
			          run_date, window_key, status, input_count,
			          created_hypotheses, rejected_hypotheses, error,
			          started_at, completed_at
		`, input.TeamID, input.OwnerProfileID, input.RunDate, input.WindowKey,
			input.LeaseUntil, string(snapshot)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			loaded, err := scanV2DreamCycleRun(rows)
			if err != nil {
				return err
			}
			loaded.Claimed = true
			run = loaded
			return rows.Err()
		}
		if err := rows.Err(); err != nil {
			return err
		}
		existing, err := loadV2DreamCycleByWindow(ctx, tx, input.TeamID, input.OwnerProfileID, input.WindowKey)
		if err != nil {
			return err
		}
		existing.Claimed = false
		run = existing
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 dream: claim cycle: %w", err)
	}
	return run, nil
}

func (r *V2SemanticRepositoryImpl) CompleteV2DreamCycle(ctx context.Context, input V2DreamCycleCompleteInput) error {
	input = normalizeV2DreamCycleCompleteInput(input)
	if err := validateV2DreamCycleCompleteInput(input); err != nil {
		return err
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		res := tx.WithContext(ctx).Exec(`
			UPDATE dream_cycle_runs
			SET status = ?,
			    input_count = ?,
			    created_hypotheses = ?,
			    rejected_hypotheses = ?,
			    error = ?,
			    lease_until = NULL,
			    completed_at = now(),
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND run_id = ?::uuid
		`, input.Status, input.InputCount, input.CreatedHypotheses, input.RejectedHypotheses,
			input.Error, input.TeamID, input.OwnerProfileID, input.RunID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("v2 dream: complete cycle: %w", err)
	}
	return nil
}

func (r *V2SemanticRepositoryImpl) ListV2DreamInputs(ctx context.Context, input V2DreamInputListInput) ([]V2DreamInput, error) {
	input = normalizeV2DreamInputListInput(input)
	if err := validateV2DreamInputListInput(input); err != nil {
		return nil, err
	}
	var inputs []V2DreamInput
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT r.relationship_id::text,
			       r.owner_profile_id::text,
			       r.version,
			       r.tier,
			       r.status,
			       r.subject_entity_id::text,
			       COALESCE(subject_name.display_name, '') AS subject_name,
			       r.predicate_key,
			       r.predicate_version,
			       COALESCE(r.object_entity_id::text, '') AS object_entity_id,
			       COALESCE(r.object_value_id::text, '') AS object_value_id,
			       COALESCE(object_name.display_name, value.display, '') AS object_name,
			       r.relationship_kind,
			       r.current_cardinality
			FROM relationship_records r
			LEFT JOIN entity_names subject_name
			  ON subject_name.team_id = r.team_id
			 AND subject_name.entity_id = r.subject_entity_id
			 AND subject_name.name_kind = 'canonical'
			 AND subject_name.valid_to IS NULL
			LEFT JOIN entity_names object_name
			  ON object_name.team_id = r.team_id
			 AND object_name.entity_id = r.object_entity_id
			 AND object_name.name_kind = 'canonical'
			 AND object_name.valid_to IS NULL
			LEFT JOIN value_records value
			  ON value.team_id = r.team_id
			 AND value.value_id = r.object_value_id
			WHERE r.team_id = ?::uuid
			  AND (
			    (r.tier IN ('validated_claim', 'fact') AND r.status = 'active')
			    OR (
			      r.tier = 'candidate'
			      AND r.status = 'pending_evidence'
			      AND EXISTS (
			        SELECT 1
			        FROM relationship_observations o
			        JOIN verification_events v
			          ON v.team_id = o.team_id
			         AND v.observation_id = o.observation_id
			        WHERE o.team_id = r.team_id
			          AND o.relationship_id = r.relationship_id
			          AND v.evidence_verdict = 'insufficient'
			      )
			    )
			  )
			  AND NOT EXISTS (
			    SELECT 1
			    FROM relationship_cross_references cr
			    WHERE cr.team_id = r.team_id
			      AND cr.target_relationship_id = r.relationship_id
			      AND cr.kind = 'challenges'
			  )
			ORDER BY r.updated_at DESC, r.relationship_id
			LIMIT ?
		`, input.TeamID, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item V2DreamInput
			if err := rows.Scan(
				&item.RelationshipID,
				&item.OwnerProfileID,
				&item.Version,
				&item.Tier,
				&item.Status,
				&item.SubjectEntityID,
				&item.SubjectName,
				&item.PredicateKey,
				&item.PredicateVersion,
				&item.ObjectEntityID,
				&item.ObjectValueID,
				&item.ObjectName,
				&item.RelationshipKind,
				&item.CurrentCardinality,
			); err != nil {
				return err
			}
			inputs = append(inputs, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 dream: list inputs: %w", err)
	}
	return inputs, nil
}

func (r *V2SemanticRepositoryImpl) UpsertV2Hypothesis(
	ctx context.Context,
	input V2UpsertHypothesisInput,
) (*V2HypothesisRecord, bool, error) {
	input = normalizeV2UpsertHypothesisInput(input)
	if err := validateV2UpsertHypothesisInput(input); err != nil {
		return nil, false, err
	}
	var record *V2HypothesisRecord
	inserted := false
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := validateV2HypothesisEndpoints(ctx, tx, input); err != nil {
			return err
		}
		if err := validateV2HypothesisSources(ctx, tx, input); err != nil {
			return err
		}
		sourceRefs, err := marshalV2JSONArray(input.SourceRefs)
		if err != nil {
			return err
		}
		sourceVersions, err := marshalV2IntMapJSON(input.SourceVersions)
		if err != nil {
			return err
		}
		payload, err := marshalV2JSON(input.Payload)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			INSERT INTO hypotheses (
			    team_id, owner_profile_id, status, statement, rationale,
			    likelihood, confidence, subject_entity_id, predicate_key,
			    predicate_version, object_entity_id, object_value_id,
			    source_refs, source_versions, source_owner_profile_ids,
			    content_hash, cycle_run_id, generator_kind, generator_version,
			    payload
			) VALUES (
			    ?::uuid, ?::uuid, 'proposed', ?, ?, ?, ?, ?::uuid, ?, ?,
			    NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?::jsonb, ?::jsonb,
			    ?::uuid[], ?, ?::uuid, ?, ?, ?::jsonb
			)
			ON CONFLICT (team_id, owner_profile_id, content_hash)
			WHERE content_hash IS NOT NULL
			DO NOTHING
			RETURNING team_id::text, hypothesis_id::text, owner_profile_id::text,
			          status, statement, rationale, likelihood, confidence,
			          subject_entity_id::text, predicate_key, predicate_version,
			          COALESCE(object_entity_id::text, ''), COALESCE(object_value_id::text, ''),
			          source_refs, source_versions,
			          ARRAY(SELECT source_owner_id::text FROM unnest(source_owner_profile_ids) AS source_owner(source_owner_id)),
			          COALESCE(content_hash, ''), COALESCE(cycle_run_id::text, ''),
			          generator_kind, generator_version, invalidated_reason,
			          COALESCE(submitted_ingest_id::text, ''), submitted_at,
			          payload, created_at, updated_at
		`, input.TeamID, input.OwnerProfileID, input.Statement, input.Rationale,
			input.Likelihood, input.Confidence, input.SubjectEntityID, input.PredicateKey,
			input.PredicateVersion, input.ObjectEntityID, input.ObjectValueID,
			string(sourceRefs), string(sourceVersions), pq.Array(input.SourceOwnerProfileIDs),
			input.ContentHash, input.RunID, input.GeneratorKind, input.GeneratorVersion,
			string(payload)).Rows()
		if err != nil {
			return err
		}
		if rows.Next() {
			loaded, err := scanV2HypothesisRecord(rows)
			_ = rows.Close()
			if err != nil {
				return err
			}
			record = loaded
			inserted = true
			return nil
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		record, err = reinforceV2HypothesisByHash(ctx, tx, input)
		return err
	})
	if err != nil {
		return nil, false, fmt.Errorf("v2 dream: upsert hypothesis: %w", err)
	}
	return record, inserted, nil
}

func (r *V2SemanticRepositoryImpl) ListV2Hypotheses(
	ctx context.Context,
	input V2ListHypothesesInput,
) ([]V2HypothesisRecord, string, error) {
	input = normalizeV2ListHypothesesInput(input)
	if err := validateV2ListHypothesesInput(input); err != nil {
		return nil, "", err
	}
	offset := v2EvaluationCursorOffset(input.Cursor)
	limit := input.Limit + 1
	records := []V2HypothesisRecord{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(v2HypothesisSelectSQL(`
			WHERE team_id = ?::uuid
			  AND (? = '' OR status = ?)
			ORDER BY updated_at DESC, hypothesis_id
			LIMIT ? OFFSET ?
		`), input.TeamID, input.Status, input.Status, limit, offset).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		records, err = scanV2HypothesisRecords(rows)
		return err
	})
	if err != nil {
		return nil, "", fmt.Errorf("v2 dream: list hypotheses: %w", err)
	}
	next := ""
	if len(records) > input.Limit {
		records = records[:input.Limit]
		next = fmt.Sprintf("%d", offset+input.Limit)
	}
	return records, next, nil
}

func (r *V2SemanticRepositoryImpl) GetV2Hypothesis(ctx context.Context, input V2GetHypothesisInput) (*V2HypothesisRecord, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.HypothesisID = strings.TrimSpace(input.HypothesisID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.HypothesisID); err != nil {
		return nil, fmt.Errorf("hypothesis_id is required: %w", err)
	}
	var record *V2HypothesisRecord
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(v2HypothesisSelectSQL(`
			WHERE team_id = ?::uuid
			  AND hypothesis_id = ?::uuid
			LIMIT 1
		`), input.TeamID, input.HypothesisID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return ErrV2DreamHypothesisNotFound
		}
		loaded, err := scanV2HypothesisRecord(rows)
		if err != nil {
			return err
		}
		record = loaded
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 dream: get hypothesis: %w", err)
	}
	return record, nil
}

func (r *V2SemanticRepositoryImpl) RecallV2Hypotheses(ctx context.Context, input V2RecallHypothesesInput) ([]V2HypothesisRecord, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	if input.Limit <= 0 {
		input.Limit = 5
	}
	if input.Limit > 20 {
		input.Limit = 20
	}
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	pattern := "%" + input.Query + "%"
	records := []V2HypothesisRecord{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(v2HypothesisSelectSQL(`
			WHERE team_id = ?::uuid
			  AND status IN ('proposed', 'reinforced')
			  AND (? = '' OR statement ILIKE ? OR rationale ILIKE ?)
			ORDER BY CASE WHEN ? <> '' AND statement ILIKE ? THEN 0 ELSE 1 END,
			         updated_at DESC,
			         hypothesis_id
			LIMIT ?
		`), input.TeamID, input.Query, pattern, pattern, input.Query, pattern, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		records, err = scanV2HypothesisRecords(rows)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("v2 dream: recall hypotheses: %w", err)
	}
	return records, nil
}

func (r *V2SemanticRepositoryImpl) RefreshV2HypothesisStaleness(
	ctx context.Context,
	input V2RefreshHypothesisStalenessInput,
) (int, error) {
	input = normalizeV2RefreshHypothesisStalenessInput(input)
	if err := validateV2RefreshHypothesisStalenessInput(input); err != nil {
		return 0, err
	}
	updated := 0
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH stale AS (
			    SELECT h.team_id, h.hypothesis_id
			    FROM hypotheses h
			    WHERE h.team_id = ?::uuid
			      AND h.owner_profile_id = ?::uuid
			      AND h.status IN ('proposed', 'reinforced')
			      AND EXISTS (
			        SELECT 1
			        FROM jsonb_each_text(h.source_versions) AS source(source_id, source_version)
			        LEFT JOIN relationship_records r
			          ON r.team_id = h.team_id
			         AND r.relationship_id = CASE
			             WHEN source.source_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
			             THEN source.source_id::uuid
			             ELSE NULL
			         END
			        WHERE r.relationship_id IS NULL
			           OR r.version::text <> source.source_version
			           OR NOT (
			             (r.tier IN ('validated_claim', 'fact') AND r.status = 'active')
			             OR (
			               r.tier = 'candidate'
			               AND r.status = 'pending_evidence'
			               AND EXISTS (
			                 SELECT 1
			                 FROM relationship_observations o
			                 JOIN verification_events v
			                   ON v.team_id = o.team_id
			                  AND v.observation_id = o.observation_id
			                 WHERE o.team_id = r.team_id
			                   AND o.relationship_id = r.relationship_id
			                   AND v.evidence_verdict = 'insufficient'
			               )
			             )
			           )
			           OR EXISTS (
			             SELECT 1
			             FROM relationship_cross_references cr
			             WHERE cr.team_id = r.team_id
			               AND cr.target_relationship_id = r.relationship_id
			               AND cr.kind = 'challenges'
			           )
			      )
			    ORDER BY h.updated_at, h.hypothesis_id
			    LIMIT ?
			)
			UPDATE hypotheses h
			SET status = 'stale',
			    invalidated_reason = 'source relationship changed or became ineligible',
			    updated_at = now()
			FROM stale
			WHERE h.team_id = stale.team_id
			  AND h.hypothesis_id = stale.hypothesis_id
			RETURNING h.hypothesis_id
		`, input.TeamID, input.OwnerProfileID, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			updated++
		}
		return rows.Err()
	})
	if err != nil {
		return 0, fmt.Errorf("v2 dream: refresh hypothesis staleness: %w", err)
	}
	return updated, nil
}

func (r *V2SemanticRepositoryImpl) UpdateV2HypothesisStatus(
	ctx context.Context,
	input V2UpdateHypothesisStatusInput,
) (*V2HypothesisRecord, error) {
	input = normalizeV2UpdateHypothesisStatusInput(input)
	if err := validateV2UpdateHypothesisStatusInput(input); err != nil {
		return nil, err
	}
	var record *V2HypothesisRecord
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(v2HypothesisUpdateReturningSQL(`
			UPDATE hypotheses
			SET status = ?,
			    invalidated_reason = CASE WHEN ? <> '' THEN ? ELSE invalidated_reason END,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND hypothesis_id = ?::uuid
		`), input.Status, input.InvalidatedReason, input.InvalidatedReason,
			input.TeamID, input.OwnerProfileID, input.HypothesisID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return ErrV2DreamHypothesisNotFound
		}
		loaded, err := scanV2HypothesisRecord(rows)
		if err != nil {
			return err
		}
		record = loaded
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 dream: update hypothesis: %w", err)
	}
	return record, nil
}

func (r *V2SemanticRepositoryImpl) SubmitV2Hypothesis(
	ctx context.Context,
	input V2SubmitHypothesisInput,
) (*V2HypothesisRecord, error) {
	input = normalizeV2SubmitHypothesisInput(input)
	if err := validateV2SubmitHypothesisInput(input); err != nil {
		return nil, err
	}
	var record *V2HypothesisRecord
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(v2HypothesisUpdateReturningSQL(`
			UPDATE hypotheses
			SET status = 'submitted',
			    submitted_ingest_id = ?::uuid,
			    submitted_at = now(),
			    invalidated_reason = CASE WHEN ? <> '' THEN ? ELSE invalidated_reason END,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND hypothesis_id = ?::uuid
		`), input.SubmittedIngestID, input.InvalidatedReason, input.InvalidatedReason,
			input.TeamID, input.OwnerProfileID, input.HypothesisID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return ErrV2DreamHypothesisNotFound
		}
		loaded, err := scanV2HypothesisRecord(rows)
		if err != nil {
			return err
		}
		record = loaded
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 dream: submit hypothesis: %w", err)
	}
	return record, nil
}

func (r *V2SemanticRepositoryImpl) LatestV2DreamCycle(ctx context.Context, teamID, ownerProfileID string) (*V2DreamCycleRun, error) {
	teamID = strings.TrimSpace(teamID)
	ownerProfileID = strings.TrimSpace(ownerProfileID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(ownerProfileID); err != nil {
		return nil, fmt.Errorf("owner_profile_id is required: %w", err)
	}
	var run *V2DreamCycleRun
	err := r.withTeamProfileTx(ctx, teamID, ownerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, run_id::text, owner_profile_id::text,
			       run_date, window_key, status, input_count,
			       created_hypotheses, rejected_hypotheses, error,
			       started_at, completed_at
			FROM dream_cycle_runs
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			ORDER BY started_at DESC
			LIMIT 1
		`, teamID, ownerProfileID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return nil
		}
		loaded, err := scanV2DreamCycleRun(rows)
		if err != nil {
			return err
		}
		run = loaded
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 dream: latest cycle: %w", err)
	}
	return run, nil
}

func validateV2HypothesisEndpoints(ctx context.Context, tx *gorm.DB, input V2UpsertHypothesisInput) error {
	predicate, err := loadV2PredicateDefinition(ctx, tx, input.PredicateKey, input.PredicateVersion)
	if err != nil {
		return err
	}
	relationshipInput := V2ApplyRelationshipDecisionInput{
		TeamID:           input.TeamID,
		OwnerProfileID:   input.OwnerProfileID,
		IngestID:         uuid.NewString(),
		SubjectEntityID:  input.SubjectEntityID,
		PredicateKey:     input.PredicateKey,
		PredicateVersion: input.PredicateVersion,
		ObjectEntityID:   input.ObjectEntityID,
		ObjectValueID:    input.ObjectValueID,
		EvidenceVerdict:  "insufficient",
	}
	if err := validateV2RelationshipEndpointKinds(ctx, tx, relationshipInput, predicate); err != nil {
		return err
	}
	var existing int64
	if err := tx.WithContext(ctx).Raw(`
		SELECT COUNT(*)
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND subject_entity_id = ?::uuid
		  AND predicate_key = ?
		  AND predicate_version = ?
		  AND object_entity_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND object_value_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND tier IN ('validated_claim', 'fact')
		  AND status = 'active'
	`, input.TeamID, input.SubjectEntityID, input.PredicateKey, input.PredicateVersion,
		input.ObjectEntityID, input.ObjectValueID).Scan(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return ErrV2DreamExactRelationshipExists
	}
	return nil
}

func validateV2HypothesisSources(ctx context.Context, tx *gorm.DB, input V2UpsertHypothesisInput) error {
	for relationshipID, wantVersion := range input.SourceVersions {
		if _, err := uuid.Parse(relationshipID); err != nil {
			return fmt.Errorf("source relationship %q is invalid: %w", relationshipID, err)
		}
		var gotVersion int
		var eligible bool
		err := tx.WithContext(ctx).Raw(`
			SELECT r.version,
			       (
			         (
			           (r.tier IN ('validated_claim', 'fact') AND r.status = 'active')
			           OR (
			             r.tier = 'candidate'
			             AND r.status = 'pending_evidence'
			             AND EXISTS (
			               SELECT 1
			               FROM relationship_observations o
			               JOIN verification_events v
			                 ON v.team_id = o.team_id
			                AND v.observation_id = o.observation_id
			               WHERE o.team_id = r.team_id
			                 AND o.relationship_id = r.relationship_id
			                 AND v.evidence_verdict = 'insufficient'
			             )
			           )
			         )
			         AND NOT EXISTS (
			           SELECT 1
			           FROM relationship_cross_references cr
			           WHERE cr.team_id = r.team_id
			             AND cr.target_relationship_id = r.relationship_id
			             AND cr.kind = 'challenges'
			         )
			       ) AS eligible
			FROM relationship_records r
			WHERE r.team_id = ?::uuid
			  AND r.relationship_id = ?::uuid
		`, input.TeamID, relationshipID).Row().Scan(&gotVersion, &eligible)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrV2DreamSourceStale, relationshipID)
		}
		if err != nil {
			return err
		}
		if gotVersion != wantVersion || !eligible {
			return fmt.Errorf("%w: %s", ErrV2DreamSourceStale, relationshipID)
		}
	}
	return nil
}

func marshalV2IntMapJSON(value map[string]int) ([]byte, error) {
	if value == nil {
		value = map[string]int{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return data, nil
}

func reinforceV2HypothesisByHash(
	ctx context.Context,
	tx *gorm.DB,
	input V2UpsertHypothesisInput,
) (*V2HypothesisRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(v2HypothesisUpdateReturningSQL(`
		UPDATE hypotheses
		SET status = CASE
		        WHEN status IN ('proposed', 'reinforced') THEN 'reinforced'
		        ELSE status
		    END,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND content_hash = ?
	`), input.TeamID, input.OwnerProfileID, input.ContentHash).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrV2DreamHypothesisNotFound
	}
	return scanV2HypothesisRecord(rows)
}

func loadV2DreamCycleByWindow(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, windowKey string) (*V2DreamCycleRun, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, run_id::text, owner_profile_id::text,
		       run_date, window_key, status, input_count,
		       created_hypotheses, rejected_hypotheses, error,
		       started_at, completed_at
		FROM dream_cycle_runs
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND window_key = ?
		LIMIT 1
	`, teamID, ownerProfileID, windowKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrV2DreamCycleAlreadyClaimed
	}
	return scanV2DreamCycleRun(rows)
}
