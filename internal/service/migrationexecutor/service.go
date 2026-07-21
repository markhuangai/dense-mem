package migrationexecutor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

const (
	defaultPageSize = 100
	maxPageSize     = 500
)

var (
	ErrMissingDependency   = errors.New("v2 migration executor: missing dependency")
	ErrMigrationNotRunning = errors.New("v2 migration executor: migration is not running")
	ErrInvalidRunID        = errors.New("v2 migration executor: migration run id is invalid")
	errInvalidLegacyOwner  = errors.New("legacy owner profile does not belong to team")
)

type Store interface {
	GetLatestRun(ctx context.Context) (*domain.V2MigrationRun, error)
	ValidateMigrationOwnerProfile(ctx context.Context, input repository.V2ValidateMigrationOwnerProfileInput) (bool, error)
	UpsertMigrationCorpusItem(ctx context.Context, input repository.V2UpsertMigrationCorpusItemInput) (*domain.V2MigrationCorpusItem, error)
	UpdateMigrationCorpusOutcome(ctx context.Context, input repository.V2UpdateMigrationCorpusOutcomeInput) (*domain.V2MigrationCorpusItem, error)
	UpsertMigrationSourceMap(ctx context.Context, input repository.V2UpsertMigrationSourceMapInput) error
	UpsertMigrationCheckpoint(ctx context.Context, input repository.V2UpsertMigrationCheckpointInput) error
	GetMigrationCheckpoint(ctx context.Context, runID string, checkpointKey string) (map[string]any, error)
	RecordMigrationError(ctx context.Context, input repository.V2RecordMigrationErrorInput) error
	RecordMigrationExclusion(ctx context.Context, input repository.V2RecordMigrationExclusionInput) error
	RefreshMigrationRunStats(ctx context.Context, runID string, now time.Time) (*domain.V2MigrationRun, error)
}

type LegacyCorpusReader interface {
	ReadCorpusPage(ctx context.Context, req neo4j.LegacyCorpusPageRequest) (neo4j.LegacyCorpusPage, error)
}

type RememberService interface {
	RememberV2(ctx context.Context, req memoryservice.V2RememberRequest) (*memoryservice.V2RememberResult, error)
}

type Service interface {
	// RunOnce processes at most one bounded page. Once page processing starts,
	// a non-nil result reports counters reached before an error. Partial errors
	// wrap the same result so callers can inspect it with errors.As.
	RunOnce(ctx context.Context) (*RunOnceResult, error)
}

type Config struct {
	PageSize int
	WorkerID string
	Now      func() time.Time
}

type RunOnceResult struct {
	RunID      string `json:"run_id"`
	Fetched    int    `json:"fetched"`
	Submitted  int    `json:"submitted"`
	Skipped    int    `json:"skipped"`
	Excluded   int    `json:"excluded"`
	Failed     int    `json:"failed"`
	NextCursor string `json:"next_cursor,omitempty"`
	Done       bool   `json:"done"`
}

// PartialRunOnceError wraps an error after RunOnce has produced page counters.
type PartialRunOnceError struct {
	Result *RunOnceResult
	Err    error
}

func (e *PartialRunOnceError) Error() string {
	if e == nil || e.Err == nil {
		return "v2 migration executor: partial run failed"
	}
	return e.Err.Error()
}

func (e *PartialRunOnceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type service struct {
	store    Store
	reader   LegacyCorpusReader
	remember RememberService
	cfg      Config
	now      func() time.Time
}

func New(store Store, reader LegacyCorpusReader, remember RememberService, cfg Config) Service {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if strings.TrimSpace(cfg.WorkerID) == "" {
		cfg.WorkerID = "migration-executor"
	}
	return &service{
		store:    store,
		reader:   reader,
		remember: remember,
		cfg:      cfg,
		now:      now,
	}
}

func (s *service) RunOnce(ctx context.Context) (*RunOnceResult, error) {
	if s.store == nil || s.reader == nil || s.remember == nil {
		return nil, ErrMissingDependency
	}
	run, err := s.store.GetLatestRun(ctx)
	if err != nil {
		return nil, err
	}
	if run == nil || run.State != domain.V2MigrationStateRunning {
		return nil, ErrMigrationNotRunning
	}
	migrationRunID, err := uuid.Parse(strings.TrimSpace(run.RunID))
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRunID, run.RunID)
	}
	cursor, err := s.cursor(ctx, run)
	if err != nil {
		return nil, err
	}
	page, err := s.reader.ReadCorpusPage(ctx, neo4j.LegacyCorpusPageRequest{
		AfterSourceID: cursor,
		Limit:         s.pageSize(),
	})
	if err != nil {
		_ = s.recordError(ctx, run.RunID, "", "", "read_legacy_corpus", "read_failed", err, true, nil)
		return nil, err
	}
	result := &RunOnceResult{RunID: run.RunID, Fetched: len(page.Items), NextCursor: page.NextCursor, Done: len(page.Items) == 0}
	for _, item := range page.Items {
		if err := s.processItem(ctx, run, migrationRunID, item, result); err != nil {
			return result, partialRunOnceError(result, err)
		}
	}
	if err := s.store.UpsertMigrationCheckpoint(ctx, repository.V2UpsertMigrationCheckpointInput{
		RunID:         run.RunID,
		CheckpointKey: domain.V2MigrationCheckpointLegacyNeo4jCursor,
		CheckpointValue: map[string]any{
			"after_source_id": result.NextCursor,
			"done":            result.Done,
		},
		LeaseOwner: s.cfg.WorkerID,
		Now:        s.now(),
	}); err != nil {
		return result, partialRunOnceError(result, err)
	}
	if _, err := s.store.RefreshMigrationRunStats(ctx, run.RunID, s.now()); err != nil {
		return result, partialRunOnceError(result, err)
	}
	return result, nil
}

func (s *service) processItem(ctx context.Context, run *domain.V2MigrationRun, migrationRunID uuid.UUID, item neo4j.LegacyCorpusItem, result *RunOnceResult) error {
	item = normalizeLegacyCorpusItem(item)
	sourceKind := legacySourceKind(item)
	if err := validateLegacyCorpusItem(item); err != nil {
		result.Excluded++
		if recordErr := s.store.RecordMigrationExclusion(ctx, repository.V2RecordMigrationExclusionInput{
			RunID:         run.RunID,
			SourceKind:    sourceKind,
			SourceID:      strings.TrimSpace(item.SourceID),
			Reason:        err.Error(),
			BlocksCutover: true,
			Metadata:      legacyCorpusMetadata(item),
			Now:           s.now(),
		}); recordErr != nil {
			return recordErr
		}
		if recordErr := s.recordError(ctx, run.RunID, sourceKind, item.SourceID, "validate_legacy_item", "invalid_legacy_item", err, false, nil); recordErr != nil {
			return recordErr
		}
		return nil
	}
	ownerValid, err := s.validateLegacyOwnerProfile(ctx, run.RunID, sourceKind, item, result)
	if err != nil {
		return err
	}
	if !ownerValid {
		return nil
	}
	corpusItem, err := s.store.UpsertMigrationCorpusItem(ctx, repository.V2UpsertMigrationCorpusItemInput{
		RunID:          run.RunID,
		TeamID:         strings.TrimSpace(item.TeamID),
		OwnerProfileID: strings.TrimSpace(item.OwnerProfileID),
		SourceKind:     sourceKind,
		SourceID:       strings.TrimSpace(item.SourceID),
		SourceHash:     strings.TrimSpace(item.SourceHash),
		ItemKind:       domain.V2MigrationItemKindEvidence,
		Outcome:        domain.V2MigrationOutcomePending,
		Metadata:       legacyCorpusMetadata(item),
		Now:            s.now(),
	})
	if err != nil {
		result.Failed++
		if recordErr := s.store.RecordMigrationExclusion(ctx, repository.V2RecordMigrationExclusionInput{
			RunID:         run.RunID,
			SourceKind:    sourceKind,
			SourceID:      strings.TrimSpace(item.SourceID),
			Reason:        "postgres corpus item upsert failed",
			BlocksCutover: true,
			Metadata:      map[string]any{"error": err.Error()},
			Now:           s.now(),
		}); recordErr != nil {
			return recordErr
		}
		if recordErr := s.recordError(ctx, run.RunID, sourceKind, item.SourceID, "upsert_corpus_item", "postgres_write_failed", err, true, nil); recordErr != nil {
			return recordErr
		}
		return nil
	}
	if corpusItem.Outcome != domain.V2MigrationOutcomePending {
		result.Skipped++
		return nil
	}
	rememberResult, err := s.remember.RememberV2(legacyActorContext(ctx, item, migrationRunID), legacyRememberRequest(run.RunID, item))
	if err != nil {
		result.Failed++
		if _, updateErr := s.store.UpdateMigrationCorpusOutcome(ctx, repository.V2UpdateMigrationCorpusOutcomeInput{
			RunID:           run.RunID,
			SourceKind:      sourceKind,
			SourceID:        strings.TrimSpace(item.SourceID),
			Outcome:         domain.V2MigrationOutcomeFailed,
			ExclusionReason: "remember_v2_failed",
			Metadata:        map[string]any{"error": err.Error()},
			Now:             s.now(),
		}); updateErr != nil {
			return updateErr
		}
		if recordErr := s.recordError(ctx, run.RunID, sourceKind, item.SourceID, "remember_v2", "remember_failed", err, true, nil); recordErr != nil {
			return recordErr
		}
		return nil
	}
	result.Submitted++
	if err := s.recordSourceMaps(ctx, run.RunID, item, rememberResult); err != nil {
		return err
	}
	if _, err := s.store.UpdateMigrationCorpusOutcome(ctx, repository.V2UpdateMigrationCorpusOutcomeInput{
		RunID:      run.RunID,
		SourceKind: sourceKind,
		SourceID:   strings.TrimSpace(item.SourceID),
		Outcome:    rememberOutcome(rememberResult),
		IngestID:   strings.TrimSpace(rememberResult.IngestID),
		Metadata: map[string]any{
			"remember_processing_state": rememberResult.ProcessingState,
			"submitted_at":              s.now().UTC().Format(time.RFC3339Nano),
		},
		Now: s.now(),
	}); err != nil {
		return err
	}
	return nil
}

func (s *service) validateLegacyOwnerProfile(
	ctx context.Context,
	runID string,
	sourceKind string,
	item neo4j.LegacyCorpusItem,
	result *RunOnceResult,
) (bool, error) {
	teamID := strings.TrimSpace(item.TeamID)
	ownerProfileID := strings.TrimSpace(item.OwnerProfileID)
	ok, err := s.store.ValidateMigrationOwnerProfile(ctx, repository.V2ValidateMigrationOwnerProfileInput{
		TeamID:         teamID,
		OwnerProfileID: ownerProfileID,
	})
	if err != nil {
		result.Failed++
		if recordErr := s.recordError(ctx, runID, sourceKind, item.SourceID, "validate_legacy_owner_profile", "owner_profile_validation_failed", err, true, nil); recordErr != nil {
			return false, recordErr
		}
		return false, err
	}
	if ok {
		return true, nil
	}

	result.Excluded++
	err = fmt.Errorf("%w: team_id %s owner_profile_id %s", errInvalidLegacyOwner, teamID, ownerProfileID)
	if recordErr := s.store.RecordMigrationExclusion(ctx, repository.V2RecordMigrationExclusionInput{
		RunID:         runID,
		SourceKind:    sourceKind,
		SourceID:      strings.TrimSpace(item.SourceID),
		Reason:        err.Error(),
		BlocksCutover: true,
		Metadata:      legacyCorpusMetadata(item),
		Now:           s.now(),
	}); recordErr != nil {
		return false, recordErr
	}
	return false, s.recordError(ctx, runID, sourceKind, item.SourceID, "validate_legacy_owner_profile", "invalid_legacy_owner_profile", err, false, nil)
}

func (s *service) recordSourceMaps(ctx context.Context, runID string, item neo4j.LegacyCorpusItem, rememberResult *memoryservice.V2RememberResult) error {
	if rememberResult == nil {
		return nil
	}
	sourceKind := legacySourceKind(item)
	sourceID := strings.TrimSpace(item.SourceID)
	if strings.TrimSpace(rememberResult.IngestID) != "" {
		if err := s.store.UpsertMigrationSourceMap(ctx, repository.V2UpsertMigrationSourceMapInput{
			RunID:      runID,
			SourceKind: sourceKind,
			SourceID:   sourceID,
			TargetType: domain.V2MigrationTargetIngest,
			TargetID:   strings.TrimSpace(rememberResult.IngestID),
			Metadata:   map[string]any{"processing_state": rememberResult.ProcessingState},
			Now:        s.now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) recordError(
	ctx context.Context,
	runID string,
	sourceKind string,
	sourceID string,
	phase string,
	code string,
	err error,
	retryable bool,
	metadata map[string]any,
) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	return s.store.RecordMigrationError(ctx, repository.V2RecordMigrationErrorInput{
		RunID:      runID,
		SourceKind: sourceKind,
		SourceID:   strings.TrimSpace(sourceID),
		Phase:      phase,
		ErrorCode:  code,
		Message:    err.Error(),
		Retryable:  retryable,
		Metadata:   metadata,
		Now:        s.now(),
	})
}

func (s *service) cursor(ctx context.Context, run *domain.V2MigrationRun) (string, error) {
	checkpoint, err := s.store.GetMigrationCheckpoint(ctx, run.RunID, domain.V2MigrationCheckpointLegacyNeo4jCursor)
	if err != nil {
		return "", err
	}
	if cursor := stringFromMap(checkpoint, "after_source_id"); cursor != "" {
		return cursor, nil
	}
	if run.CheckpointKey == domain.V2MigrationCheckpointLegacyNeo4jCursor {
		return stringFromMap(run.CheckpointValue, "after_source_id"), nil
	}
	return "", nil
}

func (s *service) pageSize() int {
	pageSize := s.cfg.PageSize
	if pageSize <= 0 {
		return defaultPageSize
	}
	if pageSize > maxPageSize {
		return maxPageSize
	}
	return pageSize
}

func validateLegacyCorpusItem(item neo4j.LegacyCorpusItem) error {
	if strings.TrimSpace(item.SourceID) == "" {
		return errors.New("legacy source_id is required")
	}
	if _, err := uuid.Parse(strings.TrimSpace(item.TeamID)); err != nil {
		return fmt.Errorf("legacy team_id is invalid: %w", err)
	}
	if _, err := uuid.Parse(strings.TrimSpace(item.OwnerProfileID)); err != nil {
		return fmt.Errorf("legacy owner_profile_id is invalid: %w", err)
	}
	if strings.TrimSpace(item.Content) == "" {
		return errors.New("legacy evidence content is required")
	}
	if strings.TrimSpace(item.SourceHash) == "" {
		return errors.New("legacy source_hash is required")
	}
	return nil
}

func normalizeLegacyCorpusItem(item neo4j.LegacyCorpusItem) neo4j.LegacyCorpusItem {
	item.SourceHash = strings.TrimSpace(item.SourceHash)
	if item.SourceHash == "" && strings.TrimSpace(item.Content) != "" {
		item.SourceHash = neo4j.LegacyContentHash(item.Content)
	}
	return item
}

func partialRunOnceError(result *RunOnceResult, err error) error {
	if err == nil {
		return nil
	}
	return &PartialRunOnceError{Result: result, Err: err}
}

func legacyActorContext(ctx context.Context, item neo4j.LegacyCorpusItem, migrationRunID uuid.UUID) context.Context {
	teamID, _ := uuid.Parse(strings.TrimSpace(item.TeamID))
	ownerID, _ := uuid.Parse(strings.TrimSpace(item.OwnerProfileID))
	ctx = requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
		TeamID:      teamID,
		ProfileID:   ownerID,
		ProfileName: strings.TrimSpace(item.OwnerProfileName),
	})
	return requestctx.WithMigrationActor(ctx, requestctx.MigrationActor{RunID: migrationRunID})
}

func legacyRememberRequest(runID string, item neo4j.LegacyCorpusItem) memoryservice.V2RememberRequest {
	sourceKey := legacySourceRef(item)
	return memoryservice.V2RememberRequest{
		ContractVersion: domain.V2ContractVersion,
		Evidence: []memoryservice.V2RememberEvidenceInput{{
			Content:        item.Content,
			SourceType:     legacySourceType(item.SourceType),
			Source:         legacySource(item),
			Authority:      legacyAuthority(item.Authority),
			SourceKey:      sourceKey,
			SourceRevision: strings.TrimSpace(item.SourceHash),
			IdempotencyKey: legacyIdempotencyKey(runID, item),
			Labels:         legacyLabels(item.Labels),
			Metadata:       legacyCorpusMetadata(item),
		}},
		EntityHints:       legacyEntityHints(item),
		RelationshipHints: legacyRelationshipHints(item),
		IdempotencyKey:    legacyIdempotencyKey(runID, item),
	}
}

func legacyIdempotencyKey(runID string, item neo4j.LegacyCorpusItem) string {
	return fmt.Sprintf("v2-migration:%s:%s:%s", strings.TrimSpace(runID), legacySourceKind(item), strings.TrimSpace(item.SourceID))
}

func legacySourceKind(item neo4j.LegacyCorpusItem) string {
	if strings.TrimSpace(item.SourceKind) == "" {
		return neo4j.LegacyCorpusSourceKind
	}
	return strings.TrimSpace(item.SourceKind)
}

func legacySource(item neo4j.LegacyCorpusItem) string {
	if strings.TrimSpace(item.Source) != "" {
		return strings.TrimSpace(item.Source)
	}
	return legacySourceRef(item)
}

func legacySourceRef(item neo4j.LegacyCorpusItem) string {
	return "neo4j://source-fragment/" + strings.TrimSpace(item.SourceID)
}

func legacySourceType(value string) string {
	switch strings.TrimSpace(value) {
	case "conversation", "document", "observation", "manual":
		return strings.TrimSpace(value)
	default:
		return "document"
	}
}

func legacyAuthority(value string) string {
	switch strings.TrimSpace(value) {
	case "primary", "secondary", "derived":
		return strings.TrimSpace(value)
	case "authoritative", "user", "":
		return "primary"
	default:
		return "derived"
	}
}

func legacyLabels(labels []string) []string {
	out := make([]string, 0, len(labels)+1)
	seen := map[string]bool{}
	for _, label := range append(labels, "legacy_neo4j_migration") {
		trimmed := strings.TrimSpace(label)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

func legacyCorpusMetadata(item neo4j.LegacyCorpusItem) map[string]any {
	out := map[string]any{
		"migration_source_kind": legacySourceKind(item),
		"legacy_source_id":      strings.TrimSpace(item.SourceID),
		"legacy_source_hash":    strings.TrimSpace(item.SourceHash),
		"legacy_status":         strings.TrimSpace(item.Status),
		"legacy_authority":      strings.TrimSpace(item.Authority),
		"legacy_source_type":    strings.TrimSpace(item.SourceType),
		"legacy_claim_hints":    item.Claims,
		"legacy_fact_hints":     item.Facts,
	}
	if item.CreatedAt != nil {
		out["legacy_created_at"] = item.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if item.UpdatedAt != nil {
		out["legacy_updated_at"] = item.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	if len(item.Metadata) > 0 {
		out["legacy_metadata"] = item.Metadata
	}
	if len(item.Classification) > 0 {
		out["legacy_classification"] = item.Classification
	}
	return out
}

func legacyEntityHints(item neo4j.LegacyCorpusItem) []map[string]any {
	seen := map[string]bool{}
	out := []map[string]any{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, map[string]any{
			"name":          name,
			"proposal_only": true,
			"source":        "legacy_neo4j_migration",
		})
	}
	for _, claim := range item.Claims {
		add(claim.Subject)
		add(claim.Object)
	}
	for _, fact := range item.Facts {
		add(fact.Subject)
		add(fact.Object)
	}
	return out
}

func legacyRelationshipHints(item neo4j.LegacyCorpusItem) []map[string]any {
	out := []map[string]any{}
	for _, claim := range item.Claims {
		if hint := relationshipHint(claim.Subject, claim.Predicate, claim.Object, claim.ClaimID, "claim"); hint != nil {
			out = append(out, hint)
		}
	}
	for _, fact := range item.Facts {
		if hint := relationshipHint(fact.Subject, fact.Predicate, fact.Object, fact.FactID, "fact"); hint != nil {
			out = append(out, hint)
		}
	}
	return out
}

func relationshipHint(subject string, predicate string, object string, legacyID string, legacyKind string) map[string]any {
	subject = strings.TrimSpace(subject)
	predicate = strings.TrimSpace(predicate)
	object = strings.TrimSpace(object)
	if subject == "" || predicate == "" || object == "" {
		return nil
	}
	return map[string]any{
		"subject":       subject,
		"predicate":     predicate,
		"object":        object,
		"legacy_id":     strings.TrimSpace(legacyID),
		"legacy_kind":   legacyKind,
		"proposal_only": true,
		"source":        "legacy_neo4j_migration",
	}
}

func rememberOutcome(result *memoryservice.V2RememberResult) string {
	if result == nil {
		return domain.V2MigrationOutcomeFailed
	}
	switch result.ProcessingState {
	case string(domain.V2PlacementRunQuarantined):
		return domain.V2MigrationOutcomeQuarantined
	case string(domain.V2PlacementRunFailed):
		return domain.V2MigrationOutcomeFailed
	}
	return domain.V2MigrationOutcomeNeedsReview
}

func stringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	raw, ok := values[key]
	if !ok || raw == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}
