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

var ErrV2SemanticOwnerMismatch = errors.New("v2 semantic owner mismatch")
var ErrV2SemanticIdempotencyConflict = errors.New("v2 semantic idempotency conflict")

type V2SemanticRepositoryImpl struct {
	db  *gorm.DB
	rls v2RLSHelper
}

var _ V2SemanticRepository = (*V2SemanticRepositoryImpl)(nil)

func NewV2SemanticRepository(db *gorm.DB, rls *postgres.RLS) *V2SemanticRepositoryImpl {
	return &V2SemanticRepositoryImpl{db: db, rls: rls}
}

func (r *V2SemanticRepositoryImpl) CreateEntity(ctx context.Context, input V2CreateEntityInput) (*V2EntityRecord, error) {
	input = normalizeV2CreateEntityInput(input)
	if err := validateV2CreateEntityInput(input); err != nil {
		return nil, err
	}
	var record *V2EntityRecord
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		identityContext, err := marshalV2JSON(input.IdentityContext)
		if err != nil {
			return err
		}
		metadata, err := marshalV2JSON(input.Metadata)
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
		created := V2EntityRecord{CanonicalName: input.CanonicalName}
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
		if _, err := insertV2EntityName(ctx, tx, V2AddEntityNameInput{
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
		return nil, fmt.Errorf("v2 semantic: create entity: %w", err)
	}
	return record, nil
}

func (r *V2SemanticRepositoryImpl) AddEntityName(ctx context.Context, input V2AddEntityNameInput) (string, error) {
	input = normalizeV2AddEntityNameInput(input)
	if err := validateV2AddEntityNameInput(input); err != nil {
		return "", err
	}
	var nameID string
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		var err error
		nameID, err = insertV2EntityName(ctx, tx, input)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("v2 semantic: add entity name: %w", err)
	}
	return nameID, nil
}

func (r *V2SemanticRepositoryImpl) UpsertValue(ctx context.Context, input V2UpsertValueInput) (*V2ValueRecord, error) {
	input = normalizeV2UpsertValueInput(input)
	if err := validateV2UpsertValueInput(input); err != nil {
		return nil, err
	}
	var record *V2ValueRecord
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		metadata, err := marshalV2JSON(input.Metadata)
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
		loaded, scanErr := scanV2ValueRows(rows)
		closeErr := rows.Close()
		if scanErr != nil {
			return scanErr
		}
		if closeErr != nil {
			return closeErr
		}
		if loaded == nil {
			loaded, err = selectV2ValueByKey(ctx, tx, input)
			if err != nil {
				return err
			}
		}
		record = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic: upsert value: %w", err)
	}
	return record, nil
}

func scanV2ValueRows(rows *sql.Rows) (*V2ValueRecord, error) {
	if !rows.Next() {
		return nil, rows.Err()
	}
	loaded := V2ValueRecord{}
	if err := rows.Scan(&loaded.TeamID, &loaded.ValueID, &loaded.ValueType, &loaded.CanonicalValue,
		&loaded.Unit, &loaded.Display, &loaded.NormalizationVersion, &loaded.Existing); err != nil {
		return nil, err
	}
	return &loaded, rows.Err()
}

func selectV2ValueByKey(ctx context.Context, tx *gorm.DB, input V2UpsertValueInput) (*V2ValueRecord, error) {
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
	record, err := scanV2ValueRows(rows)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return record, nil
}

func (r *V2SemanticRepositoryImpl) ApplyRelationshipDecision(
	ctx context.Context,
	input V2ApplyRelationshipDecisionInput,
) (*V2RelationshipDecisionResult, error) {
	input = normalizeV2ApplyRelationshipDecisionInput(input)
	if err := validateV2ApplyRelationshipDecisionInput(input); err != nil {
		return nil, err
	}
	var result *V2RelationshipDecisionResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		if err := validateV2SupportOwnership(ctx, tx, input); err != nil {
			return err
		}
		predicate, err := loadV2PredicateDefinition(ctx, tx, input.PredicateKey, input.PredicateVersion)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			review, err := insertV2PredicateReview(ctx, tx, input)
			if err != nil {
				return err
			}
			result = review
			return nil
		}
		if err != nil {
			return err
		}
		if err := validateV2RelationshipEndpointKinds(ctx, tx, input, predicate); err != nil {
			return err
		}
		tier, status := v2TierStatusForVerdict(input.EvidenceVerdict, input.PromoteToFact)
		groupKey := v2SemanticGroupKey(input)
		recordState, err := upsertV2RelationshipRecord(ctx, tx, input, predicate, tier, status, groupKey)
		if err != nil {
			return err
		}
		observationID, err := insertV2RelationshipObservation(ctx, tx, input, recordState.Record.RelationshipID)
		if err != nil {
			return err
		}
		verificationID, err := insertV2VerificationEvent(ctx, tx, input, observationID)
		if err != nil {
			return err
		}
		var supportID, supportDecisionID string
		if input.EvidenceVerdict == string(domain.V2VerificationEntailed) && input.Support != nil {
			supportID, supportDecisionID, err = insertV2RelationshipSupport(ctx, tx, input, recordState.Record.RelationshipID, observationID, verificationID)
			if err != nil {
				return err
			}
			if err := refreshV2RelationshipSupportCounts(ctx, tx, input.TeamID, recordState.Record.RelationshipID); err != nil {
				return err
			}
		}
		if recordState.Changed {
			if _, err := insertV2RelationshipTransition(ctx, tx, v2TransitionInput{
				TeamID:              input.TeamID,
				OwnerProfileID:      input.OwnerProfileID,
				RelationshipID:      recordState.Record.RelationshipID,
				FromTier:            recordState.FromTier,
				FromStatus:          recordState.FromStatus,
				ToTier:              tier,
				ToStatus:            status,
				Reason:              "verifier_decision",
				VerificationEventID: verificationID,
				SupportDecisionID:   supportDecisionID,
			}); err != nil {
				return err
			}
		}
		loaded, err := loadV2RelationshipRecord(ctx, tx, input.TeamID, recordState.Record.RelationshipID)
		if err != nil {
			return err
		}
		result = &V2RelationshipDecisionResult{
			Relationship:        loaded,
			ObservationID:       observationID,
			VerificationEventID: verificationID,
			SupportID:           supportID,
			SupportDecisionID:   supportDecisionID,
			CreatedRelationship: recordState.Created,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic: apply relationship decision: %w", err)
	}
	return result, nil
}

func (r *V2SemanticRepositoryImpl) RetractRelationship(
	ctx context.Context,
	input V2RetractRelationshipInput,
) (*V2RelationshipTransitionResult, error) {
	input = normalizeV2RetractRelationshipInput(input)
	if err := validateV2RetractRelationshipInput(input); err != nil {
		return nil, err
	}
	var result *V2RelationshipTransitionResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		existing, err := loadV2RelationshipTransitionByIdempotency(ctx, tx, input)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.RelationshipID != input.RelationshipID || existing.ToStatus != string(domain.V2RelationshipStatusRetracted) {
				return ErrV2SemanticIdempotencyConflict
			}
			result = existing
			return nil
		}
		current, err := loadV2RelationshipRecord(ctx, tx, input.TeamID, input.RelationshipID)
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
			return ErrV2SemanticOwnerMismatch
		}
		transitionID, err := insertV2RelationshipTransition(ctx, tx, v2TransitionInput{
			TeamID:         input.TeamID,
			OwnerProfileID: input.OwnerProfileID,
			RelationshipID: input.RelationshipID,
			IdempotencyKey: input.IdempotencyKey,
			FromTier:       current.Tier,
			FromStatus:     current.Status,
			ToTier:         current.Tier,
			ToStatus:       string(domain.V2RelationshipStatusRetracted),
			Reason:         input.Reason,
		})
		if err != nil {
			return err
		}
		result = &V2RelationshipTransitionResult{
			TeamID:         input.TeamID,
			TransitionID:   transitionID,
			RelationshipID: input.RelationshipID,
			FromTier:       current.Tier,
			FromStatus:     current.Status,
			ToTier:         current.Tier,
			ToStatus:       string(domain.V2RelationshipStatusRetracted),
			IdempotencyKey: input.IdempotencyKey,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic: retract relationship: %w", err)
	}
	return result, nil
}

func (r *V2SemanticRepositoryImpl) ApplyRelationshipSupportDecision(
	ctx context.Context,
	input V2ApplyRelationshipSupportDecisionInput,
) (*V2RelationshipSupportDecisionResult, error) {
	input = normalizeV2ApplyRelationshipSupportDecisionInput(input)
	if err := validateV2ApplyRelationshipSupportDecisionInput(input); err != nil {
		return nil, err
	}
	var result *V2RelationshipSupportDecisionResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		existing, err := loadV2SupportDecisionByIdempotency(ctx, tx, input)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.RelationshipID != input.RelationshipID ||
				existing.SupportID != input.SupportID ||
				existing.Decision != input.Decision {
				return ErrV2SemanticIdempotencyConflict
			}
			current, err := loadV2RelationshipRecord(ctx, tx, input.TeamID, input.RelationshipID)
			if err != nil {
				return err
			}
			result = &V2RelationshipSupportDecisionResult{
				TeamID:            input.TeamID,
				SupportDecisionID: existing.SupportDecisionID,
				SupportID:         existing.SupportID,
				RelationshipID:    existing.RelationshipID,
				Decision:          existing.Decision,
				IdempotencyKey:    input.IdempotencyKey,
				FromTier:          current.Tier,
				FromStatus:        current.Status,
				ToTier:            current.Tier,
				ToStatus:          current.Status,
				SupportCount:      current.SupportCount,
				SourceGroupCount:  current.SourceGroupCount,
			}
			return nil
		}
		if err := lockV2OwnedRelationshipSupport(ctx, tx, input); err != nil {
			return err
		}
		decisionID, err := insertV2SupportDecisionEvent(ctx, tx, v2SupportDecisionInput{
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
		recomputed, err := recomputeV2RelationshipFromEffectiveSupport(
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
		result = &V2RelationshipSupportDecisionResult{
			TeamID:            input.TeamID,
			SupportDecisionID: decisionID,
			SupportID:         input.SupportID,
			RelationshipID:    input.RelationshipID,
			Decision:          input.Decision,
			IdempotencyKey:    input.IdempotencyKey,
			FromTier:          recomputed.Before.Tier,
			FromStatus:        recomputed.Before.Status,
			ToTier:            recomputed.After.Tier,
			ToStatus:          recomputed.After.Status,
			SupportCount:      recomputed.After.SupportCount,
			SourceGroupCount:  recomputed.After.SourceGroupCount,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic: apply relationship support decision: %w", err)
	}
	return result, nil
}

func normalizeV2ApplyRelationshipSupportDecisionInput(
	input V2ApplyRelationshipSupportDecisionInput,
) V2ApplyRelationshipSupportDecisionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.RelationshipID = strings.TrimSpace(input.RelationshipID)
	input.SupportID = strings.TrimSpace(input.SupportID)
	input.Decision = strings.TrimSpace(input.Decision)
	input.Reason = strings.TrimSpace(input.Reason)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	return input
}

func validateV2ApplyRelationshipSupportDecisionInput(input V2ApplyRelationshipSupportDecisionInput) error {
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
	switch domain.V2SupportDecision(input.Decision) {
	case domain.V2SupportRevoke, domain.V2SupportReinstate:
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

func lockV2OwnedRelationshipSupport(ctx context.Context, tx *gorm.DB, input V2ApplyRelationshipSupportDecisionInput) error {
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
		return ErrV2SemanticOwnerMismatch
	}
	var supportID string
	if err := rows.Scan(&supportID); err != nil {
		return err
	}
	return rows.Err()
}

func loadV2SupportDecisionByIdempotency(
	ctx context.Context,
	tx *gorm.DB,
	input V2ApplyRelationshipSupportDecisionInput,
) (*V2RelationshipSupportDecisionResult, error) {
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
	result := &V2RelationshipSupportDecisionResult{}
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

func loadV2RelationshipTransitionByIdempotency(
	ctx context.Context,
	tx *gorm.DB,
	input V2RetractRelationshipInput,
) (*V2RelationshipTransitionResult, error) {
	if input.IdempotencyKey == "" {
		return nil, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, transition_id::text, relationship_id::text,
		       COALESCE(from_tier, ''), COALESCE(from_status, ''),
		       to_tier, to_status, idempotency_key
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
	result := &V2RelationshipTransitionResult{}
	if err := rows.Scan(
		&result.TeamID,
		&result.TransitionID,
		&result.RelationshipID,
		&result.FromTier,
		&result.FromStatus,
		&result.ToTier,
		&result.ToStatus,
		&result.IdempotencyKey,
	); err != nil {
		return nil, err
	}
	return result, rows.Err()
}

func (r *V2SemanticRepositoryImpl) AppendCrossReference(ctx context.Context, input V2AppendCrossReferenceInput) (string, error) {
	input = normalizeV2AppendCrossReferenceInput(input)
	if err := validateV2AppendCrossReferenceInput(input); err != nil {
		return "", err
	}
	var crossReferenceID string
	err := r.withTeamProfileTx(ctx, input.TeamID, input.AuthorProfileID, func(tx *gorm.DB) error {
		if err := requireV2RelationshipVersion(ctx, tx, input.TeamID, input.SourceRelationshipID, input.AuthorProfileID, input.SourceRelationshipVersion); err != nil {
			return err
		}
		if err := requireV2RelationshipVersion(ctx, tx, input.TeamID, input.TargetRelationshipID, "", input.TargetRelationshipVersion); err != nil {
			return err
		}
		if err := requireV2VerificationForRelationship(ctx, tx, input.TeamID, input.VerificationEventID, input.AuthorProfileID, input.SourceRelationshipID); err != nil {
			return err
		}
		metadata, err := marshalV2JSON(input.Metadata)
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
		return "", fmt.Errorf("v2 semantic: append cross reference: %w", err)
	}
	return crossReferenceID, nil
}

func (r *V2SemanticRepositoryImpl) CreateHypothesis(ctx context.Context, input V2CreateHypothesisInput) (string, error) {
	input = normalizeV2CreateHypothesisInput(input)
	if err := validateV2CreateHypothesisInput(input); err != nil {
		return "", err
	}
	var hypothesisID string
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		payload, err := marshalV2JSON(input.Payload)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			INSERT INTO hypotheses (team_id, owner_profile_id, status, payload)
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
		return "", fmt.Errorf("v2 semantic: create hypothesis: %w", err)
	}
	return hypothesisID, nil
}

func (r *V2SemanticRepositoryImpl) ListSemanticEdges(ctx context.Context, teamID string, limit int) ([]V2SemanticEdge, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if limit <= 0 || limit > 500 {
		return nil, errors.New("limit must be between 1 and 500")
	}
	var edges []V2SemanticEdge
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, relationship_id::text, owner_profile_id::text,
			       semantic_group_key, subject_entity_id::text, predicate_key,
			       predicate_version, COALESCE(object_entity_id::text, ''),
			       COALESCE(object_value_id::text, ''), relationship_kind,
			       current_cardinality, polarity, COALESCE(scope_key, ''), tier,
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
			edge := V2SemanticEdge{}
			if err := rows.Scan(&edge.TeamID, &edge.RelationshipID, &edge.OwnerProfileID,
				&edge.SemanticGroupKey, &edge.SubjectEntityID, &edge.PredicateKey,
				&edge.PredicateVersion, &edge.ObjectEntityID, &edge.ObjectValueID,
				&edge.RelationshipKind, &edge.CurrentCardinality, &edge.Polarity,
				&edge.ScopeKey, &edge.Tier, &edge.SupportCount, &edge.SourceGroupCount,
				&edge.Version); err != nil {
				return err
			}
			edges = append(edges, edge)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic: list semantic edges: %w", err)
	}
	return edges, nil
}

func (r *V2SemanticRepositoryImpl) withTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("v2 semantic: database is required")
	}
	if r.rls == nil {
		return errors.New("v2 semantic: rls helper is required")
	}
	return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, fn)
}

func (r *V2SemanticRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("v2 semantic: database is required")
	}
	if r.rls == nil {
		return errors.New("v2 semantic: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}
