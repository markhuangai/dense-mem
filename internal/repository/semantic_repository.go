package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var ErrSemanticOwnerMismatch = errors.New("semantic owner mismatch")
var ErrSemanticIdempotencyConflict = errors.New("semantic idempotency conflict")
var ErrSemanticIdentityAlias = errors.New("semantic relationship is a legacy identity alias")

type SemanticRepositoryImpl struct {
	db  *gorm.DB
	rls rLSHelper
}

var _ SemanticRepository = (*SemanticRepositoryImpl)(nil)

func NewSemanticRepository(db *gorm.DB, rls *postgres.RLS) *SemanticRepositoryImpl {
	return &SemanticRepositoryImpl{db: db, rls: rls}
}

func (r *SemanticRepositoryImpl) CreateEntity(ctx context.Context, input CreateEntityInput) (*EntityRecord, error) {
	input = normalizeCreateEntityInput(input)
	if err := validateCreateEntityInput(input); err != nil {
		return nil, err
	}
	var record *EntityRecord
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureSemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		identityContext, err := marshalJSON(input.IdentityContext)
		if err != nil {
			return err
		}
		metadata, err := marshalJSON(input.Metadata)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			INSERT INTO entity_records (team_id, entity_kind, identity_context, metadata)
			VALUES (?::uuid, ?, ?::jsonb, ?::jsonb)
			RETURNING team_id::text, entity_id::text, entity_kind, version
		`, input.TeamID, input.EntityKind, string(identityContext), string(metadata)).Rows()
		if err != nil {
			return err
		}
		if !rows.Next() {
			_ = rows.Close()
			return rows.Err()
		}
		created := EntityRecord{CanonicalName: input.CanonicalName}
		if err := rows.Scan(&created.TeamID, &created.EntityID, &created.EntityKind, &created.Version); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if _, err := insertEntityName(ctx, tx, AddEntityNameInput{
			TeamID:         input.TeamID,
			OwnerProfileID: input.OwnerProfileID,
			EntityID:       created.EntityID,
			DisplayName:    input.CanonicalName,
			NameKind:       "canonical",
		}); err != nil {
			return err
		}
		record = &created
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: create entity: %w", err)
	}
	return record, nil
}

func (r *SemanticRepositoryImpl) AddEntityName(ctx context.Context, input AddEntityNameInput) (string, error) {
	input = normalizeAddEntityNameInput(input)
	if err := validateAddEntityNameInput(input); err != nil {
		return "", err
	}
	var nameID string
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureSemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		var err error
		nameID, err = insertEntityName(ctx, tx, input)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("semantic: add entity name: %w", err)
	}
	return nameID, nil
}

func (r *SemanticRepositoryImpl) UpsertValue(ctx context.Context, input UpsertValueInput) (*ValueRecord, error) {
	input = normalizeUpsertValueInput(input)
	if err := validateUpsertValueInput(input); err != nil {
		return nil, err
	}
	var record *ValueRecord
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureSemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		metadata, err := marshalJSON(input.Metadata)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			WITH inserted AS (
				INSERT INTO value_records (
				    team_id, value_type, canonical_value, unit, display,
				    normalization_version, metadata
				) VALUES (
				    ?::uuid, ?, ?, NULLIF(?, ''), ?, ?, ?::jsonb
				)
				ON CONFLICT ON CONSTRAINT value_records_canonical_unique
				DO NOTHING
				RETURNING team_id::text, value_id::text, value_type, canonical_value,
				          COALESCE(unit, ''), display, normalization_version, false AS existing
			)
			SELECT * FROM inserted
			UNION ALL
			SELECT team_id::text, value_id::text, value_type, canonical_value,
			       COALESCE(unit, ''), display, normalization_version, true AS existing
			FROM value_records
			WHERE team_id = ?::uuid
			  AND value_type = ?
			  AND canonical_value = ?
			  AND unit IS NOT DISTINCT FROM NULLIF(?, '')
			  AND normalization_version = ?
			LIMIT 1
		`, input.TeamID, input.ValueType, input.CanonicalValue, input.Unit, input.Display,
			input.NormalizationVersion, string(metadata),
			input.TeamID, input.ValueType, input.CanonicalValue, input.Unit,
			input.NormalizationVersion).Rows()
		if err != nil {
			return err
		}
		loaded, scanErr := scanValueRows(rows)
		closeErr := rows.Close()
		if scanErr != nil {
			return scanErr
		}
		if closeErr != nil {
			return closeErr
		}
		if loaded == nil {
			loaded, err = selectValueByKey(ctx, tx, input)
			if err != nil {
				return err
			}
		}
		record = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: upsert value: %w", err)
	}
	return record, nil
}

func scanValueRows(rows *sql.Rows) (*ValueRecord, error) {
	if !rows.Next() {
		return nil, rows.Err()
	}
	loaded := ValueRecord{}
	if err := rows.Scan(&loaded.TeamID, &loaded.ValueID, &loaded.ValueType, &loaded.CanonicalValue,
		&loaded.Unit, &loaded.Display, &loaded.NormalizationVersion, &loaded.Existing); err != nil {
		return nil, err
	}
	return &loaded, rows.Err()
}

func selectValueByKey(ctx context.Context, tx *gorm.DB, input UpsertValueInput) (*ValueRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, value_id::text, value_type, canonical_value,
		       COALESCE(unit, ''), display, normalization_version, true AS existing
		FROM value_records
		WHERE team_id = ?::uuid
		  AND value_type = ?
		  AND canonical_value = ?
		  AND unit IS NOT DISTINCT FROM NULLIF(?, '')
		  AND normalization_version = ?
	`, input.TeamID, input.ValueType, input.CanonicalValue, input.Unit,
		input.NormalizationVersion).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	record, err := scanValueRows(rows)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return record, nil
}

func (r *SemanticRepositoryImpl) ApplyRelationshipDecision(
	ctx context.Context,
	input ApplyRelationshipDecisionInput,
) (*RelationshipDecisionResult, error) {
	input = normalizeApplyRelationshipDecisionInput(input)
	if err := validateApplyRelationshipDecisionInput(input); err != nil {
		return nil, err
	}
	var result *RelationshipDecisionResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureSemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		if err := validateSupportOwnership(ctx, tx, input); err != nil {
			return err
		}
		predicate, err := loadPredicateDefinition(ctx, tx, input.TeamID, input.PredicateKey, input.PredicateVersion)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			review, err := insertPredicateReview(ctx, tx, input)
			if err != nil {
				return err
			}
			result = review
			return nil
		}
		if err != nil {
			return err
		}
		if err := validateRelationshipEndpointKinds(ctx, tx, input, predicate); err != nil {
			return err
		}
		status := statusForRelationshipDecision(input)
		groupKey := semanticGroupKey(input)
		recordState, err := upsertRelationshipRecord(ctx, tx, input, predicate, status, groupKey)
		if err != nil {
			return err
		}
		if recordState.ValidToConflict {
			review, err := insertRelationshipValidToReview(ctx, tx, input, recordState.Record)
			if err != nil {
				return err
			}
			result = review
			return nil
		}
		observationID, err := insertRelationshipObservation(ctx, tx, input, recordState.Record.RelationshipID)
		if err != nil {
			return err
		}
		verificationID, err := insertVerificationEvent(ctx, tx, input, observationID)
		if err != nil {
			return err
		}
		var supportID, supportDecisionID string
		var supportIDs []string
		if input.EvidenceVerdict == string(domain.VerificationEntailed) && len(relationshipEvidenceSupports(input.Support, input.Supports)) > 0 && !input.SuppressSupport {
			var supportDecisionIDs []string
			supportIDs, supportDecisionIDs, err = insertRelationshipSupports(ctx, tx, input, recordState.Record.RelationshipID, observationID, verificationID)
			if err != nil {
				return err
			}
			if len(supportIDs) > 0 {
				supportID = supportIDs[0]
			}
			if len(supportDecisionIDs) > 0 {
				supportDecisionID = supportDecisionIDs[0]
			}
			if err := refreshRelationshipSupportCounts(ctx, tx, input.TeamID, recordState.Record.RelationshipID); err != nil {
				return err
			}
		}
		if recordState.Changed {
			if _, err := insertRelationshipTransition(ctx, tx, transitionInput{
				TeamID:              input.TeamID,
				OwnerProfileID:      input.OwnerProfileID,
				RelationshipID:      recordState.Record.RelationshipID,
				FromStatus:          recordState.FromStatus,
				ToStatus:            status,
				Reason:              "verifier_decision",
				VerificationEventID: verificationID,
				SupportDecisionID:   supportDecisionID,
				IdempotencyKey:      relationshipTransitionIdempotencyKey(verificationID, supportDecisionID),
			}); err != nil {
				return err
			}
		}
		loaded, err := loadRelationshipRecord(ctx, tx, input.TeamID, recordState.Record.RelationshipID)
		if err != nil {
			return err
		}
		result = &RelationshipDecisionResult{
			Relationship:        loaded,
			ObservationID:       observationID,
			VerificationEventID: verificationID,
			SupportID:           supportID,
			SupportIDs:          supportIDs,
			SupportDecisionID:   supportDecisionID,
			CreatedRelationship: recordState.Created,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: apply relationship decision: %w", err)
	}
	return result, nil
}

func (r *SemanticRepositoryImpl) RetractRelationship(
	ctx context.Context,
	input RetractRelationshipInput,
) (*RelationshipTransitionResult, error) {
	input = normalizeRetractRelationshipInput(input)
	if err := validateRetractRelationshipInput(input); err != nil {
		return nil, err
	}
	var result *RelationshipTransitionResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		existing, err := loadRelationshipTransitionByIdempotency(ctx, tx, input)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.RelationshipID != input.RelationshipID || existing.ToStatus != string(domain.RelationshipStatusRetracted) {
				return ErrSemanticIdempotencyConflict
			}
			result = existing
			return nil
		}
		current, err := loadRelationshipRecord(ctx, tx, input.TeamID, input.RelationshipID)
		if err != nil {
			return err
		}
		updateResult := tx.WithContext(ctx).Exec(`
			UPDATE relationship_records
			SET status = 'retracted',
			    version = version + 1,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
			  AND owner_profile_id = ?::uuid
		`, input.TeamID, input.RelationshipID, input.OwnerProfileID)
		if updateResult.Error != nil {
			return updateResult.Error
		}
		if updateResult.RowsAffected == 0 {
			return ErrSemanticOwnerMismatch
		}
		transitionID, err := insertRelationshipTransition(ctx, tx, transitionInput{
			TeamID:         input.TeamID,
			OwnerProfileID: input.OwnerProfileID,
			RelationshipID: input.RelationshipID,
			IdempotencyKey: input.IdempotencyKey,
			FromStatus:     current.Status,
			ToStatus:       string(domain.RelationshipStatusRetracted),
			Reason:         input.Reason,
		})
		if err != nil {
			return err
		}
		result = &RelationshipTransitionResult{
			TeamID:         input.TeamID,
			TransitionID:   transitionID,
			RelationshipID: input.RelationshipID,
			FromStatus:     current.Status,
			ToStatus:       string(domain.RelationshipStatusRetracted),
			IdempotencyKey: input.IdempotencyKey,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: retract relationship: %w", err)
	}
	return result, nil
}

func (r *SemanticRepositoryImpl) ApplyRelationshipSupportDecision(
	ctx context.Context,
	input ApplyRelationshipSupportDecisionInput,
) (*RelationshipSupportDecisionResult, error) {
	input = normalizeApplyRelationshipSupportDecisionInput(input)
	if err := validateApplyRelationshipSupportDecisionInput(input); err != nil {
		return nil, err
	}
	var result *RelationshipSupportDecisionResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		existing, err := loadSupportDecisionByIdempotency(ctx, tx, input)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.RelationshipID != input.RelationshipID ||
				existing.SupportID != input.SupportID ||
				existing.Decision != input.Decision {
				return ErrSemanticIdempotencyConflict
			}
			current, err := loadRelationshipRecord(ctx, tx, input.TeamID, input.RelationshipID)
			if err != nil {
				return err
			}
			result = &RelationshipSupportDecisionResult{
				TeamID:            input.TeamID,
				SupportDecisionID: existing.SupportDecisionID,
				SupportID:         existing.SupportID,
				RelationshipID:    existing.RelationshipID,
				Decision:          existing.Decision,
				IdempotencyKey:    input.IdempotencyKey,
				FromStatus:        current.Status,
				ToStatus:          current.Status,
				SupportCount:      current.SupportCount,
				SourceGroupCount:  current.SourceGroupCount,
			}
			return nil
		}
		if err := lockOwnedRelationshipSupport(ctx, tx, input); err != nil {
			return err
		}
		decisionID, err := insertSupportDecisionEvent(ctx, tx, supportDecisionInput{
			TeamID:         input.TeamID,
			OwnerProfileID: input.OwnerProfileID,
			SupportID:      input.SupportID,
			RelationshipID: input.RelationshipID,
			Decision:       input.Decision,
			Reason:         input.Reason,
			IdempotencyKey: input.IdempotencyKey,
			Metadata:       input.Metadata,
		})
		if err != nil {
			return err
		}
		recomputed, err := recomputeRelationshipFromEffectiveSupport(
			ctx,
			tx,
			input.TeamID,
			input.RelationshipID,
			decisionID,
			"support_"+input.Decision,
		)
		if err != nil {
			return err
		}
		result = &RelationshipSupportDecisionResult{
			TeamID:            input.TeamID,
			SupportDecisionID: decisionID,
			SupportID:         input.SupportID,
			RelationshipID:    input.RelationshipID,
			Decision:          input.Decision,
			IdempotencyKey:    input.IdempotencyKey,
			FromStatus:        recomputed.Before.Status,
			ToStatus:          recomputed.After.Status,
			SupportCount:      recomputed.After.SupportCount,
			SourceGroupCount:  recomputed.After.SourceGroupCount,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: apply relationship support decision: %w", err)
	}
	return result, nil
}

func normalizeApplyRelationshipSupportDecisionInput(
	input ApplyRelationshipSupportDecisionInput,
) ApplyRelationshipSupportDecisionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.RelationshipID = strings.TrimSpace(input.RelationshipID)
	input.SupportID = strings.TrimSpace(input.SupportID)
	input.Decision = strings.TrimSpace(input.Decision)
	input.Reason = strings.TrimSpace(input.Reason)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	return input
}

func validateApplyRelationshipSupportDecisionInput(input ApplyRelationshipSupportDecisionInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.RelationshipID); err != nil {
		return fmt.Errorf("relationship_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.SupportID); err != nil {
		return fmt.Errorf("support_id is required: %w", err)
	}
	switch domain.SupportDecision(input.Decision) {
	case domain.SupportRevoke, domain.SupportReinstate:
	default:
		return fmt.Errorf("unsupported support decision %q", input.Decision)
	}
	if input.Reason == "" {
		return errors.New("reason is required")
	}
	if input.IdempotencyKey == "" {
		return errors.New("idempotency_key is required")
	}
	return nil
}

func lockOwnedRelationshipSupport(ctx context.Context, tx *gorm.DB, input ApplyRelationshipSupportDecisionInput) error {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT support_id::text
		FROM relationship_evidence_supports
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND relationship_id = ?::uuid
		  AND support_id = ?::uuid
	`, input.TeamID, input.OwnerProfileID, input.RelationshipID, input.SupportID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return ErrSemanticOwnerMismatch
	}
	var supportID string
	if err := rows.Scan(&supportID); err != nil {
		return err
	}
	return rows.Err()
}

func loadSupportDecisionByIdempotency(
	ctx context.Context,
	tx *gorm.DB,
	input ApplyRelationshipSupportDecisionInput,
) (*RelationshipSupportDecisionResult, error) {
	if input.IdempotencyKey == "" {
		return nil, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, support_decision_id::text, support_id::text,
		       relationship_id::text, decision, idempotency_key
		FROM relationship_support_decision_events
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND idempotency_key = ?
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	result := &RelationshipSupportDecisionResult{}
	if err := rows.Scan(
		&result.TeamID,
		&result.SupportDecisionID,
		&result.SupportID,
		&result.RelationshipID,
		&result.Decision,
		&result.IdempotencyKey,
	); err != nil {
		return nil, err
	}
	return result, rows.Err()
}

func loadRelationshipTransitionByIdempotency(
	ctx context.Context,
	tx *gorm.DB,
	input RetractRelationshipInput,
) (*RelationshipTransitionResult, error) {
	if input.IdempotencyKey == "" {
		return nil, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, transition_id::text, relationship_id::text,
		       COALESCE(from_status, ''), to_status, idempotency_key
		FROM relationship_transition_events
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND idempotency_key = ?
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	result := &RelationshipTransitionResult{}
	if err := rows.Scan(
		&result.TeamID,
		&result.TransitionID,
		&result.RelationshipID,
		&result.FromStatus,
		&result.ToStatus,
		&result.IdempotencyKey,
	); err != nil {
		return nil, err
	}
	return result, rows.Err()
}

func (r *SemanticRepositoryImpl) AppendCrossReference(ctx context.Context, input AppendCrossReferenceInput) (string, error) {
	input = normalizeAppendCrossReferenceInput(input)
	if err := validateAppendCrossReferenceInput(input); err != nil {
		return "", err
	}
	var crossReferenceID string
	err := r.withTeamProfileTx(ctx, input.TeamID, input.AuthorProfileID, func(tx *gorm.DB) error {
		if err := requireRelationshipVersion(ctx, tx, input.TeamID, input.SourceRelationshipID, input.AuthorProfileID, input.SourceRelationshipVersion); err != nil {
			return err
		}
		if err := requireRelationshipVersion(ctx, tx, input.TeamID, input.TargetRelationshipID, "", input.TargetRelationshipVersion); err != nil {
			return err
		}
		if err := requireVerificationForRelationship(ctx, tx, input.TeamID, input.VerificationEventID, input.AuthorProfileID, input.SourceRelationshipID); err != nil {
			return err
		}
		metadata, err := marshalJSON(input.Metadata)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			INSERT INTO relationship_cross_references (
			    team_id, author_profile_id, source_relationship_id,
			    source_relationship_version, target_relationship_id,
			    target_relationship_version, kind, verification_event_id, metadata
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?, ?, ?::uuid, ?::jsonb
			)
			RETURNING cross_reference_id::text
		`, input.TeamID, input.AuthorProfileID, input.SourceRelationshipID,
			input.SourceRelationshipVersion, input.TargetRelationshipID,
			input.TargetRelationshipVersion, input.Kind, input.VerificationEventID,
			string(metadata)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		return rows.Scan(&crossReferenceID)
	})
	if err != nil {
		return "", fmt.Errorf("semantic: append cross reference: %w", err)
	}
	return crossReferenceID, nil
}

func (r *SemanticRepositoryImpl) CreateHypothesis(ctx context.Context, input CreateHypothesisInput) (string, error) {
	input = normalizeCreateHypothesisInput(input)
	if err := validateCreateHypothesisInput(input); err != nil {
		return "", err
	}
	var hypothesisID string
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureSemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		payload, err := marshalJSON(input.Payload)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			INSERT INTO hypotheses (team_id, created_by_profile_id, status, payload)
			VALUES (?::uuid, ?::uuid, ?, ?::jsonb)
			RETURNING hypothesis_id::text
		`, input.TeamID, input.OwnerProfileID, input.Status, string(payload)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		return rows.Scan(&hypothesisID)
	})
	if err != nil {
		return "", fmt.Errorf("semantic: create hypothesis: %w", err)
	}
	return hypothesisID, nil
}

func (r *SemanticRepositoryImpl) ListSemanticEdges(ctx context.Context, teamID string, limit int) ([]SemanticEdge, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if limit <= 0 || limit > 500 {
		return nil, errors.New("limit must be between 1 and 500")
	}
	var edges []SemanticEdge
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, relationship_id::text, owner_profile_id::text,
			       semantic_group_key, subject_entity_id::text, predicate_key,
			       predicate_version, COALESCE(object_entity_id::text, ''),
			       COALESCE(object_value_id::text, ''), relationship_kind,
			       current_cardinality, polarity, COALESCE(scope_key, ''),
			       support_count, source_group_count, version
			FROM semantic_edges
			WHERE team_id = ?::uuid
			ORDER BY relationship_id
			LIMIT ?
		`, teamID, limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			edge := SemanticEdge{}
			if err := rows.Scan(&edge.TeamID, &edge.RelationshipID, &edge.OwnerProfileID,
				&edge.SemanticGroupKey, &edge.SubjectEntityID, &edge.PredicateKey,
				&edge.PredicateVersion, &edge.ObjectEntityID, &edge.ObjectValueID,
				&edge.RelationshipKind, &edge.CurrentCardinality, &edge.Polarity,
				&edge.ScopeKey, &edge.SupportCount, &edge.SourceGroupCount, &edge.Version); err != nil {
				return err
			}
			edges = append(edges, edge)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: list semantic edges: %w", err)
	}
	return edges, nil
}

func (r *SemanticRepositoryImpl) withTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("semantic: database is required")
	}
	if r.rls == nil {
		return errors.New("semantic: rls helper is required")
	}
	return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (r *SemanticRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("semantic: database is required")
	}
	if r.rls == nil {
		return errors.New("semantic: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}
