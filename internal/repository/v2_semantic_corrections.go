package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var (
	ErrV2SemanticCorrectionPlanStale   = errors.New("v2 semantic correction plan stale")
	ErrV2SemanticCorrectionPlanExpired = errors.New("v2 semantic correction plan expired")
)

const v2EntityCorrectionPlanTTL = 2 * time.Hour

type v2EntityCorrectionObservation struct {
	ObservationID       string
	RelationshipID      string
	RelationshipVersion int
	SubjectRef          string
	ObjectRef           string
	SubjectEntityID     string
	ObjectEntityID      string
	ObjectValueID       string
	PredicateKey        string
	PredicateVersion    int
	Polarity            string
	ScopeKey            string
	ValidFrom           sql.NullTime
	ValidTo             sql.NullTime
}

type v2EntityCorrectionRelationshipImpact struct {
	RelationshipID string   `json:"relationship_id"`
	Version        int      `json:"version"`
	ObservationIDs []string `json:"observation_ids"`
}

type v2EntityCorrectionPlanRow struct {
	TeamID                 string
	PlanToken              string
	OwnerProfileID         string
	Action                 string
	SourceEntityID         string
	TargetEntityID         string
	NewEntityID            string
	SelectedObservationIDs []string
	BlockedObservationIDs  []string
	AffectedRelationships  []v2EntityCorrectionRelationshipImpact
	ImpactSummary          string
	IdempotencyKey         string
	Status                 string
	CorrectionEventID      string
	ExpiresAt              time.Time
}

func (r *V2SemanticRepositoryImpl) CorrectEntityResolution(
	ctx context.Context,
	input V2CorrectEntityResolutionInput,
) (*V2CorrectEntityResolutionResult, error) {
	input = normalizeV2CorrectEntityResolutionInput(input)
	if err := validateV2CorrectEntityResolutionInput(input); err != nil {
		return nil, err
	}
	var result *V2CorrectEntityResolutionResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		var err error
		if input.DryRun {
			result, err = r.planV2EntityCorrection(ctx, tx, input)
		} else {
			result, err = r.applyV2EntityCorrection(ctx, tx, input)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic: correct entity resolution: %w", err)
	}
	return result, nil
}

func (r *V2SemanticRepositoryImpl) planV2EntityCorrection(
	ctx context.Context,
	tx *gorm.DB,
	input V2CorrectEntityResolutionInput,
) (*V2CorrectEntityResolutionResult, error) {
	if input.IdempotencyKey != "" {
		existing, err := loadV2EntityCorrectionPlanByIdempotency(ctx, tx, input)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			existing = nil
		} else if err != nil {
			return nil, err
		}
		if existing != nil {
			if existing.Action != input.Action ||
				existing.SourceEntityID != input.SourceEntityID ||
				existing.TargetEntityID != input.TargetEntityID {
				return nil, ErrV2SemanticIdempotencyConflict
			}
			return existing.toResult(true), nil
		}
	}
	observations, blockedIDs, err := loadV2EntityCorrectionObservations(ctx, tx, input)
	if err != nil {
		return nil, err
	}
	impacts, selectedIDs, conflictBlockedIDs, err := buildV2EntityCorrectionImpacts(ctx, tx, input, observations)
	if err != nil {
		return nil, err
	}
	blockedIDs = uniqueV2Strings(append(blockedIDs, conflictBlockedIDs...))
	if len(selectedIDs) == 0 {
		return &V2CorrectEntityResolutionResult{
			DryRun:                 true,
			SelectedObservationIDs: []string{},
			BlockedObservationIDs:  blockedIDs,
			ImpactSummary:          v2EntityCorrectionSummary(input.Action, 0, 0, len(blockedIDs), false),
		}, nil
	}
	planToken, err := insertV2EntityCorrectionPlan(ctx, tx, input, impacts, selectedIDs, blockedIDs)
	if err != nil {
		return nil, err
	}
	return &V2CorrectEntityResolutionResult{
		DryRun:                 true,
		PlanToken:              planToken,
		SelectedObservationIDs: selectedIDs,
		BlockedObservationIDs:  blockedIDs,
		ImpactSummary:          v2EntityCorrectionSummary(input.Action, len(impacts), len(selectedIDs), len(blockedIDs), false),
	}, nil
}

func (r *V2SemanticRepositoryImpl) applyV2EntityCorrection(
	ctx context.Context,
	tx *gorm.DB,
	input V2CorrectEntityResolutionInput,
) (*V2CorrectEntityResolutionResult, error) {
	plan, err := loadV2EntityCorrectionPlanByToken(ctx, tx, input.TeamID, input.OwnerProfileID, input.PlanToken, true)
	if err != nil {
		return nil, err
	}
	if plan.Action != input.Action ||
		plan.SourceEntityID != input.SourceEntityID ||
		plan.TargetEntityID != input.TargetEntityID {
		return nil, ErrV2SemanticIdempotencyConflict
	}
	if plan.Status == "applied" {
		return plan.toResult(false), nil
	}
	if time.Now().UTC().After(plan.ExpiresAt) {
		return nil, ErrV2SemanticCorrectionPlanExpired
	}
	if len(plan.AffectedRelationships) == 0 || len(plan.SelectedObservationIDs) == 0 {
		return nil, errors.New("entity correction plan has no selected caller-owned observations")
	}
	currentVersions, err := lockV2EntityCorrectionRelationships(ctx, tx, plan)
	if err != nil {
		return nil, err
	}
	for _, impact := range plan.AffectedRelationships {
		if currentVersions[impact.RelationshipID] != impact.Version {
			return nil, ErrV2SemanticCorrectionPlanStale
		}
	}
	replacementEntityID := plan.TargetEntityID
	if plan.Action == string(domain.V2EntityCorrectionSplit) {
		replacementEntityID, err = createV2EntityCorrectionSplitEntity(ctx, tx, plan)
		if err != nil {
			return nil, err
		}
	}
	if plan.Action == string(domain.V2EntityCorrectionMerge) {
		if err := ensureV2EntityExists(ctx, tx, plan.TeamID, replacementEntityID); err != nil {
			return nil, err
		}
		if err := rejectV2MergeConflictsAtApply(ctx, tx, plan, replacementEntityID); err != nil {
			return nil, err
		}
	}
	if err := updateV2EntityCorrectionRelationships(ctx, tx, plan, replacementEntityID); err != nil {
		return nil, err
	}
	correctionEventID, err := insertV2EntityCorrectionEvent(ctx, tx, plan, replacementEntityID)
	if err != nil {
		return nil, err
	}
	if err := markV2EntityCorrectionPlanApplied(ctx, tx, plan, correctionEventID, replacementEntityID); err != nil {
		return nil, err
	}
	plan.Status = "applied"
	plan.CorrectionEventID = correctionEventID
	if plan.Action == string(domain.V2EntityCorrectionSplit) {
		plan.NewEntityID = replacementEntityID
	}
	plan.ImpactSummary = v2EntityCorrectionSummary(plan.Action, len(plan.AffectedRelationships), len(plan.SelectedObservationIDs), len(plan.BlockedObservationIDs), true)
	return plan.toResult(false), nil
}

func normalizeV2CorrectEntityResolutionInput(input V2CorrectEntityResolutionInput) V2CorrectEntityResolutionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Action = strings.TrimSpace(input.Action)
	input.SourceEntityID = strings.TrimSpace(input.SourceEntityID)
	input.TargetEntityID = strings.TrimSpace(input.TargetEntityID)
	input.PlanToken = strings.TrimSpace(input.PlanToken)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.SelectedObservationIDs = uniqueV2Strings(input.SelectedObservationIDs)
	for i := range input.Evidence {
		input.Evidence[i].Content = strings.TrimSpace(input.Evidence[i].Content)
		input.Evidence[i].SourceType = strings.TrimSpace(input.Evidence[i].SourceType)
		input.Evidence[i].Authority = strings.TrimSpace(input.Evidence[i].Authority)
		input.Evidence[i].SourceGroup = strings.TrimSpace(input.Evidence[i].SourceGroup)
	}
	return input
}

func validateV2CorrectEntityResolutionInput(input V2CorrectEntityResolutionInput) error {
	for label, value := range map[string]string{
		"team_id":          input.TeamID,
		"owner_profile_id": input.OwnerProfileID,
		"source_entity_id": input.SourceEntityID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	switch domain.V2EntityCorrectionAction(input.Action) {
	case domain.V2EntityCorrectionMerge:
		if _, err := uuid.Parse(input.TargetEntityID); err != nil {
			return fmt.Errorf("target_entity_id is required: %w", err)
		}
		if input.TargetEntityID == input.SourceEntityID {
			return errors.New("target_entity_id must differ from source_entity_id")
		}
	case domain.V2EntityCorrectionSplit:
		if input.TargetEntityID != "" {
			return errors.New("target_entity_id is only accepted for merge")
		}
		if len(input.SelectedObservationIDs) == 0 {
			return errors.New("selected_observation_ids is required for split")
		}
	default:
		return fmt.Errorf("unsupported entity correction action %q", input.Action)
	}
	for _, observationID := range input.SelectedObservationIDs {
		if _, err := uuid.Parse(observationID); err != nil {
			return fmt.Errorf("selected_observation_ids contains invalid id %q: %w", observationID, err)
		}
	}
	if !input.DryRun {
		if _, err := uuid.Parse(input.PlanToken); err != nil {
			return fmt.Errorf("plan_token is required: %w", err)
		}
	}
	return nil
}

func loadV2EntityCorrectionObservations(
	ctx context.Context,
	tx *gorm.DB,
	input V2CorrectEntityResolutionInput,
) ([]v2EntityCorrectionObservation, []string, error) {
	args := []any{input.TeamID, input.OwnerProfileID, input.OwnerProfileID, input.SourceEntityID, input.SourceEntityID, input.SourceEntityID, input.SourceEntityID}
	filter := ""
	if input.Action == string(domain.V2EntityCorrectionSplit) {
		filter = "AND o.observation_id = ANY(?::uuid[])"
		args = append(args, pq.Array(input.SelectedObservationIDs))
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT o.observation_id::text,
		       r.relationship_id::text,
		       r.version,
		       o.subject_ref,
		       o.object_ref,
		       r.subject_entity_id::text,
		       COALESCE(r.object_entity_id::text, ''),
		       COALESCE(r.object_value_id::text, ''),
		       r.predicate_key,
		       r.predicate_version,
		       r.polarity,
		       COALESCE(r.scope_key, ''),
		       r.valid_from,
		       r.valid_to
		FROM relationship_observations AS o
		JOIN relationship_records AS r
		  ON r.team_id = o.team_id
		 AND r.relationship_id = o.relationship_id
		WHERE o.team_id = ?::uuid
		  AND o.owner_profile_id = ?::uuid
		  AND r.owner_profile_id = ?::uuid
		  AND r.identity_alias_of_relationship_id IS NULL
		  AND o.relationship_id IS NOT NULL
		  AND (
		      o.subject_entity_id = ?::uuid
		      OR o.object_entity_id = ?::uuid
		      OR r.subject_entity_id = ?::uuid
		      OR r.object_entity_id = ?::uuid
		  )
		`+filter+`
		ORDER BY o.created_at, o.observation_id
	`, args...).Rows()
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var observations []v2EntityCorrectionObservation
	found := map[string]struct{}{}
	for rows.Next() {
		observation := v2EntityCorrectionObservation{}
		if err := rows.Scan(
			&observation.ObservationID,
			&observation.RelationshipID,
			&observation.RelationshipVersion,
			&observation.SubjectRef,
			&observation.ObjectRef,
			&observation.SubjectEntityID,
			&observation.ObjectEntityID,
			&observation.ObjectValueID,
			&observation.PredicateKey,
			&observation.PredicateVersion,
			&observation.Polarity,
			&observation.ScopeKey,
			&observation.ValidFrom,
			&observation.ValidTo,
		); err != nil {
			return nil, nil, err
		}
		found[observation.ObservationID] = struct{}{}
		observations = append(observations, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if input.Action != string(domain.V2EntityCorrectionSplit) {
		return observations, nil, nil
	}
	var blocked []string
	for _, selectedID := range input.SelectedObservationIDs {
		if _, ok := found[selectedID]; !ok {
			blocked = append(blocked, selectedID)
		}
	}
	return observations, blocked, nil
}

func buildV2EntityCorrectionImpacts(
	ctx context.Context,
	tx *gorm.DB,
	input V2CorrectEntityResolutionInput,
	observations []v2EntityCorrectionObservation,
) ([]v2EntityCorrectionRelationshipImpact, []string, []string, error) {
	byRelationship := map[string][]v2EntityCorrectionObservation{}
	for _, observation := range observations {
		byRelationship[observation.RelationshipID] = append(byRelationship[observation.RelationshipID], observation)
	}
	relationshipIDs := make([]string, 0, len(byRelationship))
	for relationshipID := range byRelationship {
		relationshipIDs = append(relationshipIDs, relationshipID)
	}
	sort.Strings(relationshipIDs)
	var impacts []v2EntityCorrectionRelationshipImpact
	var selectedIDs []string
	var blockedIDs []string
	seenMergeKeys := map[string]struct{}{}
	for _, relationshipID := range relationshipIDs {
		group := byRelationship[relationshipID]
		base := group[0]
		if base.SubjectEntityID != input.SourceEntityID && base.ObjectEntityID != input.SourceEntityID {
			blockedIDs = append(blockedIDs, observationIDsFromV2CorrectionGroup(group)...)
			continue
		}
		if input.Action == string(domain.V2EntityCorrectionMerge) {
			key, conflict, err := v2MergeConflictKey(ctx, tx, input, base)
			if err != nil {
				return nil, nil, nil, err
			}
			if conflict {
				blockedIDs = append(blockedIDs, observationIDsFromV2CorrectionGroup(group)...)
				continue
			}
			if _, duplicate := seenMergeKeys[key]; duplicate {
				blockedIDs = append(blockedIDs, observationIDsFromV2CorrectionGroup(group)...)
				continue
			}
			seenMergeKeys[key] = struct{}{}
		}
		ids := observationIDsFromV2CorrectionGroup(group)
		selectedIDs = append(selectedIDs, ids...)
		impacts = append(impacts, v2EntityCorrectionRelationshipImpact{
			RelationshipID: relationshipID,
			Version:        base.RelationshipVersion,
			ObservationIDs: ids,
		})
	}
	return impacts, uniqueV2Strings(selectedIDs), uniqueV2Strings(blockedIDs), nil
}

func observationIDsFromV2CorrectionGroup(group []v2EntityCorrectionObservation) []string {
	ids := make([]string, 0, len(group))
	for _, observation := range group {
		ids = append(ids, observation.ObservationID)
	}
	return uniqueV2Strings(ids)
}

func v2MergeConflictKey(
	ctx context.Context,
	tx *gorm.DB,
	input V2CorrectEntityResolutionInput,
	observation v2EntityCorrectionObservation,
) (string, bool, error) {
	subjectEntityID := observation.SubjectEntityID
	objectEntityID := observation.ObjectEntityID
	if subjectEntityID == input.SourceEntityID {
		subjectEntityID = input.TargetEntityID
	}
	if objectEntityID == input.SourceEntityID {
		objectEntityID = input.TargetEntityID
	}
	key := strings.Join([]string{
		subjectEntityID,
		observation.PredicateKey,
		objectEntityID,
		observation.ObjectValueID,
		observation.Polarity,
		observation.ScopeKey,
		v2NullableTimeKey(observation.ValidFrom),
	}, "\x00")
	conflict, err := hasV2RelationshipIdentityConflict(ctx, tx, input.TeamID, input.OwnerProfileID, observation.RelationshipID, subjectEntityID, objectEntityID, observation)
	return key, conflict, err
}

func hasV2RelationshipIdentityConflict(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	ownerProfileID string,
	relationshipID string,
	subjectEntityID string,
	objectEntityID string,
	observation v2EntityCorrectionObservation,
) (bool, error) {
	var existing string
	err := tx.WithContext(ctx).Raw(`
		SELECT relationship_id::text
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND relationship_id <> ?::uuid
		  AND subject_entity_id = ?::uuid
		  AND predicate_key = ?
		  AND object_entity_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND object_value_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND polarity = ?
		  AND valid_from IS NOT DISTINCT FROM ?
		  AND scope_key IS NOT DISTINCT FROM NULLIF(?, '')
		  AND identity_alias_of_relationship_id IS NULL
		LIMIT 1
	`, teamID, ownerProfileID, relationshipID, subjectEntityID, observation.PredicateKey,
		objectEntityID, observation.ObjectValueID,
		observation.Polarity, v2NullableTimeArg(observation.ValidFrom),
		observation.ScopeKey).Row().Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return existing != "", err
}

func insertV2EntityCorrectionPlan(
	ctx context.Context,
	tx *gorm.DB,
	input V2CorrectEntityResolutionInput,
	impacts []v2EntityCorrectionRelationshipImpact,
	selectedIDs []string,
	blockedIDs []string,
) (string, error) {
	affected, err := json.Marshal(impacts)
	if err != nil {
		return "", fmt.Errorf("marshal affected relationships: %w", err)
	}
	evidenceInput := input.Evidence
	if evidenceInput == nil {
		evidenceInput = []V2CorrectionEvidenceInput{}
	}
	evidence, err := json.Marshal(evidenceInput)
	if err != nil {
		return "", fmt.Errorf("marshal correction evidence: %w", err)
	}
	metadata, err := marshalV2JSON(map[string]any{
		"contract": "correct_entity_resolution",
	})
	if err != nil {
		return "", err
	}
	summary := v2EntityCorrectionSummary(input.Action, len(impacts), len(selectedIDs), len(blockedIDs), false)
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO entity_correction_plans (
		    team_id, owner_profile_id, action, source_entity_id, target_entity_id,
		    selected_observation_ids, blocked_observation_ids, affected_relationships,
		    evidence, impact_summary, idempotency_key, expires_at, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?, ?::uuid, NULLIF(?, '')::uuid,
		    ?::uuid[], ?::uuid[], ?::jsonb, ?::jsonb, ?, ?, ?, ?::jsonb
		)
		RETURNING plan_token::text
	`, input.TeamID, input.OwnerProfileID, input.Action, input.SourceEntityID,
		input.TargetEntityID, pq.Array(selectedIDs), pq.Array(blockedIDs),
		string(affected), string(evidence), summary, input.IdempotencyKey,
		time.Now().UTC().Add(v2EntityCorrectionPlanTTL), string(metadata)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var planToken string
	if err := rows.Scan(&planToken); err != nil {
		return "", err
	}
	return planToken, rows.Err()
}

func loadV2EntityCorrectionPlanByIdempotency(
	ctx context.Context,
	tx *gorm.DB,
	input V2CorrectEntityResolutionInput,
) (*v2EntityCorrectionPlanRow, error) {
	if input.IdempotencyKey == "" {
		return nil, nil
	}
	return scanV2EntityCorrectionPlan(ctx, tx, `
		SELECT team_id::text, plan_token::text, owner_profile_id::text, action,
		       source_entity_id::text, COALESCE(target_entity_id::text, ''),
		       COALESCE(new_entity_id::text, ''),
		       ARRAY(SELECT unnest(selected_observation_ids)::text),
		       ARRAY(SELECT unnest(blocked_observation_ids)::text),
		       affected_relationships, impact_summary, idempotency_key, status,
		       COALESCE(correction_event_id::text, ''), expires_at
		FROM entity_correction_plans
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND idempotency_key = ?
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey)
}

func loadV2EntityCorrectionPlanByToken(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	ownerProfileID string,
	planToken string,
	forUpdate bool,
) (*v2EntityCorrectionPlanRow, error) {
	lockClause := ""
	if forUpdate {
		lockClause = "FOR UPDATE"
	}
	return scanV2EntityCorrectionPlan(ctx, tx, `
		SELECT team_id::text, plan_token::text, owner_profile_id::text, action,
		       source_entity_id::text, COALESCE(target_entity_id::text, ''),
		       COALESCE(new_entity_id::text, ''),
		       ARRAY(SELECT unnest(selected_observation_ids)::text),
		       ARRAY(SELECT unnest(blocked_observation_ids)::text),
		       affected_relationships, impact_summary, idempotency_key, status,
		       COALESCE(correction_event_id::text, ''), expires_at
		FROM entity_correction_plans
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND plan_token = ?::uuid
		`+lockClause+`
	`, teamID, ownerProfileID, planToken)
}

func scanV2EntityCorrectionPlan(ctx context.Context, tx *gorm.DB, query string, args ...any) (*v2EntityCorrectionPlanRow, error) {
	rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, gorm.ErrRecordNotFound
	}
	var selected pq.StringArray
	var blocked pq.StringArray
	var affectedJSON []byte
	plan := &v2EntityCorrectionPlanRow{}
	if err := rows.Scan(
		&plan.TeamID,
		&plan.PlanToken,
		&plan.OwnerProfileID,
		&plan.Action,
		&plan.SourceEntityID,
		&plan.TargetEntityID,
		&plan.NewEntityID,
		&selected,
		&blocked,
		&affectedJSON,
		&plan.ImpactSummary,
		&plan.IdempotencyKey,
		&plan.Status,
		&plan.CorrectionEventID,
		&plan.ExpiresAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(affectedJSON, &plan.AffectedRelationships); err != nil {
		return nil, fmt.Errorf("decode affected relationships: %w", err)
	}
	plan.SelectedObservationIDs = []string(selected)
	plan.BlockedObservationIDs = []string(blocked)
	return plan, rows.Err()
}

func (p *v2EntityCorrectionPlanRow) toResult(dryRun bool) *V2CorrectEntityResolutionResult {
	return &V2CorrectEntityResolutionResult{
		DryRun:                 dryRun,
		PlanToken:              p.PlanToken,
		SelectedObservationIDs: p.SelectedObservationIDs,
		BlockedObservationIDs:  p.BlockedObservationIDs,
		ImpactSummary:          p.ImpactSummary,
		CorrectionEventID:      p.CorrectionEventID,
		NewEntityID:            p.NewEntityID,
	}
}

func v2EntityCorrectionSummary(action string, relationships int, observations int, blocked int, applied bool) string {
	state := "will update"
	if applied {
		state = "updated"
	}
	return fmt.Sprintf("%s %s %d caller-owned relationship(s) across %d observation(s); %d observation(s) blocked", action, state, relationships, observations, blocked)
}

func uniqueV2Strings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func v2NullableTimeArg(value sql.NullTime) any {
	if !value.Valid {
		return nil
	}
	return value.Time.UTC()
}

func v2NullableTimeKey(value sql.NullTime) string {
	if !value.Valid {
		return ""
	}
	return value.Time.UTC().Format(time.RFC3339Nano)
}
