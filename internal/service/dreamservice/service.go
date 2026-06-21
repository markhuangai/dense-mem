package dreamservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/fulltextquery"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	neo4jstorage "github.com/markhuangai/dense-mem/internal/storage/neo4j"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"gorm.io/gorm"
)

const (
	lockTimeout = 30 * time.Second
)

type service struct {
	deps Dependencies
	now  func() time.Time
}

var _ Service = (*service)(nil)

func New(deps Dependencies) Service {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if deps.Generator == nil {
		deps.Generator = NewHeuristicGenerator("")
	}
	return &service{deps: deps, now: now}
}

func (s *service) RunCycle(ctx context.Context, profileID string, req RunCycleRequest) (*RunCycleResult, error) {
	if strings.TrimSpace(profileID) == "" {
		return nil, fmt.Errorf("dreaming cycle: profile id is required")
	}
	cfg, err := s.EffectiveConfig(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled && !req.Manual {
		return &RunCycleResult{ProfileID: profileID, RunDate: s.now().Format("2006-01-02"), Status: "skipped"}, nil
	}
	if req.MaxOutputs > 0 {
		cfg.MaxOutputs = req.MaxOutputs
	}
	reflectEnabled := boolValue(req.ReflectEnabled, cfg.ReflectEnabled)
	reevaluateEnabled := boolValue(req.ReevaluateEnabled, cfg.ReevaluateEnabled)
	dreamEnabled := boolValue(req.DreamEnabled, cfg.DreamEnabled)

	started := s.now().UTC()
	runDate := localRunDate(started, cfg)
	runID := uuid.NewString()
	result := &RunCycleResult{
		RunID:      runID,
		ProfileID:  profileID,
		RunDate:    runDate,
		StartedAt:  started,
		Status:     "running",
		ReflectRan: false,
		DreamRan:   false,
	}

	run := func() error {
		var reflection *memoryservice.ReflectResult
		if reflectEnabled && s.deps.Memory != nil {
			res, err := s.deps.Memory.Reflect(ctx, profileID, memoryservice.ReflectRequest{Limit: 50})
			if err != nil {
				return fmt.Errorf("reflect phase: %w", err)
			}
			reflection = res
			result.ReflectRan = true
			result.StaleFacts = len(res.StaleFacts)
			result.CandidateClaims = len(res.CandidateClaims)
			result.DisputedClaims = len(res.DisputedClaims)
			result.Clarifications = len(res.Clarifications)
		}
		if reevaluateEnabled {
			count, err := s.reevaluateDreams(ctx, profileID, s.now().UTC())
			if err != nil {
				return fmt.Errorf("re-evaluate phase: %w", err)
			}
			result.ReevaluateRan = true
			result.ReevaluatedDreams = count
		}
		if dreamEnabled {
			created, err := s.generateDreams(ctx, profileID, runID, cfg, reflection)
			if err != nil {
				return fmt.Errorf("dream phase: %w", err)
			}
			result.DreamRan = true
			result.CreatedDreams = created
		}
		return nil
	}

	runAndRecord := func() error {
		if !req.Manual {
			exists, err := s.cycleRunExists(ctx, profileID, runDate)
			if err != nil {
				return err
			}
			if exists {
				result.CompletedAt = s.now().UTC()
				result.Status = "skipped"
				return nil
			}
		}
		runErr := run()
		result.CompletedAt = s.now().UTC()
		if runErr != nil {
			result.Status = "error"
			result.Error = runErr.Error()
			_ = s.recordRun(ctx, profileID, result)
			return runErr
		}
		result.Status = "completed"
		if err := s.recordRun(ctx, profileID, result); err != nil {
			return err
		}
		return nil
	}

	var runErr error
	if s.deps.Locker != nil && s.deps.Postgres != nil {
		runErr = s.deps.Locker.WithCycleLock(ctx, s.deps.Postgres, profileID, runDate, lockTimeout, func(_ *gorm.DB) error {
			return runAndRecord()
		})
	} else {
		runErr = runAndRecord()
	}
	if runErr != nil {
		if result.CompletedAt.IsZero() {
			result.CompletedAt = s.now().UTC()
			result.Status = "error"
			result.Error = runErr.Error()
			_ = s.recordRun(ctx, profileID, result)
		}
		return result, runErr
	}
	return result, nil
}

func (s *service) generateDreams(ctx context.Context, profileID, runID string, cfg EffectiveConfig, reflection *memoryservice.ReflectResult) (int, error) {
	inputs, err := s.loadDreamInputs(ctx, profileID, cfg.MaxOutputs*4)
	if err != nil {
		return 0, err
	}
	if len(inputs) < 2 {
		return 0, nil
	}
	existing, _, err := s.List(ctx, profileID, ListOptions{Limit: cfg.MaxOutputs, Status: string(domain.DreamStatusProposed)})
	if err != nil {
		return 0, err
	}
	generator := s.deps.Generator
	if generator == nil {
		generator = NewHeuristicGenerator("")
	}
	model := generator.Model()
	generated, err := generator.Generate(ctx, profileID, GenerateRequest{
		MaxOutputs:     cfg.MaxOutputs,
		Reflection:     reflection,
		Inputs:         inputs,
		Existing:       existing,
		GeneratorModel: model,
	})
	if err != nil {
		return 0, err
	}
	now := s.now().UTC()
	created := 0
	for _, g := range generated {
		dream := &domain.Dream{
			DreamID:         uuid.NewString(),
			ProfileID:       profileID,
			Hypothesis:      strings.TrimSpace(g.Hypothesis),
			WhatIf:          strings.TrimSpace(g.WhatIf),
			PossibleOutcome: strings.TrimSpace(g.PossibleOutcome),
			Rationale:       strings.TrimSpace(g.Rationale),
			Likelihood:      clamp01(g.Likelihood),
			Confidence:      clamp01(g.Confidence),
			Status:          domain.DreamStatusProposed,
			Cycle:           CycleDream,
			CycleRunID:      runID,
			GeneratorModel:  model,
			SourceRefs:      g.SourceRefs,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if dream.Hypothesis == "" || len(dream.SourceRefs) == 0 {
			continue
		}
		dream.ContentHash = dreamContentHash(dream)
		inserted, err := s.upsertDream(ctx, profileID, dream)
		if err != nil {
			return created, err
		}
		if inserted {
			created++
		}
	}
	return created, nil
}

func (s *service) List(ctx context.Context, profileID string, opts ListOptions) ([]*domain.Dream, string, error) {
	if s.deps.Graph == nil {
		return nil, "", fmt.Errorf("dream list: graph is required")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	statusFilter := strings.TrimSpace(opts.Status)
	sortField := normalizeDreamSort(opts.Sort)
	direction := normalizeDreamDirection(opts.Direction)
	cursorAt, cursorDreamID, err := decodeDreamCursor(opts.Cursor, sortField, direction)
	if err != nil {
		return nil, "", fmt.Errorf("dream list: %w", err)
	}
	sortExpression := dreamSortExpression(sortField)
	comparator := "<"
	orderDirection := "DESC"
	if direction == DreamDirectionAsc {
		comparator = ">"
		orderDirection = "ASC"
	}
	var cursorAtParam any
	var cursorDreamIDParam any
	if !cursorAt.IsZero() {
		cursorAtParam = cursorAt
		cursorDreamIDParam = cursorDreamID
	}
	query := fmt.Sprintf(`
MATCH (d:Dream {team_id: $profileId})
WITH d, %s AS sort_at
WHERE ($status = '' OR d.status = $status)
  AND ($cursorAt IS NULL OR sort_at %s $cursorAt
       OR (sort_at = $cursorAt AND d.dream_id > $cursorDreamID))
RETURN d
ORDER BY sort_at %s, d.dream_id ASC
LIMIT $limit`, sortExpression, comparator, orderDirection)
	_, rows, err := s.deps.Graph.ScopedRead(ctx, profileID, query, map[string]any{
		"status":        statusFilter,
		"cursorAt":      cursorAtParam,
		"cursorDreamID": cursorDreamIDParam,
		"limit":         int64(limit + 1),
	})
	if err != nil {
		return nil, "", fmt.Errorf("dream list: %w", err)
	}
	dreams := make([]*domain.Dream, 0, len(rows))
	for _, row := range rows {
		dream, err := dreamFromRow(row)
		if err != nil {
			return nil, "", err
		}
		if dream != nil {
			dreams = append(dreams, dream)
		}
	}
	nextCursor := ""
	if len(dreams) > limit {
		last := dreams[limit-1]
		nextCursor = encodeDreamCursor(dreamCursor{
			Sort:      sortField,
			Direction: direction,
			SortAt:    dreamSortTime(last, sortField),
			DreamID:   last.DreamID,
		})
		dreams = dreams[:limit]
	}
	return dreams, nextCursor, nil
}

func (s *service) Get(ctx context.Context, profileID, dreamID string) (*domain.Dream, error) {
	if s.deps.Graph == nil {
		return nil, fmt.Errorf("dream get: graph is required")
	}
	_, rows, err := s.deps.Graph.ScopedRead(ctx, profileID, `
MATCH (d:Dream {team_id: $profileId, dream_id: $dreamId})
RETURN d
LIMIT 1`, map[string]any{"dreamId": dreamID})
	if err != nil {
		return nil, fmt.Errorf("dream get: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrDreamNotFound
	}
	return dreamFromRow(rows[0])
}

func (s *service) ListRuns(ctx context.Context, profileID string, limit int) ([]*RunCycleResult, error) {
	if s.deps.Graph == nil {
		return nil, fmt.Errorf("dream runs: graph is required")
	}
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	_, rows, err := s.deps.Graph.ScopedRead(ctx, profileID, `
MATCH (r:DreamCycleRun {team_id: $profileId})
RETURN r
ORDER BY r.started_at DESC
LIMIT $limit`, map[string]any{"limit": int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("dream runs: %w", err)
	}
	runs := make([]*RunCycleResult, 0, len(rows))
	for _, row := range rows {
		run := runCycleResultFromRow(row, profileID)
		if run != nil {
			runs = append(runs, run)
		}
	}
	return runs, nil
}

func (s *service) Recall(ctx context.Context, profileID, query string, limit int) ([]*domain.Dream, error) {
	if s.deps.Graph == nil || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	searchQuery := fulltextquery.PlainText(query)
	if searchQuery == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	_, rows, err := s.deps.Graph.ScopedRead(ctx, profileID, `
CALL db.index.fulltext.queryNodes('dream_recall_idx', $searchQuery) YIELD node AS d, score
WHERE d.team_id = $profileId
  AND d.status IN ['proposed', 'reinforced']
RETURN d, score
ORDER BY score DESC
LIMIT $limit`, map[string]any{
		"searchQuery": searchQuery,
		"limit":       int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("dream recall: %w", err)
	}
	dreams := make([]*domain.Dream, 0, len(rows))
	for _, row := range rows {
		dream, err := dreamFromRow(row)
		if err != nil {
			return nil, err
		}
		if dream != nil {
			dreams = append(dreams, dream)
		}
	}
	return dreams, nil
}

func (s *service) ResolveFeedback(ctx context.Context, profileID string, req ResolveFeedbackRequest) (*ResolveFeedbackResult, error) {
	dreamID := strings.TrimSpace(req.DreamID)
	if dreamID == "" {
		return nil, fmt.Errorf("resolve dream feedback: dream_id is required")
	}
	decision := strings.TrimSpace(req.Decision)
	dream, err := s.Get(ctx, profileID, dreamID)
	if err != nil {
		s.recordDreamFeedback(ctx, decision, nil, "error")
		return nil, err
	}
	switch decision {
	case "reject":
		if err := s.updateDreamStatus(ctx, profileID, dreamID, domain.DreamStatusRejected, strings.TrimSpace(req.Feedback)); err != nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
	case "stale":
		if err := s.updateDreamStatus(ctx, profileID, dreamID, domain.DreamStatusStale, strings.TrimSpace(req.Feedback)); err != nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
	case "reinforce":
		if err := s.updateDreamStatus(ctx, profileID, dreamID, domain.DreamStatusReinforced, strings.TrimSpace(req.Feedback)); err != nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
	case "promote_candidate":
		if s.deps.FragmentCreate == nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, fmt.Errorf("resolve dream feedback: fragment create service is required")
		}
		content := dreamPromotionContent(dream, req.Feedback)
		fragment, err := s.deps.FragmentCreate.Create(ctx, profileID, &dto.CreateFragmentRequest{
			Content:        content,
			SourceType:     "conversation",
			Source:         "dream_feedback:" + dream.DreamID,
			Authority:      "primary",
			IdempotencyKey: "dream-feedback:" + dream.DreamID,
			Labels:         []string{"dream_feedback"},
			Metadata: map[string]any{
				"dream_id":         dream.DreamID,
				"dream_status":     string(dream.Status),
				"dream_likelihood": dream.Likelihood,
				"dream_confidence": dream.Confidence,
			},
			SourceQuality: 0.75,
		})
		if err != nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
		if err := s.linkDreamPromotion(ctx, profileID, dream.DreamID, fragment.Fragment.FragmentID); err != nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
		if err := s.updateDreamStatus(ctx, profileID, dreamID, domain.DreamStatusPromoted, strings.TrimSpace(req.Feedback)); err != nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
		updated, err := s.Get(ctx, profileID, dreamID)
		if err != nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
		s.recordDreamFeedback(ctx, decision, dream, "ok")
		return &ResolveFeedbackResult{Dream: updated, Fragment: fragment}, nil
	default:
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, fmt.Errorf("%w: %s", ErrInvalidDreamStatus, decision)
	}
	updated, err := s.Get(ctx, profileID, dreamID)
	if err != nil {
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, err
	}
	s.recordDreamFeedback(ctx, decision, dream, "ok")
	return &ResolveFeedbackResult{Dream: updated}, nil
}

func (s *service) recordDreamFeedback(ctx context.Context, decision string, dream *domain.Dream, outcome string) {
	fromStatus := ""
	if dream != nil {
		fromStatus = string(dream.Status)
	}
	observability.RecordDreamFeedback(ctx, s.deps.Metrics, observability.DreamFeedback{
		Decision:   decision,
		Outcome:    outcome,
		FromStatus: fromStatus,
	})
}

func (s *service) Status(ctx context.Context, profileID string) (*StatusResult, error) {
	cfg, err := s.EffectiveConfig(ctx, profileID)
	if err != nil {
		return nil, err
	}
	pending, _, err := s.List(ctx, profileID, ListOptions{Limit: 100, Status: string(domain.DreamStatusProposed)})
	if err != nil {
		return nil, err
	}
	latest, _ := s.latestRun(ctx, profileID)
	return &StatusResult{EffectiveConfig: cfg, LatestRun: latest, PendingCount: len(pending)}, nil
}

func (s *service) loadDreamInputs(ctx context.Context, profileID string, limit int) ([]DreamInput, error) {
	if s.deps.Graph == nil {
		return nil, fmt.Errorf("dream inputs: graph is required")
	}
	if limit <= 0 {
		limit = 20
	}
	query := `
CALL {
  MATCH (f:Fact {team_id: $profileId})
  WHERE coalesce(f.status, '') IN ['active', 'needs_revalidation']
  RETURN 'fact' AS type, f.fact_id AS id, f.subject AS subject, f.predicate AS predicate, f.object AS object, '' AS content, f.status AS status, f.recorded_at AS sort_at
  UNION ALL
  MATCH (c:Claim {team_id: $profileId})
  WHERE coalesce(c.status, '') IN ['candidate', 'validated', 'disputed']
  RETURN 'claim' AS type, c.claim_id AS id, c.subject AS subject, c.predicate AS predicate, c.object AS object, '' AS content, c.status AS status, c.recorded_at AS sort_at
  UNION ALL
  MATCH (sf:SourceFragment {team_id: $profileId})
  WHERE coalesce(sf.status, 'active') <> 'retracted'
  RETURN 'fragment' AS type, sf.fragment_id AS id, '' AS subject, '' AS predicate, '' AS object, sf.content AS content, coalesce(sf.status, 'active') AS status, sf.created_at AS sort_at
}
RETURN type, id, subject, predicate, object, content, status
ORDER BY sort_at DESC
LIMIT $totalLimit`
	_, rows, err := s.deps.Graph.ScopedRead(ctx, profileID, query, map[string]any{
		"limit":      int64(limit),
		"totalLimit": int64(limit * 3),
	})
	if err != nil {
		return nil, fmt.Errorf("dream inputs: %w", err)
	}
	inputs := make([]DreamInput, 0, len(rows))
	for _, row := range rows {
		inputs = append(inputs, DreamInput{
			Type:      stringFromRow(row, "type"),
			ID:        stringFromRow(row, "id"),
			Subject:   stringFromRow(row, "subject"),
			Predicate: stringFromRow(row, "predicate"),
			Object:    stringFromRow(row, "object"),
			Content:   stringFromRow(row, "content"),
			Status:    stringFromRow(row, "status"),
		})
	}
	return inputs, nil
}

func (s *service) upsertDream(ctx context.Context, profileID string, dream *domain.Dream) (bool, error) {
	sourceJSON, err := json.Marshal(dream.SourceRefs)
	if err != nil {
		return false, fmt.Errorf("dream upsert: source refs: %w", err)
	}
	inserted := false
	err = s.deps.Graph.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		res, err := neo4jstorage.RunScoped(ctx, tx, profileID, `
MERGE (d:Dream {team_id: $profileId, content_hash: $contentHash})
ON CREATE SET
  d.dream_id = $dreamId,
  d.hypothesis = $hypothesis,
  d.what_if = $whatIf,
  d.possible_outcome = $possibleOutcome,
  d.rationale = $rationale,
  d.likelihood = $likelihood,
  d.confidence = $confidence,
  d.status = $status,
  d.cycle = $cycle,
  d.cycle_run_id = $cycleRunId,
  d.generator_model = $generatorModel,
  d.source_refs_json = $sourceRefsJSON,
  d.created_at = $createdAt,
  d.updated_at = $updatedAt
ON MATCH SET
  d.updated_at = $updatedAt,
  d.last_evaluated_at = coalesce(d.last_evaluated_at, $updatedAt)
RETURN d.dream_id AS dream_id, d.dream_id = $dreamId AS inserted`, map[string]any{
			"contentHash":     dream.ContentHash,
			"dreamId":         dream.DreamID,
			"hypothesis":      dream.Hypothesis,
			"whatIf":          dream.WhatIf,
			"possibleOutcome": dream.PossibleOutcome,
			"rationale":       dream.Rationale,
			"likelihood":      dream.Likelihood,
			"confidence":      dream.Confidence,
			"status":          string(dream.Status),
			"cycle":           dream.Cycle,
			"cycleRunId":      dream.CycleRunID,
			"generatorModel":  dream.GeneratorModel,
			"sourceRefsJSON":  string(sourceJSON),
			"createdAt":       dream.CreatedAt,
			"updatedAt":       dream.UpdatedAt,
		})
		if err != nil {
			return err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}
		inserted, _ = records[0].AsMap()["inserted"].(bool)
		dreamID, _ := records[0].AsMap()["dream_id"].(string)
		if dreamID != "" {
			dream.DreamID = dreamID
		}
		if !inserted {
			return nil
		}
		for _, ref := range dream.SourceRefs {
			if err := linkDreamSource(ctx, tx, profileID, dream.DreamID, ref); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("dream upsert: %w", err)
	}
	return inserted, nil
}

func linkDreamSource(ctx context.Context, tx neo4j.ManagedTransaction, profileID, dreamID string, ref domain.DreamSourceRef) error {
	label, idProp, err := sourceLabelAndID(ref.Type)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`
MATCH (d:Dream {team_id: $profileId, dream_id: $dreamId})
MATCH (src:%s {team_id: $profileId, %s: $sourceId})
MERGE (d)-[r:DREAMS_FROM {team_id: $profileId, source_type: $sourceType, source_id: $sourceId}]->(src)
ON CREATE SET r.created_at = $createdAt`, label, idProp)
	res, err := neo4jstorage.RunScoped(ctx, tx, profileID, query, map[string]any{
		"dreamId":    dreamID,
		"sourceId":   ref.ID,
		"sourceType": ref.Type,
		"createdAt":  time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	_, err = res.Consume(ctx)
	return err
}

func (s *service) reevaluateDreams(ctx context.Context, profileID string, now time.Time) (int, error) {
	if s.deps.Graph == nil {
		return 0, fmt.Errorf("dream re-evaluate: graph is required")
	}
	count := 0
	err := s.deps.Graph.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		res, err := neo4jstorage.RunScoped(ctx, tx, profileID, `
MATCH (d:Dream {team_id: $profileId})
WHERE d.status IN ['proposed', 'reinforced']
OPTIONAL MATCH (d)-[r:DREAMS_FROM {team_id: $profileId}]->(src)
WITH d, count(src) AS source_count
SET d.last_evaluated_at = $now,
    d.updated_at = $now,
    d.status = CASE
      WHEN source_count = 0 THEN 'stale'
      WHEN d.status = 'proposed' THEN 'reinforced'
      ELSE d.status
    END,
    d.invalidated_reason = CASE
      WHEN source_count = 0 THEN 'all source references are missing'
      ELSE d.invalidated_reason
    END
RETURN count(d) AS updated`, map[string]any{"now": now})
		if err != nil {
			return err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return err
		}
		if len(records) > 0 {
			count = intFromRow(records[0].AsMap(), "updated")
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("dream re-evaluate: %w", err)
	}
	return count, nil
}

func (s *service) updateDreamStatus(ctx context.Context, profileID, dreamID string, status domain.DreamStatus, reason string) error {
	if !status.IsValid() {
		return ErrInvalidDreamStatus
	}
	_, rows, err := s.deps.Graph.ScopedRead(ctx, profileID, `
MATCH (d:Dream {team_id: $profileId, dream_id: $dreamId})
RETURN d.dream_id AS dream_id
LIMIT 1`, map[string]any{"dreamId": dreamID})
	if err != nil {
		return fmt.Errorf("dream status existence check: %w", err)
	}
	if len(rows) == 0 {
		return ErrDreamNotFound
	}
	now := s.now().UTC()
	return s.deps.Graph.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		res, err := neo4jstorage.RunScoped(ctx, tx, profileID, `
MATCH (d:Dream {team_id: $profileId, dream_id: $dreamId})
SET d.status = $status,
    d.updated_at = $now,
    d.last_evaluated_at = CASE WHEN $status IN ['reinforced','stale','rejected'] THEN $now ELSE d.last_evaluated_at END,
    d.invalidated_reason = CASE WHEN $reason <> '' THEN $reason ELSE d.invalidated_reason END`, map[string]any{
			"dreamId": dreamID,
			"status":  string(status),
			"reason":  reason,
			"now":     now,
		})
		if err != nil {
			return err
		}
		_, err = res.Consume(ctx)
		return err
	})
}

func (s *service) linkDreamPromotion(ctx context.Context, profileID, dreamID, fragmentID string) error {
	return s.deps.Graph.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		res, err := neo4jstorage.RunScoped(ctx, tx, profileID, `
MATCH (d:Dream {team_id: $profileId, dream_id: $dreamId})
MATCH (sf:SourceFragment {team_id: $profileId, fragment_id: $fragmentId})
MERGE (d)-[r:PROMOTED_TO {team_id: $profileId, target_type: 'fragment', target_id: $fragmentId}]->(sf)
ON CREATE SET r.created_at = $createdAt`, map[string]any{
			"dreamId":    dreamID,
			"fragmentId": fragmentID,
			"createdAt":  s.now().UTC(),
		})
		if err != nil {
			return err
		}
		_, err = res.Consume(ctx)
		return err
	})
}

func (s *service) recordRun(ctx context.Context, profileID string, run *RunCycleResult) error {
	if s.deps.Graph == nil || run == nil || run.RunID == "" {
		return nil
	}
	return s.deps.Graph.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		res, err := neo4jstorage.RunScoped(ctx, tx, profileID, `
MERGE (r:DreamCycleRun {team_id: $profileId, run_id: $runId})
SET r.run_date = $runDate,
    r.started_at = $startedAt,
    r.completed_at = $completedAt,
    r.status = $status,
    r.error = $error,
    r.reflect_ran = $reflectRan,
    r.reevaluate_ran = $reevaluateRan,
    r.dream_ran = $dreamRan,
    r.stale_facts = $staleFacts,
    r.candidate_claims = $candidateClaims,
    r.disputed_claims = $disputedClaims,
    r.clarifications = $clarifications,
    r.reevaluated_dreams = $reevaluatedDreams,
    r.created_dreams = $createdDreams`, map[string]any{
			"runId":             run.RunID,
			"runDate":           run.RunDate,
			"startedAt":         run.StartedAt,
			"completedAt":       run.CompletedAt,
			"status":            run.Status,
			"error":             run.Error,
			"reflectRan":        run.ReflectRan,
			"reevaluateRan":     run.ReevaluateRan,
			"dreamRan":          run.DreamRan,
			"staleFacts":        run.StaleFacts,
			"candidateClaims":   run.CandidateClaims,
			"disputedClaims":    run.DisputedClaims,
			"clarifications":    run.Clarifications,
			"reevaluatedDreams": run.ReevaluatedDreams,
			"createdDreams":     run.CreatedDreams,
		})
		if err != nil {
			return err
		}
		_, err = res.Consume(ctx)
		return err
	})
}

func (s *service) cycleRunExists(ctx context.Context, profileID, runDate string) (bool, error) {
	if s.deps.Graph == nil {
		return false, nil
	}
	_, rows, err := s.deps.Graph.ScopedRead(ctx, profileID, `
MATCH (r:DreamCycleRun {team_id: $profileId, run_date: $runDate})
RETURN r.run_id AS run_id
LIMIT 1`, map[string]any{"runDate": runDate})
	if err != nil {
		return false, fmt.Errorf("dreaming cycle existing run check: %w", err)
	}
	return len(rows) > 0, nil
}

func (s *service) latestRun(ctx context.Context, profileID string) (*RunCycleResult, error) {
	if s.deps.Graph == nil {
		return nil, nil
	}
	_, rows, err := s.deps.Graph.ScopedRead(ctx, profileID, `
MATCH (r:DreamCycleRun {team_id: $profileId})
RETURN r
ORDER BY r.started_at DESC
LIMIT 1`, nil)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return runCycleResultFromRow(rows[0], profileID), nil
}

func runCycleResultFromRow(row map[string]any, profileID string) *RunCycleResult {
	node, ok := row["r"].(neo4j.Node)
	if !ok {
		return nil
	}
	props := node.Props
	return &RunCycleResult{
		RunID:             stringFromMap(props, "run_id"),
		ProfileID:         profileID,
		RunDate:           stringFromMap(props, "run_date"),
		StartedAt:         timeFromMap(props, "started_at"),
		CompletedAt:       timeFromMap(props, "completed_at"),
		ReflectRan:        boolFromMap(props, "reflect_ran"),
		ReevaluateRan:     boolFromMap(props, "reevaluate_ran"),
		DreamRan:          boolFromMap(props, "dream_ran"),
		StaleFacts:        intFromRow(props, "stale_facts"),
		CandidateClaims:   intFromRow(props, "candidate_claims"),
		DisputedClaims:    intFromRow(props, "disputed_claims"),
		Clarifications:    intFromRow(props, "clarifications"),
		ReevaluatedDreams: intFromRow(props, "reevaluated_dreams"),
		CreatedDreams:     intFromRow(props, "created_dreams"),
		Status:            stringFromMap(props, "status"),
		Error:             stringFromMap(props, "error"),
	}
}

func dreamFromRow(row map[string]any) (*domain.Dream, error) {
	node, ok := row["d"].(neo4j.Node)
	if !ok {
		return nil, nil
	}
	props := node.Props
	sourceRefs := []domain.DreamSourceRef{}
	if raw := stringFromMap(props, "source_refs_json"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &sourceRefs)
	}
	dream := &domain.Dream{
		DreamID:           stringFromMap(props, "dream_id"),
		ProfileID:         stringFromMap(props, "team_id"),
		Hypothesis:        stringFromMap(props, "hypothesis"),
		WhatIf:            stringFromMap(props, "what_if"),
		PossibleOutcome:   stringFromMap(props, "possible_outcome"),
		Rationale:         stringFromMap(props, "rationale"),
		Likelihood:        floatFromMap(props, "likelihood"),
		Confidence:        floatFromMap(props, "confidence"),
		Status:            domain.DreamStatus(stringFromMap(props, "status")),
		Cycle:             stringFromMap(props, "cycle"),
		CycleRunID:        stringFromMap(props, "cycle_run_id"),
		GeneratorModel:    stringFromMap(props, "generator_model"),
		ContentHash:       stringFromMap(props, "content_hash"),
		SourceRefs:        sourceRefs,
		InvalidatedReason: stringFromMap(props, "invalidated_reason"),
		CreatedAt:         timeFromMap(props, "created_at"),
		UpdatedAt:         timeFromMap(props, "updated_at"),
	}
	if t := timeFromMap(props, "last_evaluated_at"); !t.IsZero() {
		dream.LastEvaluatedAt = &t
	}
	return dream, nil
}

func sourceLabelAndID(sourceType string) (string, string, error) {
	switch sourceType {
	case "fact":
		return "Fact", "fact_id", nil
	case "claim":
		return "Claim", "claim_id", nil
	case "fragment":
		return "SourceFragment", "fragment_id", nil
	case "community":
		return "Community", "community_id", nil
	case "dream":
		return "Dream", "dream_id", nil
	default:
		return "", "", fmt.Errorf("unsupported dream source type %q", sourceType)
	}
}

func dreamContentHash(d *domain.Dream) string {
	sourceRefs := append([]domain.DreamSourceRef(nil), d.SourceRefs...)
	data, _ := json.Marshal(sourceRefs)
	h := sha256.Sum256([]byte(strings.Join([]string{
		d.Hypothesis,
		d.WhatIf,
		d.PossibleOutcome,
		string(data),
	}, "\x00")))
	return hex.EncodeToString(h[:])
}

func dreamPromotionContent(d *domain.Dream, feedback string) string {
	parts := []string{
		"Human confirmed a Dense-Mem dream hypothesis.",
		"Hypothesis: " + d.Hypothesis,
	}
	if d.WhatIf != "" {
		parts = append(parts, "What-if: "+d.WhatIf)
	}
	if d.PossibleOutcome != "" {
		parts = append(parts, "Possible outcome: "+d.PossibleOutcome)
	}
	if strings.TrimSpace(feedback) != "" {
		parts = append(parts, "Human feedback: "+strings.TrimSpace(feedback))
	}
	return strings.Join(parts, "\n")
}

func localRunDate(now time.Time, cfg EffectiveConfig) string {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return now.In(loc).Format("2006-01-02")
}

func parseProfileID(profileID string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(profileID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("dreaming config: invalid profile id: %w", err)
	}
	return parsed, nil
}

func stringFromRow(row map[string]any, key string) string {
	return stringFromMap(row, key)
}

func stringFromMap(row map[string]any, key string) string {
	switch v := row[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

func boolFromMap(row map[string]any, key string) bool {
	v, _ := row[key].(bool)
	return v
}

func intFromRow(row map[string]any, key string) int {
	switch v := row[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func floatFromMap(row map[string]any, key string) float64 {
	switch v := row[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int64:
		return float64(v)
	case int:
		return float64(v)
	default:
		return 0
	}
}

func timeFromMap(row map[string]any, key string) time.Time {
	switch v := row[key].(type) {
	case time.Time:
		return v
	default:
		return time.Time{}
	}
}
