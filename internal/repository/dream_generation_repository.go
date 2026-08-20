package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

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
		var err error
		record, inserted, err = insertHypothesisTx(ctx, tx, input)
		return err
	})
	if err != nil {
		return nil, false, fmt.Errorf("dream: upsert hypothesis: %w", err)
	}
	return record, inserted, nil
}

func (r *SemanticRepositoryImpl) PersistDreamGeneration(
	ctx context.Context,
	input DreamGenerationPersistInput,
) (DreamGenerationPersistResult, error) {
	return r.persistDreamGeneration(ctx, input, false)
}

func (r *SemanticRepositoryImpl) PersistScheduledDreamGeneration(
	ctx context.Context,
	input DreamGenerationPersistInput,
) (DreamGenerationPersistResult, error) {
	return r.persistDreamGeneration(ctx, input, true)
}

func (r *SemanticRepositoryImpl) persistDreamGeneration(
	ctx context.Context,
	input DreamGenerationPersistInput,
	system bool,
) (DreamGenerationPersistResult, error) {
	input = normalizeDreamGenerationPersistInput(input)
	if err := validateDreamGenerationPersistInput(input, system); err != nil {
		return DreamGenerationPersistResult{}, err
	}
	result := DreamGenerationPersistResult{}
	err := r.withDreamWriteTx(ctx, input.TeamID, input.CreatedByProfileID, system, func(tx *gorm.DB) error {
		if err := requireCurrentDreamCycleLease(ctx, tx, input.TeamID, input.RunID, input.LeaseToken); err != nil {
			return err
		}
		for _, proposal := range input.Proposals {
			_, inserted, err := insertHypothesisTx(ctx, tx, proposal)
			if err != nil {
				if errors.Is(err, ErrDreamExactRelationshipExists) ||
					errors.Is(err, ErrDreamExactHypothesisExists) ||
					errors.Is(err, ErrDreamSourceStale) {
					result.Rejected++
					continue
				}
				return err
			}
			if inserted {
				result.Created++
			}
		}
		return insertDreamPathEvaluationsTx(ctx, tx, input.TeamID, input.ProviderModel, input.EvaluatedPaths)
	})
	if err != nil {
		return DreamGenerationPersistResult{}, fmt.Errorf("dream: persist generation: %w", err)
	}
	return result, nil
}

func insertHypothesisTx(
	ctx context.Context,
	tx *gorm.DB,
	input UpsertHypothesisInput,
) (*HypothesisRecord, bool, error) {
	if err := validateHypothesisEndpoints(ctx, tx, input); err != nil {
		return nil, false, err
	}
	if err := validateHypothesisTargetAbsent(ctx, tx, input); err != nil {
		return nil, false, err
	}
	if err := validateHypothesisSources(ctx, tx, input); err != nil {
		return nil, false, err
	}
	sourceRefs, err := marshalJSONArray(input.SourceRefs)
	if err != nil {
		return nil, false, err
	}
	sourceVersions, err := marshalIntMapJSON(input.SourceVersions)
	if err != nil {
		return nil, false, err
	}
	payload, err := marshalJSON(input.Payload)
	if err != nil {
		return nil, false, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO hypotheses (
		    team_id, space_id, space_generation, created_by_profile_id, status, statement, rationale,
		    likelihood, confidence, subject_entity_id, predicate_key,
		    predicate_version, object_entity_id, object_value_id,
		    source_refs, source_versions, source_owner_profile_ids,
		    content_hash, target_identity, cycle_run_id, generator_kind, generator_version,
		    payload
		) VALUES (
			?::uuid, dense_mem_team_shared_space(?::uuid),
			(SELECT generation FROM memory_spaces WHERE id = dense_mem_team_shared_space(?::uuid)),
			NULLIF(?, '')::uuid, 'proposed', ?, ?, ?, ?, ?::uuid, ?, ?,
		    NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?::jsonb, ?::jsonb,
		    ?::uuid[], ?, ?, ?::uuid, ?, ?, ?::jsonb
		)
		ON CONFLICT DO NOTHING
		RETURNING team_id::text, hypothesis_id::text, COALESCE(created_by_profile_id::text, ''),
		          status, statement, rationale, likelihood, confidence,
		          subject_entity_id::text, predicate_key, predicate_version,
		          COALESCE(object_entity_id::text, ''), COALESCE(object_value_id::text, ''),
		          source_refs, source_versions,
		          ARRAY(SELECT source_owner_id::text FROM unnest(source_owner_profile_ids) AS source_owner(source_owner_id)),
		          COALESCE(content_hash, ''), COALESCE(target_identity, ''), COALESCE(cycle_run_id::text, ''),
		          generator_kind, generator_version, invalidated_reason,
		          COALESCE(submitted_ingest_id::text, ''), submitted_at,
		          payload, created_at, updated_at
	`, input.TeamID, input.TeamID, input.TeamID, input.CreatedByProfileID, input.Statement, input.Rationale,
		input.Likelihood, input.Confidence, input.SubjectEntityID, input.PredicateKey,
		input.PredicateVersion, input.ObjectEntityID, input.ObjectValueID,
		string(sourceRefs), string(sourceVersions), pq.Array(input.SourceOwnerProfileIDs),
		input.ContentHash, input.TargetIdentity, input.RunID, input.GeneratorKind, input.GeneratorVersion,
		string(payload)).Rows()
	if err != nil {
		return nil, false, err
	}
	if !rows.Next() {
		rowErr := rows.Err()
		closeErr := rows.Close()
		if rowErr != nil {
			return nil, false, rowErr
		}
		if closeErr != nil {
			return nil, false, closeErr
		}
		return nil, false, ErrDreamExactHypothesisExists
	}
	loaded, err := scanHypothesisRecord(rows)
	if err != nil {
		_ = rows.Close()
		return nil, false, err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, false, err
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	if err := insertHypothesisDerivations(ctx, tx, input.TeamID, loaded.HypothesisID, input.Derivations); err != nil {
		return nil, false, err
	}
	return loaded, true, nil
}
