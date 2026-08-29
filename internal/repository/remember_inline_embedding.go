package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const synchronousRememberEmbeddingDocumentLimit = 256

var ErrSynchronousRememberEmbeddingFence = errors.New("synchronous Remember embedding fence failed")
var ErrSynchronousRememberEmbeddingInputBudget = errors.New("synchronous Remember embedding input budget exceeded")

type synchronousRememberEmbeddingStageError struct {
	stage string
	err   error
}

func (e *synchronousRememberEmbeddingStageError) Error() string {
	return "synchronous Remember inline embedding " + e.stage + " failed"
}

func (e *synchronousRememberEmbeddingStageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *synchronousRememberEmbeddingStageError) SynchronousRememberEmbeddingStage() string {
	if e == nil {
		return ""
	}
	return e.stage
}

func wrapSynchronousRememberEmbeddingStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &synchronousRememberEmbeddingStageError{stage: stage, err: err}
}

// SynchronousRememberEmbeddingPlan is repository data, not an embedding
// provider contract. The application service converts it to semanticwrite.Plan
// and executes its Executor before calling CommitSynchronousRemember.
type SynchronousRememberEmbeddingPlan struct {
	EmbeddingContractID     string
	EmbeddingDimensions     int
	EmbeddingModel          string
	SearchGenerationID      string
	SearchGenerationVersion int64
	Documents               []SynchronousRememberEmbeddingDocument
}

type SynchronousRememberEmbeddingDocument struct {
	Hash string
	Text string
}

// SynchronousRememberEmbeddingResult preserves the provider output's exact
// fence and hash association. Vectors are applied only after final documents
// are rendered within the authoritative transaction.
type SynchronousRememberEmbeddingResult struct {
	EmbeddingContractID     string
	EmbeddingDimensions     int
	EmbeddingModel          string
	SearchGenerationID      string
	SearchGenerationVersion int64
	Embeddings              []SynchronousRememberEmbedding
}

type SynchronousRememberEmbedding struct {
	DocumentHash string
	Vector       []float32
}

type synchronousInlineEmbeddingContextKey struct{}

func withSynchronousInlineEmbedding(ctx context.Context, result *SynchronousRememberEmbeddingResult) context.Context {
	return context.WithValue(ctx, synchronousInlineEmbeddingContextKey{}, result)
}

func synchronousInlineEmbeddingEnabled(ctx context.Context) bool {
	result, _ := ctx.Value(synchronousInlineEmbeddingContextKey{}).(*SynchronousRememberEmbeddingResult)
	return result != nil
}

// PlanSynchronousRememberEmbeddings renders the candidate search documents
// without writing canonical state. Entity and typed Value records deliberately
// remain relationship projection inputs; they are not standalone documents.
func (r *LedgerRepositoryImpl) PlanSynchronousRememberEmbeddings(
	ctx context.Context,
	create CreateIngestInput,
	commit CommitSubmissionAssessmentInput,
) (*SynchronousRememberEmbeddingPlan, error) {
	create = normalizeCreateIngestInput(create)
	commit = normalizeCommitSubmissionAssessmentInput(commit)
	if err := validateCreateIngestInput(create); err != nil {
		return nil, fmt.Errorf("synchronous Remember embedding plan ingest: %w", err)
	}
	if create.TeamID != commit.TeamID || create.OwnerProfileID != commit.OwnerProfileID {
		return nil, fmt.Errorf("%w: plan actor does not match commit actor", ErrSynchronousRememberEmbeddingFence)
	}
	plan := &SynchronousRememberEmbeddingPlan{Documents: []SynchronousRememberEmbeddingDocument{}}
	err := r.withTeamProfileTx(ctx, create.TeamID, create.OwnerProfileID, func(tx *gorm.DB) error {
		contract, err := loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		plan.EmbeddingContractID = contract.EmbeddingContractID
		plan.EmbeddingDimensions = contract.EmbeddingDimensions
		plan.EmbeddingModel = contract.EmbeddingModel
		plan.SearchGenerationID = contract.SearchIndexGenerationID
		plan.SearchGenerationVersion = int64(contract.IndexGeneration)

		seen := make(map[string]struct{}, len(create.Evidence)+len(commit.RelationshipObservations))
		add := func(text string) error {
			text = strings.TrimSpace(text)
			if text == "" {
				return fmt.Errorf("%w: empty rendered document", ErrSynchronousRememberEmbeddingFence)
			}
			hash := searchDocumentTextHash(text)
			if _, ok := seen[hash]; ok {
				return nil
			}
			if len(plan.Documents) >= synchronousRememberEmbeddingDocumentLimit {
				return fmt.Errorf("%w: %w: document limit %d exceeded", ErrSynchronousRememberEmbeddingFence, ErrSynchronousRememberEmbeddingInputBudget, synchronousRememberEmbeddingDocumentLimit)
			}
			seen[hash] = struct{}{}
			plan.Documents = append(plan.Documents, SynchronousRememberEmbeddingDocument{Hash: hash, Text: text})
			return nil
		}
		for _, evidence := range create.Evidence {
			if err := add(evidence.Content); err != nil {
				return err
			}
		}
		names, err := synchronousRememberEmbeddingEntityNames(ctx, tx, create.TeamID, commit.EntityResolutions)
		if err != nil {
			return err
		}
		registrations := make(map[string]SubmissionPredicateRegistrationInput, len(commit.PredicateRegistrations))
		for _, registration := range commit.PredicateRegistrations {
			registrations[registration.RelationshipRef] = registration
		}
		entityTexts, err := synchronousRememberEmbeddingEntityTexts(ctx, tx, create.TeamID, contract, commit.EntityResolutions)
		if err != nil {
			return err
		}
		for _, text := range entityTexts {
			if err := add(text); err != nil {
				return err
			}
		}
		for _, item := range commit.RelationshipObservations {
			observation := item.Observation
			if !observation.PromoteToFact {
				continue
			}
			predicate := observation.PredicateKey
			if registration, ok := registrations[item.RelationshipRef]; ok {
				if predicate != "" {
					return fmt.Errorf("%w: registered predicate %q also supplied by assessment", ErrSynchronousRememberEmbeddingFence, item.RelationshipRef)
				}
				predicate, err = previewSynchronousRememberPredicateKey(ctx, tx, create.TeamID, registration)
				if err != nil {
					return err
				}
			}
			if strings.TrimSpace(predicate) == "" {
				return fmt.Errorf("%w: relationship %q has no predicate", ErrSynchronousRememberEmbeddingFence, item.RelationshipRef)
			}
			subject := firstNonEmpty(names[observation.SubjectRef], observation.SubjectRef)
			object := firstNonEmpty(names[observation.ObjectRef], observation.ObjectRef)
			valueType, value, unit := "", "", ""
			if observation.ObjectValue != nil {
				projected, err := synchronousRememberEmbeddingValue(ctx, tx, create.TeamID, *observation.ObjectValue)
				if err != nil {
					return err
				}
				valueType, value, unit = projected.ValueType, firstNonEmpty(projected.Display, projected.CanonicalValue), projected.Unit
				object = value
			}
			if err := add(relationshipProjectionText(&RelationshipRecord{
				SubjectEntityID: observation.SubjectRef, PredicateKey: predicate, ObjectEntityID: observation.ObjectRef,
				Polarity: observation.Polarity, ScopeKey: observation.ScopeKey, ValidFrom: observation.ValidFrom, ValidTo: observation.ValidTo,
			}, relationshipProjectionNames{SubjectName: subject, ObjectName: object, ObjectValueType: valueType, ObjectValue: value, ObjectUnit: unit})); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("repository: plan synchronous Remember embeddings: %w", err)
	}
	return plan, nil
}

func synchronousRememberEmbeddingEntityTexts(ctx context.Context, tx *gorm.DB, teamID string, contract *ActiveSearchContract, resolutions []SubmissionAssessmentEntityResolutionInput) ([]string, error) {
	texts := make([]string, 0, len(resolutions))
	for _, entry := range resolutions {
		resolution := entry.Resolution
		name, kind := strings.TrimSpace(resolution.CanonicalName), strings.TrimSpace(resolution.EntityKind)
		if resolution.EntityID != "" {
			var canonical, persistedKind, spaceID string
			var spaceGeneration int64
			err := tx.WithContext(ctx).Raw(`SELECT COALESCE(canonical.display_name, ''), entity.entity_kind, entity.space_id::text, COALESCE(entity.space_generation, 0) FROM entity_records AS entity LEFT JOIN entity_names AS canonical ON canonical.team_id = entity.team_id AND canonical.entity_id = entity.entity_id AND canonical.name_kind = 'canonical' AND canonical.valid_to IS NULL WHERE entity.team_id = ?::uuid AND entity.entity_id = ?::uuid AND entity.status = 'active' ORDER BY canonical.created_at DESC NULLS LAST, canonical.entity_name_id DESC NULLS LAST LIMIT 1`, teamID, resolution.EntityID).Row().Scan(&canonical, &persistedKind, &spaceID, &spaceGeneration)
			if err != nil {
				return nil, err
			}
			name, kind = firstNonEmpty(canonical, name, resolution.EntityID), persistedKind
			if name == "" || kind == "" {
				return nil, fmt.Errorf("%w: entity projection is incomplete", ErrSynchronousRememberEmbeddingFence)
			}
			text := entitySearchProjectionText(name, kind)
			current, err := synchronousRememberEntitySearchDocumentCurrent(ctx, tx, teamID, resolution.EntityID, contract, text, spaceID, spaceGeneration)
			if err != nil {
				return nil, err
			}
			if current {
				continue
			}
			texts = append(texts, text)
			continue
		}
		if name == "" || kind == "" {
			return nil, fmt.Errorf("%w: entity projection is incomplete", ErrSynchronousRememberEmbeddingFence)
		}
		texts = append(texts, entitySearchProjectionText(name, kind))
	}
	sort.Strings(texts)
	return texts, nil
}

func synchronousRememberEntitySearchDocumentCurrent(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	entityID string,
	contract *ActiveSearchContract,
	text string,
	spaceID string,
	spaceGeneration int64,
) (bool, error) {
	if contract == nil {
		return false, errors.New("active search contract is required")
	}
	var current bool
	err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'entity'
			  AND source_id = ?::uuid
			  AND embedding_contract_id = ?::uuid
			  AND embedding_dimensions = ?
			  AND space_id = ?::uuid
			  AND space_generation = ?
			  AND projection_format_version = 1
			  AND projection_generation_id IS NULL
			  AND document_hash = ?
			  AND search_state = 'current'
			  AND embedding IS NOT NULL
		)
	`, teamID, entityID, contract.EmbeddingContractID, contract.EmbeddingDimensions,
		spaceID, spaceGeneration, searchDocumentTextHash(text)).Row().Scan(&current)
	return current, err
}

func entitySearchProjectionText(name, kind string) string {
	return "entity\nname: " + strings.TrimSpace(name) + "\nkind: " + strings.TrimSpace(kind)
}

func upsertSynchronousRememberEntitySearchDocument(ctx context.Context, tx *gorm.DB, teamID, ownerID, entityID string, contract *ActiveSearchContract) (*SearchDocumentResult, error) {
	var result *SearchDocumentResult
	err := withSystemModeInTx(ctx, tx, teamID, ownerID, func(systemTx *gorm.DB) error {
		var name, kind, spaceID string
		var authoritativeOwnerID string
		var version int64
		var spaceGeneration int64
		if err := systemTx.WithContext(ctx).Raw(`SELECT COALESCE(canonical.display_name, entity.entity_id::text), entity.entity_kind, entity.version, entity.space_id::text, COALESCE(entity.space_generation, 0), COALESCE(canonical.owner_profile_id::text, '') FROM entity_records AS entity LEFT JOIN entity_names AS canonical ON canonical.team_id = entity.team_id AND canonical.entity_id = entity.entity_id AND canonical.name_kind = 'canonical' AND canonical.valid_to IS NULL WHERE entity.team_id = ?::uuid AND entity.entity_id = ?::uuid AND entity.status = 'active' ORDER BY canonical.created_at DESC NULLS LAST, canonical.entity_name_id DESC NULLS LAST LIMIT 1`, teamID, entityID).Row().Scan(&name, &kind, &version, &spaceID, &spaceGeneration, &authoritativeOwnerID); err != nil {
			return err
		}
		if strings.TrimSpace(authoritativeOwnerID) == "" {
			authoritativeOwnerID = ownerID
		}
		input := normalizeUpsertSearchDocumentInput(UpsertSearchDocumentInput{TeamID: teamID, OwnerProfileID: authoritativeOwnerID, SourceKind: "entity", SourceID: entityID, SourceVersion: version, ProjectionFormat: 1, DocumentText: entitySearchProjectionText(name, kind), Metadata: map[string]any{}, SpaceID: spaceID, SpaceGeneration: spaceGeneration})
		if err := validateUpsertSearchDocumentInput(input); err != nil {
			return err
		}
		var err error
		result, err = upsertSearchDocumentInTx(ctx, systemTx, input, contract, 0)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func previewSynchronousRememberPredicateKey(ctx context.Context, tx *gorm.DB, teamID string, registration SubmissionPredicateRegistrationInput) (string, error) {
	requested := strings.TrimSpace(registration.PredicateKey)
	canonical := canonicalGeneratedPredicateKey(requested)
	loaded, err := loadLatestSubmissionPredicate(ctx, tx, teamID, requested, canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return canonical, nil
	}
	if err != nil {
		return "", err
	}
	if loaded == nil || strings.TrimSpace(loaded.PredicateKey) == "" {
		return "", fmt.Errorf("%w: registered predicate could not be previewed", ErrSynchronousRememberEmbeddingFence)
	}
	return loaded.PredicateKey, nil
}

func searchDocumentTextHash(text string) string {
	return strings.TrimPrefix(sha256Hex(strings.TrimSpace(text)), "sha256:")
}

func synchronousRememberEmbeddingEntityNames(ctx context.Context, tx *gorm.DB, teamID string, resolutions []SubmissionAssessmentEntityResolutionInput) (map[string]string, error) {
	names := make(map[string]string, len(resolutions))
	for _, entry := range resolutions {
		ref := strings.TrimSpace(entry.Resolution.MentionRef)
		if ref == "" {
			continue
		}
		name := strings.TrimSpace(entry.Resolution.CanonicalName)
		if entry.Resolution.EntityID != "" {
			var canonical string
			err := tx.WithContext(ctx).Raw(`SELECT display_name FROM entity_names WHERE team_id = ?::uuid AND entity_id = ?::uuid AND name_kind = 'canonical' AND valid_to IS NULL ORDER BY created_at DESC, entity_name_id DESC LIMIT 1`, teamID, entry.Resolution.EntityID).Row().Scan(&canonical)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if strings.TrimSpace(canonical) != "" {
				name = strings.TrimSpace(canonical)
			}
		}
		if previous, ok := names[ref]; ok && previous != name {
			return nil, fmt.Errorf("%w: entity reference %q has conflicting names", ErrSynchronousRememberEmbeddingFence, ref)
		}
		names[ref] = name
	}
	return names, nil
}

func synchronousRememberEmbeddingValue(ctx context.Context, tx *gorm.DB, teamID string, input PlacementValueInput) (PlacementValueInput, error) {
	input = normalizePlacementValueInput(input)
	var persisted PlacementValueInput
	err := tx.WithContext(ctx).Raw(`SELECT value_type, canonical_value, COALESCE(unit, ''), COALESCE(display, '') FROM value_records WHERE team_id = ?::uuid AND value_type = ? AND canonical_value = ? AND unit IS NOT DISTINCT FROM NULLIF(?, '') AND normalization_version = ? ORDER BY value_id LIMIT 1`, teamID, input.ValueType, input.CanonicalValue, input.Unit, input.NormalizationVersion).Row().Scan(&persisted.ValueType, &persisted.CanonicalValue, &persisted.Unit, &persisted.Display)
	if errors.Is(err, sql.ErrNoRows) {
		return input, nil
	}
	if err != nil {
		return PlacementValueInput{}, err
	}
	if persisted.Display == "" {
		persisted.Display = persisted.CanonicalValue
	}
	return persisted, nil
}

func validateSynchronousRememberEmbeddingResult(result SynchronousRememberEmbeddingResult) error {
	if _, err := uuid.Parse(result.EmbeddingContractID); err != nil || result.EmbeddingDimensions < 1 || strings.TrimSpace(result.EmbeddingModel) == "" || result.SearchGenerationVersion < 1 {
		return fmt.Errorf("%w: incomplete embedding contract fence", ErrSynchronousRememberEmbeddingFence)
	}
	if _, err := uuid.Parse(result.SearchGenerationID); err != nil {
		return fmt.Errorf("%w: invalid search generation fence", ErrSynchronousRememberEmbeddingFence)
	}
	seen := make(map[string]struct{}, len(result.Embeddings))
	for _, item := range result.Embeddings {
		if strings.TrimSpace(item.DocumentHash) == "" || len(item.Vector) != result.EmbeddingDimensions {
			return fmt.Errorf("%w: malformed embedding result", ErrSynchronousRememberEmbeddingFence)
		}
		if _, ok := seen[item.DocumentHash]; ok {
			return fmt.Errorf("%w: duplicate embedding result hash", ErrSynchronousRememberEmbeddingFence)
		}
		if _, err := vectorLiteral(item.Vector); err != nil {
			return fmt.Errorf("%w: invalid vector: %v", ErrSynchronousRememberEmbeddingFence, err)
		}
		seen[item.DocumentHash] = struct{}{}
	}
	return nil
}

func applySynchronousRememberEmbeddings(ctx context.Context, tx *gorm.DB, teamID, ownerID string, documents []SearchDocumentResult, result SynchronousRememberEmbeddingResult) error {
	if tx == nil {
		return wrapSynchronousRememberEmbeddingStage("transaction", errors.New("synchronous Remember embedding transaction is required"))
	}
	return withSystemModeInTx(ctx, tx, teamID, ownerID, func(systemTx *gorm.DB) error {
		return applySynchronousRememberEmbeddingsInSystemMode(ctx, systemTx, teamID, ownerID, documents, result)
	})
}

func applySynchronousRememberEmbeddingsInSystemMode(ctx context.Context, tx *gorm.DB, teamID, ownerID string, documents []SearchDocumentResult, result SynchronousRememberEmbeddingResult) error {
	if err := validateSynchronousRememberEmbeddingResult(result); err != nil {
		return wrapSynchronousRememberEmbeddingStage("result_validation", err)
	}
	if err := lockSynchronousRememberSearchGeneration(ctx, tx, result); err != nil {
		return wrapSynchronousRememberEmbeddingStage("generation_fence", err)
	}
	contract, err := loadActiveSearchContractInTx(ctx, tx)
	if err != nil {
		return wrapSynchronousRememberEmbeddingStage("contract_load", err)
	}
	if result.EmbeddingContractID != contract.EmbeddingContractID || result.EmbeddingDimensions != contract.EmbeddingDimensions || result.EmbeddingModel != contract.EmbeddingModel || result.SearchGenerationID != contract.SearchIndexGenerationID || result.SearchGenerationVersion != int64(contract.IndexGeneration) {
		return wrapSynchronousRememberEmbeddingStage("contract_fence", fmt.Errorf("%w: active search contract changed after embedding", ErrSynchronousRememberEmbeddingFence))
	}
	byHash := make(map[string][]float32, len(result.Embeddings))
	for _, item := range result.Embeddings {
		byHash[item.DocumentHash] = item.Vector
	}
	for _, document := range documents {
		if document.SearchState != "pending" {
			continue
		}
		documentOwnerID := strings.TrimSpace(document.OwnerProfileID)
		if documentOwnerID == "" {
			documentOwnerID = ownerID
		}
		var text, hash string
		if err := tx.WithContext(ctx).Raw(`SELECT document_text, document_hash FROM search_documents WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND search_document_id = ?::uuid FOR SHARE`, teamID, documentOwnerID, document.SearchDocumentID).Row().Scan(&text, &hash); err != nil {
			return wrapSynchronousRememberEmbeddingStage("document_load", err)
		}
		if hash != searchDocumentTextHash(text) {
			return wrapSynchronousRememberEmbeddingStage("document_hash", fmt.Errorf("%w: persisted document hash is not canonical", ErrSynchronousRememberEmbeddingFence))
		}
		vector, ok := byHash[hash]
		if !ok {
			return wrapSynchronousRememberEmbeddingStage("planned_hash_"+strings.TrimSpace(document.SourceKind), fmt.Errorf("%w: final document was not embedded", ErrSynchronousRememberEmbeddingFence))
		}
		literal, err := vectorLiteral(vector)
		if err != nil {
			return wrapSynchronousRememberEmbeddingStage("vector", err)
		}
		if err := retireSynchronousRememberEmbeddingJobs(ctx, tx, document); err != nil {
			return wrapSynchronousRememberEmbeddingStage("job_retirement", err)
		}
		updated := tx.WithContext(ctx).Exec(`UPDATE search_documents SET embedding = ?::vector, search_state = 'current', embedding_updated_at = now(), embedding_error = '', updated_at = now() WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND search_document_id = ?::uuid AND document_hash = ? AND embedding_contract_id = ?::uuid AND embedding_dimensions = ? AND search_state = 'pending'`, literal, teamID, documentOwnerID, document.SearchDocumentID, hash, result.EmbeddingContractID, result.EmbeddingDimensions)
		if updated.Error != nil {
			return wrapSynchronousRememberEmbeddingStage("document_apply", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return wrapSynchronousRememberEmbeddingStage("document_apply", fmt.Errorf("%w: final document changed before vector application", ErrSynchronousRememberEmbeddingFence))
		}
	}
	return nil
}

func retireSynchronousRememberEmbeddingJobs(ctx context.Context, tx *gorm.DB, document SearchDocumentResult) error {
	return tx.WithContext(ctx).Exec(`
		UPDATE embedding_jobs AS job
		SET status = 'stale',
		    error = 'embedded synchronously by Remember',
		    completed_at = COALESCE(job.completed_at, now()),
		    lease_until = NULL,
		    worker_id = '',
		    updated_at = now()
		WHERE job.team_id = ?::uuid
		  AND job.search_document_id = ?::uuid
		  AND job.source_kind = ?
		  AND job.source_id = ?::uuid
		  AND job.source_version = ?
		  AND job.projection_format_version = ?
		  AND job.projection_generation_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND job.document_version = ?
		  AND job.embedding_contract_id = ?::uuid
		  AND job.embedding_dimensions = ?
		  AND job.space_id = ?::uuid
		  AND job.space_generation = ?
		  AND job.status IN ('queued', 'processing', 'failed')
	`, document.TeamID, document.SearchDocumentID, document.SourceKind, document.SourceID,
		document.SourceVersion, document.ProjectionFormat, document.ProjectionGenerationID,
		document.DocumentVersion, document.EmbeddingContractID, document.EmbeddingDimensions,
		document.SpaceID, document.SpaceGeneration).Error
}

func lockSynchronousRememberSearchGeneration(ctx context.Context, tx *gorm.DB, result SynchronousRememberEmbeddingResult) error {
	var state string
	err := tx.WithContext(ctx).Raw(`
		SELECT activation_state
		FROM search_index_generations
		WHERE search_index_generation_id = ?::uuid
		  AND embedding_contract_id = ?::uuid
		FOR SHARE
	`, result.SearchGenerationID, result.EmbeddingContractID).Row().Scan(&state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: planned search generation is no longer active", ErrSynchronousRememberEmbeddingFence)
		}
		return fmt.Errorf("%w: planned search generation lock failed", ErrSynchronousRememberEmbeddingFence)
	}
	if state != "active" {
		return fmt.Errorf("%w: planned search generation is no longer active", ErrSynchronousRememberEmbeddingFence)
	}
	return nil
}
