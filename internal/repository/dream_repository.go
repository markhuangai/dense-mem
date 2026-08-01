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
	ErrDreamCycleAlreadyClaimed     = errors.New("dream cycle already claimed")
	ErrDreamHypothesisNotFound      = errors.New("dream hypothesis not found")
	ErrDreamSourceStale             = errors.New("dream source is stale")
	ErrDreamExactRelationshipExists = errors.New("dream exact relationship already exists")
)

const hypothesisSourceIneligiblePredicateSQL = `EXISTS (
	SELECT 1
	FROM jsonb_each_text(hypotheses.source_versions) AS source(source_id, source_version)
	LEFT JOIN relationship_records r
	  ON r.team_id = hypotheses.team_id
	 AND r.relationship_id = CASE
	     WHEN source.source_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
	     THEN source.source_id::uuid
	     ELSE NULL
	 END
	WHERE r.relationship_id IS NULL
	   OR r.version::text <> source.source_version
	   OR NOT (
	     (r.status = 'active' AND r.support_count > 0)
	     OR (
	       r.status = 'pending_evidence'
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
)`

type DreamRepository interface {
	ClaimDreamCycle(ctx context.Context, input DreamCycleClaimInput) (*DreamCycleRun, error)
	CompleteDreamCycle(ctx context.Context, input DreamCycleCompleteInput) error
	ListDreamInputs(ctx context.Context, input DreamInputListInput) ([]DreamInput, error)
	UpsertHypothesis(ctx context.Context, input UpsertHypothesisInput) (*HypothesisRecord, bool, error)
	ListHypotheses(ctx context.Context, input ListHypothesesInput) ([]HypothesisRecord, string, error)
	GetHypothesis(ctx context.Context, input GetHypothesisInput) (*HypothesisRecord, error)
	RecallHypotheses(ctx context.Context, input RecallHypothesesInput) ([]HypothesisRecord, error)
	UpdateHypothesisStatus(ctx context.Context, input UpdateHypothesisStatusInput) (*HypothesisRecord, error)
	SubmitHypothesis(ctx context.Context, input SubmitHypothesisInput) (*HypothesisRecord, error)
	CountHypotheses(ctx context.Context, teamID, status string) (int, error)
	ListDreamCyclesForTeam(ctx context.Context, teamID string, limit int) ([]DreamCycleRun, error)
}

// ScheduledDreamRepository is deliberately separate from DreamRepository's
// authenticated actor methods. Only the scheduler receives this system-mode
// port, so a request cannot select system mutation mode.
type ScheduledDreamRepository interface {
	ClaimScheduledDreamCycle(ctx context.Context, input DreamCycleClaimInput) (*DreamCycleRun, error)
	CompleteScheduledDreamCycle(ctx context.Context, input DreamCycleCompleteInput) error
	UpsertScheduledHypothesis(ctx context.Context, input UpsertHypothesisInput) (*HypothesisRecord, bool, error)
	RecordMissedScheduledDreamCycle(ctx context.Context, input DreamCycleClaimInput) (*DreamCycleRun, error)
}

type DreamCycleClaimInput struct {
	TeamID               string
	InitiatedByProfileID string
	RunDate              string
	WindowKey            string
	LeaseUntil           time.Time
	SourceSnapshot       []map[string]any
}

type DreamCycleCompleteInput struct {
	TeamID               string
	InitiatedByProfileID string
	RunID                string
	Status               string
	InputCount           int
	CreatedHypotheses    int
	RejectedHypotheses   int
	SourceSnapshot       []map[string]any
	Error                string
}

type DreamCycleRun struct {
	TeamID               string
	RunID                string
	InitiatedByProfileID string
	RunDate              string
	WindowKey            string
	Status               string
	InputCount           int
	CreatedHypotheses    int
	RejectedHypotheses   int
	Error                string
	StartedAt            time.Time
	CompletedAt          *time.Time
	Claimed              bool
}

type DreamInputListInput struct {
	TeamID string
	Limit  int
}

type DreamInput struct {
	RelationshipID     string
	OwnerProfileID     string
	Version            int
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

type UpsertHypothesisInput struct {
	TeamID                string
	CreatedByProfileID    string
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

type HypothesisRecord struct {
	TeamID                string
	HypothesisID          string
	CreatedByProfileID    string
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
	SubmittedSubmissionID string
	SubmittedAt           *time.Time
	Payload               map[string]any
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type ListHypothesesInput struct {
	TeamID    string
	Status    string
	Limit     int
	Cursor    string
	Sort      string
	Direction string
}

type GetHypothesisInput struct {
	TeamID       string
	HypothesisID string
}

type RecallHypothesesInput struct {
	TeamID string
	Query  string
	Limit  int
}

type UpdateHypothesisStatusInput struct {
	TeamID            string
	ActorProfileID    string
	HypothesisID      string
	Status            string
	Decision          string
	InvalidatedReason string
}

type SubmitHypothesisInput struct {
	TeamID                string
	ActorProfileID        string
	HypothesisID          string
	Decision              string
	SubmittedSubmissionID string
	InvalidatedReason     string
}

var _ DreamRepository = (*SemanticRepositoryImpl)(nil)
var _ ScheduledDreamRepository = (*SemanticRepositoryImpl)(nil)

func insertHypothesisFeedbackEvent(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	hypothesisID string,
	actorProfileID string,
	decision string,
	feedback string,
	submittedSubmissionID string,
) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO hypothesis_feedback_events (
		    team_id, hypothesis_id, actor_profile_id, decision, feedback,
		    submitted_submission_id
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, COALESCE(NULLIF(?, ''), ''),
		    NULLIF(?, '')::uuid
		)
	`, teamID, hypothesisID, actorProfileID, decision, feedback, submittedSubmissionID).Error
}

func (r *SemanticRepositoryImpl) ListDreamInputs(ctx context.Context, input DreamInputListInput) ([]DreamInput, error) {
	input = normalizeDreamInputListInput(input)
	if err := validateDreamInputListInput(input); err != nil {
		return nil, err
	}
	var inputs []DreamInput
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
				SELECT r.relationship_id::text,
				       r.owner_profile_id::text,
				       r.version,
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
				    (r.status = 'active' AND r.support_count > 0)
				    OR (
				      r.status = 'pending_evidence'
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
			var item DreamInput
			if err := rows.Scan(
				&item.RelationshipID,
				&item.OwnerProfileID,
				&item.Version,
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
		return nil, fmt.Errorf("dream: list inputs: %w", err)
	}
	return inputs, nil
}

func (r *SemanticRepositoryImpl) UpsertHypothesis(
	ctx context.Context,
	input UpsertHypothesisInput,
) (*HypothesisRecord, bool, error) {
	return r.upsertHypothesis(ctx, input, false)
}

func (r *SemanticRepositoryImpl) UpsertScheduledHypothesis(
	ctx context.Context,
	input UpsertHypothesisInput,
) (*HypothesisRecord, bool, error) {
	return r.upsertHypothesis(ctx, input, true)
}

func (r *SemanticRepositoryImpl) upsertHypothesis(
	ctx context.Context,
	input UpsertHypothesisInput,
	system bool,
) (*HypothesisRecord, bool, error) {
	input = normalizeUpsertHypothesisInput(input)
	if err := validateUpsertHypothesisInput(input, system); err != nil {
		return nil, false, err
	}
	var record *HypothesisRecord
	inserted := false
	err := r.withDreamWriteTx(ctx, input.TeamID, input.CreatedByProfileID, system, func(tx *gorm.DB) error {
		if err := validateHypothesisEndpoints(ctx, tx, input); err != nil {
			return err
		}
		if err := validateHypothesisSources(ctx, tx, input); err != nil {
			return err
		}
		sourceRefs, err := marshalJSONArray(input.SourceRefs)
		if err != nil {
			return err
		}
		sourceVersions, err := marshalIntMapJSON(input.SourceVersions)
		if err != nil {
			return err
		}
		payload, err := marshalJSON(input.Payload)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			INSERT INTO hypotheses (
			    team_id, created_by_profile_id, status, statement, rationale,
			    likelihood, confidence, subject_entity_id, predicate_key,
			    predicate_version, object_entity_id, object_value_id,
			    source_refs, source_versions, source_owner_profile_ids,
			    content_hash, cycle_run_id, generator_kind, generator_version,
			    payload
			) VALUES (
			    ?::uuid, NULLIF(?, '')::uuid, 'proposed', ?, ?, ?, ?, ?::uuid, ?, ?,
			    NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?::jsonb, ?::jsonb,
			    ?::uuid[], ?, ?::uuid, ?, ?, ?::jsonb
			)
			ON CONFLICT (team_id, content_hash)
			WHERE content_hash IS NOT NULL AND canonical_hypothesis_id IS NULL
			DO NOTHING
			RETURNING team_id::text, hypothesis_id::text, COALESCE(created_by_profile_id::text, ''),
			          status, statement, rationale, likelihood, confidence,
			          subject_entity_id::text, predicate_key, predicate_version,
			          COALESCE(object_entity_id::text, ''), COALESCE(object_value_id::text, ''),
			          source_refs, source_versions,
			          ARRAY(SELECT source_owner_id::text FROM unnest(source_owner_profile_ids) AS source_owner(source_owner_id)),
			          COALESCE(content_hash, ''), COALESCE(cycle_run_id::text, ''),
			          generator_kind, generator_version, invalidated_reason,
			          COALESCE(submitted_ingest_id::text, ''), COALESCE(submitted_submission_id::text, ''), submitted_at,
			          payload, created_at, updated_at
		`, input.TeamID, input.CreatedByProfileID, input.Statement, input.Rationale,
			input.Likelihood, input.Confidence, input.SubjectEntityID, input.PredicateKey,
			input.PredicateVersion, input.ObjectEntityID, input.ObjectValueID,
			string(sourceRefs), string(sourceVersions), pq.Array(input.SourceOwnerProfileIDs),
			input.ContentHash, input.RunID, input.GeneratorKind, input.GeneratorVersion,
			string(payload)).Rows()
		if err != nil {
			return err
		}
		if rows.Next() {
			loaded, err := scanHypothesisRecord(rows)
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
		record, err = reinforceHypothesisByHash(ctx, tx, input)
		return err
	})
	if err != nil {
		return nil, false, fmt.Errorf("dream: upsert hypothesis: %w", err)
	}
	return record, inserted, nil
}

func (r *SemanticRepositoryImpl) ListHypotheses(
	ctx context.Context,
	input ListHypothesesInput,
) ([]HypothesisRecord, string, error) {
	input = normalizeListHypothesesInput(input)
	if err := validateListHypothesesInput(input); err != nil {
		return nil, "", err
	}
	offset := evaluationCursorOffset(input.Cursor)
	limit := input.Limit + 1
	records := []HypothesisRecord{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		query := hypothesisSelectSQL(`
			WHERE team_id = ?::uuid
			  AND canonical_hypothesis_id IS NULL
			  AND (? = '' OR status = ?)
			ORDER BY ` + hypothesisListOrder(input.Sort, input.Direction) + `, hypothesis_id
			LIMIT ? OFFSET ?
		`)
		rows, err := tx.WithContext(ctx).Raw(
			query,
			input.TeamID,
			input.Status,
			input.Status,
			limit,
			offset,
		).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		records, err = scanHypothesisRecords(rows)
		return err
	})
	if err != nil {
		return nil, "", fmt.Errorf("dream: list hypotheses: %w", err)
	}
	next := ""
	if len(records) > input.Limit {
		records = records[:input.Limit]
		next = fmt.Sprintf("%d", offset+input.Limit)
	}
	return records, next, nil
}

func (r *SemanticRepositoryImpl) GetHypothesis(ctx context.Context, input GetHypothesisInput) (*HypothesisRecord, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.HypothesisID = strings.TrimSpace(input.HypothesisID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.HypothesisID); err != nil {
		return nil, fmt.Errorf("hypothesis_id is required: %w", err)
	}
	var record *HypothesisRecord
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(hypothesisSelectSQL(`
			WHERE team_id = ?::uuid
			  AND canonical_hypothesis_id IS NULL
			  AND hypothesis_id = COALESCE((
			      SELECT canonical_hypothesis_id
			      FROM hypotheses
			      WHERE team_id = ?::uuid
			        AND hypothesis_id = ?::uuid
			  ), ?::uuid)
			LIMIT 1
		`), input.TeamID, input.TeamID, input.HypothesisID, input.HypothesisID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return ErrDreamHypothesisNotFound
		}
		loaded, err := scanHypothesisRecord(rows)
		if err != nil {
			return err
		}
		record = loaded
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("dream: get hypothesis: %w", err)
	}
	return record, nil
}

func (r *SemanticRepositoryImpl) RecallHypotheses(ctx context.Context, input RecallHypothesesInput) ([]HypothesisRecord, error) {
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
	records := []HypothesisRecord{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		query := hypothesisSelectSQL(`
			WHERE team_id = ?::uuid
			  AND canonical_hypothesis_id IS NULL
			  AND status IN ('proposed', 'reinforced')
			  AND NOT (` + hypothesisSourceIneligiblePredicateSQL + `)
			  AND (? = '' OR statement ILIKE ? OR rationale ILIKE ?)
			ORDER BY CASE WHEN ? <> '' AND statement ILIKE ? THEN 0 ELSE 1 END,
			         updated_at DESC,
			         hypothesis_id
			LIMIT ?
		`)
		rows, err := tx.WithContext(ctx).Raw(query, input.TeamID, input.Query, pattern, pattern, input.Query, pattern, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		records, err = scanHypothesisRecords(rows)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("dream: recall hypotheses: %w", err)
	}
	return records, nil
}

func (r *SemanticRepositoryImpl) UpdateHypothesisStatus(
	ctx context.Context,
	input UpdateHypothesisStatusInput,
) (*HypothesisRecord, error) {
	input = normalizeUpdateHypothesisStatusInput(input)
	if err := validateUpdateHypothesisStatusInput(input); err != nil {
		return nil, err
	}
	var record *HypothesisRecord
	err := r.withTeamProfileTx(ctx, input.TeamID, input.ActorProfileID, func(tx *gorm.DB) error {
		if err := ensureSemanticRefs(ctx, tx, input.TeamID, input.ActorProfileID); err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(hypothesisUpdateReturningSQL(`
			UPDATE hypotheses
			SET status = ?,
			    invalidated_reason = CASE WHEN ? <> '' THEN ? ELSE invalidated_reason END,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND canonical_hypothesis_id IS NULL
			  AND hypothesis_id = COALESCE((
			      SELECT canonical_hypothesis_id
			      FROM hypotheses
			      WHERE team_id = ?::uuid
			        AND hypothesis_id = ?::uuid
			  ), ?::uuid)
		`), input.Status, input.InvalidatedReason, input.InvalidatedReason,
			input.TeamID, input.TeamID, input.HypothesisID, input.HypothesisID).Rows()
		if err != nil {
			return err
		}
		if !rows.Next() {
			_ = rows.Close()
			return ErrDreamHypothesisNotFound
		}
		loaded, err := scanHypothesisRecord(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := insertHypothesisFeedbackEvent(ctx, tx, input.TeamID, loaded.HypothesisID,
			input.ActorProfileID, input.Decision, input.InvalidatedReason, ""); err != nil {
			return err
		}
		record = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dream: update hypothesis: %w", err)
	}
	return record, nil
}

func (r *SemanticRepositoryImpl) SubmitHypothesis(
	ctx context.Context,
	input SubmitHypothesisInput,
) (*HypothesisRecord, error) {
	input = normalizeSubmitHypothesisInput(input)
	if err := validateSubmitHypothesisInput(input); err != nil {
		return nil, err
	}
	var record *HypothesisRecord
	err := r.withTeamProfileTx(ctx, input.TeamID, input.ActorProfileID, func(tx *gorm.DB) error {
		if err := ensureSemanticRefs(ctx, tx, input.TeamID, input.ActorProfileID); err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(hypothesisUpdateReturningSQL(`
			UPDATE hypotheses
			SET status = 'submitted',
			    submitted_ingest_id = NULL,
			    submitted_submission_id = ?::uuid,
			    submitted_at = now(),
			    invalidated_reason = CASE WHEN ? <> '' THEN ? ELSE invalidated_reason END,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND canonical_hypothesis_id IS NULL
			  AND hypothesis_id = COALESCE((
			      SELECT canonical_hypothesis_id
			      FROM hypotheses
			      WHERE team_id = ?::uuid
			        AND hypothesis_id = ?::uuid
			  ), ?::uuid)
		`), input.SubmittedSubmissionID, input.InvalidatedReason, input.InvalidatedReason,
			input.TeamID, input.TeamID, input.HypothesisID, input.HypothesisID).Rows()
		if err != nil {
			return err
		}
		if !rows.Next() {
			_ = rows.Close()
			return ErrDreamHypothesisNotFound
		}
		loaded, err := scanHypothesisRecord(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := insertHypothesisFeedbackEvent(ctx, tx, input.TeamID, loaded.HypothesisID,
			input.ActorProfileID, input.Decision, input.InvalidatedReason, input.SubmittedSubmissionID); err != nil {
			return err
		}
		record = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dream: submit hypothesis: %w", err)
	}
	return record, nil
}

func validateHypothesisEndpoints(ctx context.Context, tx *gorm.DB, input UpsertHypothesisInput) error {
	predicate, err := loadPredicateDefinition(ctx, tx, input.TeamID, input.PredicateKey, input.PredicateVersion)
	if err != nil {
		return err
	}
	relationshipInput := ApplyRelationshipDecisionInput{
		TeamID:           input.TeamID,
		IngestID:         uuid.NewString(),
		SubjectEntityID:  input.SubjectEntityID,
		PredicateKey:     input.PredicateKey,
		PredicateVersion: input.PredicateVersion,
		ObjectEntityID:   input.ObjectEntityID,
		ObjectValueID:    input.ObjectValueID,
		EvidenceVerdict:  "insufficient",
	}
	if err := validateRelationshipEndpointKinds(ctx, tx, relationshipInput, predicate); err != nil {
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
		  AND status = 'active'
		  AND support_count > 0
	`, input.TeamID, input.SubjectEntityID, input.PredicateKey, input.PredicateVersion,
		input.ObjectEntityID, input.ObjectValueID).Scan(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return ErrDreamExactRelationshipExists
	}
	return nil
}

func validateHypothesisSources(ctx context.Context, tx *gorm.DB, input UpsertHypothesisInput) error {
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
			           (r.status = 'active' AND r.support_count > 0)
			           OR (
			             r.status = 'pending_evidence'
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
			return fmt.Errorf("%w: %s", ErrDreamSourceStale, relationshipID)
		}
		if err != nil {
			return err
		}
		if gotVersion != wantVersion || !eligible {
			return fmt.Errorf("%w: %s", ErrDreamSourceStale, relationshipID)
		}
	}
	return nil
}

func marshalIntMapJSON(value map[string]int) ([]byte, error) {
	if value == nil {
		value = map[string]int{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return data, nil
}

func reinforceHypothesisByHash(
	ctx context.Context,
	tx *gorm.DB,
	input UpsertHypothesisInput,
) (*HypothesisRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(hypothesisUpdateReturningSQL(`
		UPDATE hypotheses
		SET status = CASE
		        WHEN status IN ('proposed', 'reinforced') THEN 'reinforced'
		        ELSE status
		    END,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND canonical_hypothesis_id IS NULL
		  AND content_hash = ?
	`), input.TeamID, input.ContentHash).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrDreamHypothesisNotFound
	}
	return scanHypothesisRecord(rows)
}

func loadDreamCycleByWindow(ctx context.Context, tx *gorm.DB, teamID, windowKey string) (*DreamCycleRun, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, run_id::text, COALESCE(initiated_by_profile_id::text, ''),
		       run_date, window_key, status, input_count,
		       created_hypotheses, rejected_hypotheses, error,
		       started_at, completed_at
		FROM dream_cycle_runs
		WHERE team_id = ?::uuid
		  AND canonical_run_id IS NULL
		  AND window_key = ?
		LIMIT 1
	`, teamID, windowKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrDreamCycleAlreadyClaimed
	}
	return scanDreamCycleRun(rows)
}
