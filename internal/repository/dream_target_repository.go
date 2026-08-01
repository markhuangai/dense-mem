package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (r *SemanticRepositoryImpl) ListAvailableDreamTargets(ctx context.Context, teamID string, targets []DreamTargetCandidate) ([]DreamTargetCandidate, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	normalized, err := normalizeDreamTargetCandidates(teamID, targets)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return []DreamTargetCandidate{}, nil
	}
	payload, err := marshalDreamTargetCandidates(teamID, normalized)
	if err != nil {
		return nil, err
	}
	byRef := make(map[dreamTargetCandidateRef]DreamTargetCandidate, len(normalized))
	for _, target := range normalized {
		byRef[dreamTargetCandidateRef{PathRef: target.PathRef, PredicateRef: target.PredicateRef}] = target
	}
	available := make([]DreamTargetCandidate, 0, len(normalized))
	err = r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH candidate_rows AS (
				SELECT path_ref,
				       predicate_ref,
				       subject_entity_id::uuid AS subject_entity_id,
				       lower(btrim(predicate_key)) AS predicate_key,
				       NULLIF(object_entity_id, '')::uuid AS object_entity_id,
				       NULLIF(object_value_id, '')::uuid AS object_value_id,
				       target_identity,
				       ordinal
				FROM jsonb_to_recordset(?::jsonb) AS candidate(
					path_ref text,
					predicate_ref text,
					subject_entity_id text,
					predicate_key text,
					object_entity_id text,
					object_value_id text,
					target_identity text,
					ordinal integer
				)
			), distinct_targets AS (
				SELECT DISTINCT ON (target_identity)
				       subject_entity_id,
				       predicate_key,
				       object_entity_id,
				       object_value_id,
				       target_identity
				FROM candidate_rows
				ORDER BY target_identity, ordinal
			), available_targets AS (
				SELECT target.target_identity
				FROM distinct_targets target
				WHERE NOT EXISTS (
				SELECT 1
				FROM relationship_records relationship
				WHERE relationship.team_id = ?::uuid
				  AND relationship.identity_alias_of_relationship_id IS NULL
				  AND relationship.subject_entity_id = target.subject_entity_id
				  AND lower(btrim(relationship.predicate_key)) = target.predicate_key
				  AND relationship.object_entity_id IS NOT DISTINCT FROM target.object_entity_id
				  AND relationship.object_value_id IS NOT DISTINCT FROM target.object_value_id
				)
				AND NOT EXISTS (
				SELECT 1
				FROM hypotheses hypothesis
				WHERE hypothesis.team_id = ?::uuid
				  AND hypothesis.canonical_hypothesis_id IS NULL
				  AND (
				    hypothesis.target_identity = target.target_identity
				    OR (
				      hypothesis.target_identity IS NULL
				      AND hypothesis.subject_entity_id = target.subject_entity_id
				      AND lower(btrim(hypothesis.predicate_key)) = target.predicate_key
				      AND hypothesis.object_entity_id IS NOT DISTINCT FROM target.object_entity_id
				      AND hypothesis.object_value_id IS NOT DISTINCT FROM target.object_value_id
				    )
				  )
				)
			)
			SELECT candidate.path_ref, candidate.predicate_ref
			FROM candidate_rows candidate
			JOIN available_targets target ON target.target_identity = candidate.target_identity
			ORDER BY candidate.ordinal
		`, string(payload), teamID, teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ref dreamTargetCandidateRef
			if err := rows.Scan(&ref.PathRef, &ref.PredicateRef); err != nil {
				return err
			}
			target, ok := byRef[ref]
			if !ok {
				return fmt.Errorf("dream target query returned unknown candidate")
			}
			available = append(available, target)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("dream: list available targets: %w", err)
	}
	return available, nil
}

type dreamTargetCandidateRef struct {
	PathRef      string
	PredicateRef string
}

type dreamTargetCandidatePayload struct {
	PathRef         string `json:"path_ref"`
	PredicateRef    string `json:"predicate_ref"`
	SubjectEntityID string `json:"subject_entity_id"`
	PredicateKey    string `json:"predicate_key"`
	ObjectEntityID  string `json:"object_entity_id"`
	ObjectValueID   string `json:"object_value_id"`
	TargetIdentity  string `json:"target_identity"`
	Ordinal         int    `json:"ordinal"`
}

func marshalDreamTargetCandidates(teamID string, targets []DreamTargetCandidate) ([]byte, error) {
	payload := make([]dreamTargetCandidatePayload, 0, len(targets))
	for index, target := range targets {
		payload = append(payload, dreamTargetCandidatePayload{
			PathRef:         target.PathRef,
			PredicateRef:    target.PredicateRef,
			SubjectEntityID: target.SubjectEntityID,
			PredicateKey:    target.PredicateKey,
			ObjectEntityID:  target.ObjectEntityID,
			ObjectValueID:   target.ObjectValueID,
			TargetIdentity:  hypothesisTargetIdentity(teamID, target.SubjectEntityID, target.PredicateKey, target.ObjectEntityID, target.ObjectValueID),
			Ordinal:         index,
		})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal dream targets: %w", err)
	}
	return data, nil
}

func normalizeDreamTargetCandidates(teamID string, targets []DreamTargetCandidate) ([]DreamTargetCandidate, error) {
	seen := map[string]struct{}{}
	result := make([]DreamTargetCandidate, 0, len(targets))
	for index, target := range targets {
		target.PathRef = strings.TrimSpace(target.PathRef)
		target.PredicateRef = strings.TrimSpace(target.PredicateRef)
		target.SubjectEntityID = strings.TrimSpace(target.SubjectEntityID)
		target.PredicateKey = strings.TrimSpace(target.PredicateKey)
		target.ObjectEntityID = strings.TrimSpace(target.ObjectEntityID)
		target.ObjectValueID = strings.TrimSpace(target.ObjectValueID)
		if target.PathRef == "" || target.PredicateRef == "" || target.PredicateKey == "" {
			return nil, fmt.Errorf("targets[%d] requires path, predicate, and predicate_key", index)
		}
		if _, err := uuid.Parse(target.SubjectEntityID); err != nil {
			return nil, fmt.Errorf("targets[%d].subject_entity_id is invalid: %w", index, err)
		}
		if (target.ObjectEntityID == "") == (target.ObjectValueID == "") {
			return nil, fmt.Errorf("targets[%d] requires exactly one object endpoint", index)
		}
		if target.ObjectEntityID != "" {
			if _, err := uuid.Parse(target.ObjectEntityID); err != nil {
				return nil, fmt.Errorf("targets[%d].object_entity_id is invalid: %w", index, err)
			}
		}
		if target.ObjectValueID != "" {
			if _, err := uuid.Parse(target.ObjectValueID); err != nil {
				return nil, fmt.Errorf("targets[%d].object_value_id is invalid: %w", index, err)
			}
		}
		key := target.PathRef + "\x00" + target.PredicateRef
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, target)
	}
	return result, nil
}
