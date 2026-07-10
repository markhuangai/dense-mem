package neo4j

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/assertionservice"
	"github.com/markhuangai/dense-mem/internal/service/graphrow"
	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const (
	mergeEntityCypher = `
MERGE (entity:Entity {team_id: $profileId, entity_id: $entityId})
ON CREATE SET entity.first_seen_at = $firstSeenAt,
              entity.canonical_name = $canonicalName,
              entity.normalized_name = $normalizedName,
              entity.entity_type = $entityType,
              entity.aliases = [],
              entity.resolution_status = $resolutionStatus,
              entity.resolution_conf = $resolutionConf
SET entity.graph_key = $graphKey,
    entity.aliases = reduce(
      aliases = coalesce(entity.aliases, []),
      alias IN $aliases |
      CASE WHEN alias IN aliases THEN aliases ELSE aliases + alias END
    ),
    entity.resolution_status = CASE
      WHEN $resolutionConf >= coalesce(entity.resolution_conf, 0) THEN $resolutionStatus
      ELSE entity.resolution_status
    END,
    entity.resolution_conf = CASE
      WHEN $resolutionConf > coalesce(entity.resolution_conf, 0) THEN $resolutionConf
      ELSE coalesce(entity.resolution_conf, 0)
    END,
    entity.last_seen_at = $lastSeenAt,
    entity.embedding_model = $embeddingModel
FOREACH (_ IN CASE WHEN size($embedding) > 0 THEN [1] ELSE [] END |
  SET entity.embedding = $embedding
)
RETURN entity.entity_id AS entity_id`

	mergeValueCypher = `
MERGE (value:Value {team_id: $profileId, value_id: $valueId})
SET value.graph_key = $graphKey,
    value.value_type = $valueType,
    value.value = $value,
    value.display = $display,
    value.unit = $unit
RETURN value.value_id AS value_id`

	mergePredicateCypher = `
MERGE (predicate:Predicate {team_id: $profileId, predicate_key: $predicateKey})
ON CREATE SET predicate.registry_id = $registryId,
              predicate.relationship_type = $relationshipType,
              predicate.policy_family = $policyFamily,
              predicate.created_at = $updatedAt
WITH predicate
WHERE predicate.relationship_type = $relationshipType
  AND predicate.policy_family = $policyFamily
SET predicate.updated_at = $updatedAt
RETURN predicate.predicate_key AS predicate_key`

	mergeAssertionCypher = `
MERGE (assertion:Assertion {team_id: $profileId, assertion_id: $assertionId})
ON CREATE SET assertion.created_at = $createdAt
SET assertion.owner_profile_id = $ownerProfileId,
    assertion.subject_entity_id = $subjectEntityId,
    assertion.predicate_key = $predicateKey,
    assertion.relationship_type = $relationshipType,
    assertion.object_entity_id = $objectEntityId,
    assertion.object_value_id = $objectValueId,
    assertion.search_text = $searchText,
    assertion.tier = $tier,
    assertion.status = $status,
    assertion.policy_family = $policyFamily,
    assertion.polarity = $polarity,
    assertion.modality = $modality,
    assertion.valid_from = $validFrom,
    assertion.valid_to = $validTo,
    assertion.recorded_at = $recordedAt,
    assertion.recorded_to = $recordedTo,
    assertion.extract_conf = $extractConf,
    assertion.resolution_conf = $resolutionConf,
    assertion.source_quality = $sourceQuality,
    assertion.support_count = $supportCount,
    assertion.source_group_count = $sourceGroupCount,
    assertion.evidence_json = $evidenceJson,
    assertion.embedding_model = $embeddingModel,
    assertion.extraction_model = $extractionModel,
    assertion.extraction_version = $extractionVersion,
    assertion.verifier_model = $verifierModel,
    assertion.pipeline_run_id = $pipelineRunId,
    assertion.projection_version = $projectionVersion,
    assertion.updated_at = $updatedAt
FOREACH (_ IN CASE WHEN size($embedding) > 0 THEN [1] ELSE [] END |
  SET assertion.embedding = $embedding
)
RETURN assertion.assertion_id AS assertion_id`

	mergeSupportCypher = `
MATCH (assertion:Assertion {team_id: $profileId, assertion_id: $assertionId})
UNWIND $evidence AS evidence
MATCH (fragment:SourceFragment {team_id: $profileId, fragment_id: evidence.fragment_id})
MERGE (assertion)-[support:SUPPORTED_BY {
  team_id: $profileId,
  fragment_id: evidence.fragment_id,
  source_group: evidence.source_group
}]->(fragment)
SET support.span_start = evidence.span_start,
    support.span_end = evidence.span_end
RETURN count(support) AS support_count`

	mergeMentionsCypher = `
MATCH (assertion:Assertion {team_id: $profileId, assertion_id: $assertionId})
UNWIND $evidence AS evidence
MATCH (fragment:SourceFragment {team_id: $profileId, fragment_id: evidence.fragment_id})
UNWIND $entityIds AS entityId
MATCH (entity:Entity {team_id: $profileId, entity_id: entityId})
MERGE (fragment)-[mention:MENTIONS {team_id: $profileId}]->(entity)
RETURN count(mention) AS mention_count`

	deleteProjectionCypher = `
MATCH ()-[projection]->()
WHERE projection.team_id = $profileId
  AND projection.assertion_id = $assertionId
  AND projection.semantic_projection = true
DELETE projection`

	createProjectionCypher = `
MATCH (subject:Entity {team_id: $profileId, entity_id: $subjectEntityId})
MATCH (object {team_id: $profileId, graph_key: $objectGraphKey})
CREATE (subject)-[projection:$($relationshipType)]->(object)
SET projection = $properties
RETURN projection.assertion_id AS assertion_id`

	getAssertionCypher = `
MATCH (assertion:Assertion {team_id: $profileId, assertion_id: $assertionId})
OPTIONAL MATCH (value:Value {team_id: $profileId, value_id: assertion.object_value_id})
RETURN assertion.assertion_id AS assertion_id,
       assertion.owner_profile_id AS owner_profile_id,
       assertion.subject_entity_id AS subject_entity_id,
       assertion.predicate_key AS predicate_key,
       assertion.relationship_type AS relationship_type,
       assertion.object_entity_id AS object_entity_id,
       assertion.object_value_id AS object_value_id,
       assertion.tier AS tier,
       assertion.status AS status,
       assertion.policy_family AS policy_family,
       assertion.polarity AS polarity,
       assertion.modality AS modality,
       assertion.valid_from AS valid_from,
       assertion.valid_to AS valid_to,
       assertion.recorded_at AS recorded_at,
       assertion.recorded_to AS recorded_to,
       assertion.extract_conf AS extract_conf,
       assertion.resolution_conf AS resolution_conf,
       assertion.source_quality AS source_quality,
       assertion.support_count AS support_count,
       assertion.source_group_count AS source_group_count,
       assertion.evidence_json AS evidence_json,
       assertion.embedding_model AS embedding_model,
       assertion.extraction_model AS extraction_model,
       assertion.extraction_version AS extraction_version,
       assertion.verifier_model AS verifier_model,
       assertion.pipeline_run_id AS pipeline_run_id,
       assertion.projection_version AS projection_version,
       assertion.created_at AS created_at,
       assertion.updated_at AS updated_at,
       value.value_type AS value_type,
       value.value AS value,
       value.display AS value_display,
       value.unit AS value_unit`

	updateAssertionStateCypher = `
MATCH (assertion:Assertion {team_id: $profileId, assertion_id: $assertionId})
SET assertion.tier = $tier,
    assertion.status = $status,
    assertion.updated_at = $updatedAt
RETURN assertion.assertion_id AS assertion_id`

	linkLegacyDecompositionCypher = `
UNWIND $legacyRefs AS legacyRef
MATCH (legacy {team_id: $profileId})
WHERE (legacyRef.type = 'fragment' AND legacy:SourceFragment AND legacy.fragment_id = legacyRef.id)
   OR (legacyRef.type = 'claim' AND legacy:Claim AND legacy.claim_id = legacyRef.id)
   OR (legacyRef.type = 'fact' AND legacy:Fact AND legacy.fact_id = legacyRef.id)
   OR (legacyRef.type = 'dream' AND legacy:Dream AND legacy.dream_id = legacyRef.id)
UNWIND $assertionIds AS assertionId
MATCH (assertion:Assertion {team_id: $profileId, assertion_id: assertionId})
MERGE (legacy)-[decomposed:DECOMPOSED_INTO {
  team_id: $profileId,
  assertion_id: assertionId,
  legacy_type: legacyRef.type,
  legacy_id: legacyRef.id
}]->(assertion)
SET decomposed.recorded_at = $recordedAt
RETURN count(decomposed) AS linked`

	missingLegacyRefsCypher = `
UNWIND $legacyRefs AS legacyRef
OPTIONAL MATCH (legacy {team_id: $profileId})
WHERE (legacyRef.type = 'fragment' AND legacy:SourceFragment AND legacy.fragment_id = legacyRef.id)
   OR (legacyRef.type = 'claim' AND legacy:Claim AND legacy.claim_id = legacyRef.id)
   OR (legacyRef.type = 'fact' AND legacy:Fact AND legacy.fact_id = legacyRef.id)
   OR (legacyRef.type = 'dream' AND legacy:Dream AND legacy.dream_id = legacyRef.id)
WITH legacyRef, count(legacy) AS matches
WHERE matches = 0
RETURN legacyRef.type AS type, legacyRef.id AS id
ORDER BY type ASC, id ASC`

	supersedeCurrentCypher = `
MATCH (current:Assertion {team_id: $profileId, assertion_id: $assertionId})
MATCH (previous:Assertion {
  team_id: $profileId,
  subject_entity_id: $subjectEntityId,
  predicate_key: $predicateKey,
  status: 'active'
})
WHERE previous.assertion_id <> $assertionId
  AND $shouldSupersede
  AND ($tier = 'fact' OR coalesce(previous.tier, 'candidate') <> 'fact')
  AND (
    $policyFamily = 'single_state' OR
    $validFrom IS NULL OR
    previous.valid_from IS NULL OR
    previous.valid_from <= $validFrom
  )
WITH current, previous,
     previous.tier AS previous_tier,
     previous.status AS previous_status
SET previous.status = 'superseded',
    previous.recorded_to = $recordedAt,
    previous.updated_at = $updatedAt
MERGE (previous)-[superseded:SUPERSEDED_BY {team_id: $profileId}]->(current)
SET superseded.recorded_at = $recordedAt
WITH previous, previous_tier, previous_status
OPTIONAL MATCH ()-[projection]->()
WHERE projection.team_id = $profileId
  AND projection.assertion_id = previous.assertion_id
  AND projection.semantic_projection = true
SET projection.status = 'superseded',
    projection.recorded_to = $recordedAt
RETURN previous.assertion_id AS assertion_id,
       previous_tier AS tier,
       previous_status AS status`
)

type AssertionStore struct {
	scope ProfileScopeEnforcer
}

var _ assertionservice.Store = (*AssertionStore)(nil)

func NewAssertionStore(scope ProfileScopeEnforcer) *AssertionStore {
	return &AssertionStore{scope: scope}
}

func (s *AssertionStore) GetAssertion(ctx context.Context, profileID, assertionID string) (*domain.Assertion, error) {
	if s == nil || s.scope == nil {
		return nil, fmt.Errorf("assertion store: profile scope is required")
	}
	_, rows, err := s.scope.ScopedRead(ctx, profileID, getAssertionCypher, map[string]any{"assertionId": assertionID})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return assertionFromRow(profileID, rows[0])
}

func (s *AssertionStore) UpdateAssertionState(ctx context.Context, profileID, assertionID string, tier domain.AssertionTier, status domain.AssertionStatus, at time.Time) (*domain.Assertion, assertionservice.WriteResult, error) {
	if s == nil || s.scope == nil {
		return nil, assertionservice.WriteResult{}, fmt.Errorf("assertion store: profile scope is required")
	}
	writeResult := assertionservice.WriteResult{Superseded: []assertionservice.SupersededAssertion{}}
	var updated *domain.Assertion
	err := s.scope.ScopedWriteTx(ctx, profileID, func(tx neo4jdriver.ManagedTransaction) error {
		var superseded []assertionservice.SupersededAssertion
		var err error
		updated, superseded, err = updateAssertionStateTx(ctx, tx, profileID, assertionID, tier, status, at)
		writeResult.Superseded = append(writeResult.Superseded, superseded...)
		return err
	})
	if err != nil {
		return nil, assertionservice.WriteResult{}, err
	}
	return updated, writeResult, nil
}

func (s *AssertionStore) UpdateAssertionStates(ctx context.Context, profileID string, updates []assertionservice.StateUpdate, at time.Time) ([]domain.Assertion, assertionservice.WriteResult, error) {
	if s == nil || s.scope == nil {
		return nil, assertionservice.WriteResult{}, fmt.Errorf("assertion store: profile scope is required")
	}
	updated := make([]domain.Assertion, 0, len(updates))
	writeResult := assertionservice.WriteResult{Superseded: []assertionservice.SupersededAssertion{}}
	err := s.scope.ScopedWriteTx(ctx, profileID, func(tx neo4jdriver.ManagedTransaction) error {
		for _, update := range updates {
			assertion, superseded, err := updateAssertionStateTx(ctx, tx, profileID, update.AssertionID, update.Tier, update.Status, at)
			if err != nil {
				return err
			}
			if assertion == nil {
				return fmt.Errorf("assertion store: assertion %q not found", update.AssertionID)
			}
			updated = append(updated, *assertion)
			writeResult.Superseded = append(writeResult.Superseded, superseded...)
		}
		return nil
	})
	if err != nil {
		return nil, assertionservice.WriteResult{}, err
	}
	return updated, writeResult, nil
}

func (s *AssertionStore) LinkLegacyDecomposition(ctx context.Context, profileID string, refs []domain.LegacyMemoryRef, assertionIDs []string, at time.Time) error {
	if s == nil || s.scope == nil {
		return fmt.Errorf("assertion store: profile scope is required")
	}
	return s.scope.ScopedWriteTx(ctx, profileID, func(tx neo4jdriver.ManagedTransaction) error {
		return linkLegacyDecompositionTx(ctx, tx, profileID, refs, assertionIDs, at)
	})
}

func (s *AssertionStore) MissingLegacyRefs(ctx context.Context, profileID string, refs []domain.LegacyMemoryRef) ([]string, error) {
	if s == nil || s.scope == nil {
		return nil, fmt.Errorf("assertion store: profile scope is required")
	}
	legacyRefs := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		legacyRefs = append(legacyRefs, map[string]any{"type": strings.ToLower(strings.TrimSpace(ref.Type)), "id": strings.TrimSpace(ref.ID)})
	}
	_, rows, err := s.scope.ScopedRead(ctx, profileID, missingLegacyRefsCypher, map[string]any{"legacyRefs": legacyRefs})
	if err != nil {
		return nil, err
	}
	missing := make([]string, 0, len(rows))
	for _, row := range rows {
		kind := graphrow.String(row, "type")
		id := graphrow.String(row, "id")
		if kind != "" && id != "" {
			missing = append(missing, kind+":"+id)
		}
	}
	return missing, nil
}

func (s *AssertionStore) FinalizeLegacyMigration(ctx context.Context, profileID string, updates []assertionservice.StateUpdate, refs []domain.LegacyMemoryRef, at time.Time) ([]domain.Assertion, assertionservice.WriteResult, error) {
	if s == nil || s.scope == nil {
		return nil, assertionservice.WriteResult{}, fmt.Errorf("assertion store: profile scope is required")
	}
	updated := make([]domain.Assertion, 0, len(updates))
	writeResult := assertionservice.WriteResult{Superseded: []assertionservice.SupersededAssertion{}}
	assertionIDs := make([]string, 0, len(updates))
	err := s.scope.ScopedWriteTx(ctx, profileID, func(tx neo4jdriver.ManagedTransaction) error {
		for _, update := range updates {
			assertion, superseded, err := updateAssertionStateTx(ctx, tx, profileID, update.AssertionID, update.Tier, update.Status, at)
			if err != nil {
				return err
			}
			if assertion == nil {
				return fmt.Errorf("assertion store: assertion %q not found", update.AssertionID)
			}
			updated = append(updated, *assertion)
			assertionIDs = append(assertionIDs, assertion.AssertionID)
			writeResult.Superseded = append(writeResult.Superseded, superseded...)
		}
		return linkLegacyDecompositionTx(ctx, tx, profileID, refs, assertionIDs, at)
	})
	if err != nil {
		return nil, assertionservice.WriteResult{}, err
	}
	return updated, writeResult, nil
}

func linkLegacyDecompositionTx(ctx context.Context, tx neo4jdriver.ManagedTransaction, profileID string, refs []domain.LegacyMemoryRef, assertionIDs []string, at time.Time) error {
	legacyRefs := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		legacyRefs = append(legacyRefs, map[string]any{"type": strings.ToLower(strings.TrimSpace(ref.Type)), "id": strings.TrimSpace(ref.ID)})
	}
	result, err := RunScoped(ctx, tx, profileID, linkLegacyDecompositionCypher, map[string]any{
		"legacyRefs":   legacyRefs,
		"assertionIds": assertionIDs,
		"recordedAt":   at,
	})
	if err != nil {
		return err
	}
	records, err := result.Collect(ctx)
	if err != nil {
		return err
	}
	linked := 0
	if len(records) == 1 {
		linked = graphrow.Int(records[0].AsMap(), "linked")
	}
	expected := len(refs) * len(assertionIDs)
	if linked != expected {
		return fmt.Errorf("assertion store: linked %d of %d legacy decompositions", linked, expected)
	}
	return nil
}

func updateAssertionStateTx(ctx context.Context, tx neo4jdriver.ManagedTransaction, profileID, assertionID string, tier domain.AssertionTier, status domain.AssertionStatus, at time.Time) (*domain.Assertion, []assertionservice.SupersededAssertion, error) {
	result, err := RunScoped(ctx, tx, profileID, getAssertionCypher, map[string]any{"assertionId": assertionID})
	if err != nil {
		return nil, nil, err
	}
	records, err := result.Collect(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(records) == 0 {
		return nil, nil, nil
	}
	assertion, err := assertionFromRow(profileID, records[0].AsMap())
	if err != nil {
		return nil, nil, err
	}
	assertion.Tier = tier
	assertion.Status = status
	assertion.UpdatedAt = at
	if err := runAssertionWrite(ctx, tx, profileID, updateAssertionStateCypher, map[string]any{
		"assertionId": assertionID,
		"tier":        string(tier),
		"status":      string(status),
		"updatedAt":   at,
	}); err != nil {
		return nil, nil, err
	}
	if err := runAssertionWrite(ctx, tx, profileID, deleteProjectionCypher, map[string]any{"assertionId": assertionID}); err != nil {
		return nil, nil, err
	}
	if status != domain.AssertionStatusActive {
		return assertion, nil, nil
	}
	if err := ensurePredicate(ctx, tx, profileID, *assertion, at); err != nil {
		return nil, nil, err
	}
	if err := runAssertionWrite(ctx, tx, profileID, mergeMentionsCypher, map[string]any{
		"assertionId": assertion.AssertionID,
		"evidence":    assertionEvidenceParams(assertion.Evidence),
		"entityIds":   assertionEntityIDs(*assertion),
	}); err != nil {
		return nil, nil, err
	}
	objectGraphKey := "entity:" + assertion.ObjectEntityID
	if assertion.ObjectValue != nil {
		objectGraphKey = "value:" + assertion.ObjectValue.ValueID
	}
	if err := runAssertionWrite(ctx, tx, profileID, createProjectionCypher, map[string]any{
		"assertionId":      assertion.AssertionID,
		"subjectEntityId":  assertion.SubjectEntityID,
		"objectGraphKey":   objectGraphKey,
		"relationshipType": assertion.RelationshipType,
		"properties":       assertionProjectionProperties(profileID, *assertion),
	}); err != nil {
		return nil, nil, err
	}
	superseded, err := supersedeCurrent(ctx, tx, profileID, *assertion, at)
	if err != nil {
		return nil, nil, err
	}
	return assertion, superseded, nil
}

func (s *AssertionStore) WriteBundle(
	ctx context.Context,
	profileID string,
	bundle assertionservice.Bundle,
) (assertionservice.WriteResult, error) {
	if s == nil || s.scope == nil {
		return assertionservice.WriteResult{}, fmt.Errorf("assertion store: profile scope is required")
	}
	if err := assertionservice.ValidateBundle(profileID, bundle); err != nil {
		return assertionservice.WriteResult{}, err
	}
	result := assertionservice.WriteResult{Superseded: []assertionservice.SupersededAssertion{}}
	entityNames := make(map[string]string, len(bundle.Entities))
	for _, entity := range bundle.Entities {
		entityNames[entity.EntityID] = entity.CanonicalName
	}
	err := s.scope.ScopedWriteTx(ctx, profileID, func(tx neo4jdriver.ManagedTransaction) error {
		for _, entity := range bundle.Entities {
			if err := writeEntity(ctx, tx, profileID, entity); err != nil {
				return err
			}
		}
		for _, assertion := range bundle.Assertions {
			superseded, err := writeAssertion(ctx, tx, profileID, assertion, entityNames)
			if err != nil {
				return err
			}
			result.Superseded = append(result.Superseded, superseded...)
		}
		return nil
	})
	if err != nil {
		return assertionservice.WriteResult{}, err
	}
	return result, nil
}

func writeEntity(
	ctx context.Context,
	tx neo4jdriver.ManagedTransaction,
	profileID string,
	entity domain.Entity,
) error {
	firstSeenAt := entity.FirstSeenAt
	if firstSeenAt.IsZero() {
		firstSeenAt = time.Now().UTC()
	}
	lastSeenAt := entity.LastSeenAt
	if lastSeenAt.IsZero() {
		lastSeenAt = firstSeenAt
	}
	return runAssertionWrite(ctx, tx, profileID, mergeEntityCypher, map[string]any{
		"entityId":         entity.EntityID,
		"graphKey":         "entity:" + entity.EntityID,
		"canonicalName":    entity.CanonicalName,
		"normalizedName":   entity.NormalizedName,
		"entityType":       entity.EntityType,
		"aliases":          entity.Aliases,
		"resolutionStatus": string(entity.ResolutionStatus),
		"resolutionConf":   entity.ResolutionConf,
		"embedding":        entity.Embedding,
		"embeddingModel":   entity.EmbeddingModel,
		"firstSeenAt":      firstSeenAt,
		"lastSeenAt":       lastSeenAt,
	})
}

func writeAssertion(
	ctx context.Context,
	tx neo4jdriver.ManagedTransaction,
	profileID string,
	assertion domain.Assertion,
	entityNames map[string]string,
) ([]assertionservice.SupersededAssertion, error) {
	if assertion.ObjectValue != nil {
		if err := writeValue(ctx, tx, profileID, *assertion.ObjectValue); err != nil {
			return nil, err
		}
	}
	evidenceJSON, err := json.Marshal(assertion.Evidence)
	if err != nil {
		return nil, fmt.Errorf("assertion store: marshal evidence: %w", err)
	}
	objectEntityID := assertion.ObjectEntityID
	objectValueID := ""
	objectGraphKey := "entity:" + objectEntityID
	if assertion.ObjectValue != nil {
		objectValueID = assertion.ObjectValue.ValueID
		objectGraphKey = "value:" + objectValueID
	}
	createdAt := assertion.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := assertion.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	recordedAt := assertion.RecordedAt
	if recordedAt.IsZero() {
		recordedAt = createdAt
	}
	assertion.RecordedAt = recordedAt
	assertion.UpdatedAt = updatedAt
	params := map[string]any{
		"assertionId":       assertion.AssertionID,
		"ownerProfileId":    assertion.OwnerProfileID,
		"subjectEntityId":   assertion.SubjectEntityID,
		"predicateKey":      assertion.PredicateKey,
		"relationshipType":  assertion.RelationshipType,
		"objectEntityId":    objectEntityID,
		"objectValueId":     objectValueID,
		"searchText":        assertionSearchText(assertion, entityNames),
		"tier":              string(assertion.Tier),
		"status":            string(assertion.Status),
		"policyFamily":      string(assertion.PolicyFamily),
		"polarity":          string(assertion.Polarity),
		"modality":          string(assertion.Modality),
		"validFrom":         optionalTimeParameter(assertion.ValidFrom),
		"validTo":           optionalTimeParameter(assertion.ValidTo),
		"recordedAt":        recordedAt,
		"recordedTo":        optionalTimeParameter(assertion.RecordedTo),
		"extractConf":       assertion.ExtractConf,
		"resolutionConf":    assertion.ResolutionConf,
		"sourceQuality":     assertion.SourceQuality,
		"supportCount":      assertion.SupportCount,
		"sourceGroupCount":  assertion.SourceGroupCount,
		"evidenceJson":      string(evidenceJSON),
		"embedding":         assertion.Embedding,
		"embeddingModel":    assertion.EmbeddingModel,
		"extractionModel":   assertion.ExtractionModel,
		"extractionVersion": assertion.ExtractionVersion,
		"verifierModel":     assertion.VerifierModel,
		"pipelineRunId":     assertion.PipelineRunID,
		"projectionVersion": assertion.ProjectionVersion,
		"createdAt":         createdAt,
		"updatedAt":         updatedAt,
	}
	if err := runAssertionWrite(ctx, tx, profileID, mergeAssertionCypher, params); err != nil {
		return nil, err
	}
	evidence := assertionEvidenceParams(assertion.Evidence)
	if err := runAssertionWrite(ctx, tx, profileID, mergeSupportCypher, map[string]any{
		"assertionId": assertion.AssertionID,
		"evidence":    evidence,
	}); err != nil {
		return nil, err
	}
	entityIDs := []string{assertion.SubjectEntityID}
	if assertion.ObjectEntityID != "" {
		entityIDs = append(entityIDs, assertion.ObjectEntityID)
	}
	if err := runAssertionWrite(ctx, tx, profileID, deleteProjectionCypher, map[string]any{
		"assertionId": assertion.AssertionID,
	}); err != nil {
		return nil, err
	}
	if assertion.Status != domain.AssertionStatusActive {
		return nil, nil
	}
	if err := ensurePredicate(ctx, tx, profileID, assertion, updatedAt); err != nil {
		return nil, err
	}
	if err := runAssertionWrite(ctx, tx, profileID, mergeMentionsCypher, map[string]any{
		"assertionId": assertion.AssertionID,
		"evidence":    evidence,
		"entityIds":   entityIDs,
	}); err != nil {
		return nil, err
	}
	if err := runAssertionWrite(ctx, tx, profileID, createProjectionCypher, map[string]any{
		"assertionId":      assertion.AssertionID,
		"subjectEntityId":  assertion.SubjectEntityID,
		"objectGraphKey":   objectGraphKey,
		"relationshipType": assertion.RelationshipType,
		"properties":       assertionProjectionProperties(profileID, assertion),
	}); err != nil {
		return nil, err
	}
	return supersedeCurrent(ctx, tx, profileID, assertion, updatedAt)
}

func ensurePredicate(ctx context.Context, tx neo4jdriver.ManagedTransaction, profileID string, assertion domain.Assertion, updatedAt time.Time) error {
	result, err := RunScoped(ctx, tx, profileID, mergePredicateCypher, map[string]any{
		"registryId":       profileID + ":" + assertion.PredicateKey,
		"predicateKey":     assertion.PredicateKey,
		"relationshipType": assertion.RelationshipType,
		"policyFamily":     string(assertion.PolicyFamily),
		"updatedAt":        updatedAt,
	})
	if err != nil {
		return err
	}
	records, err := result.Collect(ctx)
	if err != nil {
		return err
	}
	if len(records) != 1 {
		return fmt.Errorf("assertion store: predicate %q conflicts with its registered relationship type or policy family", assertion.PredicateKey)
	}
	return nil
}

func supersedeCurrent(ctx context.Context, tx neo4jdriver.ManagedTransaction, profileID string, assertion domain.Assertion, updatedAt time.Time) ([]assertionservice.SupersededAssertion, error) {
	shouldSupersede := assertion.PolicyFamily == domain.AssertionPolicySingleState || assertion.PolicyFamily == domain.AssertionPolicyVersioned
	result, err := RunScoped(ctx, tx, profileID, supersedeCurrentCypher, map[string]any{
		"assertionId":     assertion.AssertionID,
		"subjectEntityId": assertion.SubjectEntityID,
		"predicateKey":    assertion.PredicateKey,
		"policyFamily":    string(assertion.PolicyFamily),
		"tier":            string(assertion.Tier),
		"shouldSupersede": shouldSupersede,
		"validFrom":       optionalTimeParameter(assertion.ValidFrom),
		"recordedAt":      assertion.RecordedAt,
		"updatedAt":       updatedAt,
	})
	if err != nil {
		return nil, err
	}
	records, err := result.Collect(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]assertionservice.SupersededAssertion, 0, len(records))
	for _, record := range records {
		row := record.AsMap()
		assertionID, _ := row["assertion_id"].(string)
		tier, _ := row["tier"].(string)
		status, _ := row["status"].(string)
		if assertionID != "" {
			out = append(out, assertionservice.SupersededAssertion{AssertionID: assertionID, Tier: domain.AssertionTier(tier), Status: domain.AssertionStatus(status)})
		}
	}
	return out, nil
}

func writeValue(
	ctx context.Context,
	tx neo4jdriver.ManagedTransaction,
	profileID string,
	value domain.TypedValue,
) error {
	return runAssertionWrite(ctx, tx, profileID, mergeValueCypher, map[string]any{
		"valueId":   value.ValueID,
		"graphKey":  "value:" + value.ValueID,
		"valueType": string(value.ValueType),
		"value":     value.Value,
		"display":   value.Display,
		"unit":      value.Unit,
	})
}

func runAssertionWrite(
	ctx context.Context,
	tx neo4jdriver.ManagedTransaction,
	profileID string,
	query string,
	params map[string]any,
) error {
	result, err := RunScoped(ctx, tx, profileID, query, params)
	if err != nil {
		return err
	}
	_, err = result.Consume(ctx)
	return err
}

func assertionEvidenceParams(values []domain.EvidenceSpan) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		out = append(out, map[string]any{
			"fragment_id":  value.FragmentID,
			"span_start":   value.Start,
			"span_end":     value.End,
			"source_group": value.SourceGroup,
		})
	}
	return out
}

func assertionEntityIDs(assertion domain.Assertion) []string {
	ids := []string{assertion.SubjectEntityID}
	if assertion.ObjectEntityID != "" {
		ids = append(ids, assertion.ObjectEntityID)
	}
	return ids
}

func assertionSearchText(assertion domain.Assertion, entityNames map[string]string) string {
	object := entityNames[assertion.ObjectEntityID]
	if assertion.ObjectValue != nil {
		object = assertion.ObjectValue.Value
	}
	return strings.Join([]string{entityNames[assertion.SubjectEntityID], assertion.PredicateKey, object}, " ")
}

func assertionProjectionProperties(profileID string, assertion domain.Assertion) map[string]any {
	properties := map[string]any{
		"assertion_id":        assertion.AssertionID,
		"team_id":             profileID,
		"owner_profile_id":    assertion.OwnerProfileID,
		"predicate_key":       assertion.PredicateKey,
		"semantic_projection": true,
		"tier":                string(assertion.Tier),
		"status":              string(assertion.Status),
		"policy_family":       string(assertion.PolicyFamily),
		"polarity":            string(assertion.Polarity),
		"modality":            string(assertion.Modality),
		"support_count":       assertion.SupportCount,
		"source_group_count":  assertion.SourceGroupCount,
		"source_quality":      assertion.SourceQuality,
		"projection_version":  assertion.ProjectionVersion,
	}
	setOptionalTime(properties, "valid_from", assertion.ValidFrom)
	setOptionalTime(properties, "valid_to", assertion.ValidTo)
	setOptionalTime(properties, "recorded_to", assertion.RecordedTo)
	if !assertion.RecordedAt.IsZero() {
		properties["recorded_at"] = assertion.RecordedAt
	}
	return properties
}

func setOptionalTime(properties map[string]any, key string, value *time.Time) {
	if value != nil && !value.IsZero() {
		properties[key] = value.UTC()
	}
}

func optionalTimeParameter(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC()
}

func assertionFromRow(profileID string, row map[string]any) (*domain.Assertion, error) {
	assertion := &domain.Assertion{
		AssertionID:       graphrow.String(row, "assertion_id"),
		ProfileID:         profileID,
		OwnerProfileID:    graphrow.String(row, "owner_profile_id"),
		SubjectEntityID:   graphrow.String(row, "subject_entity_id"),
		PredicateKey:      graphrow.String(row, "predicate_key"),
		RelationshipType:  graphrow.String(row, "relationship_type"),
		ObjectEntityID:    graphrow.String(row, "object_entity_id"),
		Tier:              domain.AssertionTier(graphrow.String(row, "tier")),
		Status:            domain.AssertionStatus(graphrow.String(row, "status")),
		PolicyFamily:      domain.AssertionPolicyFamily(graphrow.String(row, "policy_family")),
		Polarity:          domain.ClaimPolarity(graphrow.String(row, "polarity")),
		Modality:          domain.ClaimModality(graphrow.String(row, "modality")),
		ValidFrom:         graphrow.TimePtr(row, "valid_from"),
		ValidTo:           graphrow.TimePtr(row, "valid_to"),
		RecordedTo:        graphrow.TimePtr(row, "recorded_to"),
		ExtractConf:       graphrow.Float64(row, "extract_conf"),
		ResolutionConf:    graphrow.Float64(row, "resolution_conf"),
		SourceQuality:     graphrow.Float64(row, "source_quality"),
		SupportCount:      int(graphrow.Float64(row, "support_count")),
		SourceGroupCount:  int(graphrow.Float64(row, "source_group_count")),
		EmbeddingModel:    graphrow.String(row, "embedding_model"),
		ExtractionModel:   graphrow.String(row, "extraction_model"),
		ExtractionVersion: graphrow.String(row, "extraction_version"),
		VerifierModel:     graphrow.String(row, "verifier_model"),
		PipelineRunID:     graphrow.String(row, "pipeline_run_id"),
		ProjectionVersion: graphrow.String(row, "projection_version"),
	}
	if value := graphrow.TimePtr(row, "recorded_at"); value != nil {
		assertion.RecordedAt = *value
	}
	if value := graphrow.TimePtr(row, "created_at"); value != nil {
		assertion.CreatedAt = *value
	}
	if value := graphrow.TimePtr(row, "updated_at"); value != nil {
		assertion.UpdatedAt = *value
	}
	evidenceRaw := graphrow.String(row, "evidence_json")
	if evidenceRaw != "" {
		if err := json.Unmarshal([]byte(evidenceRaw), &assertion.Evidence); err != nil {
			return nil, fmt.Errorf("assertion store: invalid evidence JSON: %w", err)
		}
	}
	if valueID := graphrow.String(row, "object_value_id"); valueID != "" {
		assertion.ObjectValue = &domain.TypedValue{
			ValueID:   valueID,
			ValueType: domain.ValueType(graphrow.String(row, "value_type")),
			Value:     graphrow.String(row, "value"),
			Display:   graphrow.String(row, "value_display"),
			Unit:      graphrow.String(row, "value_unit"),
		}
	}
	return assertion, nil
}
