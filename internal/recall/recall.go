package recall

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	"math"
	"strings"
	"time"
)

const (
	defaultRecallResultLimit = 10
	maxRecallResultLimit     = 50
)

var ErrRecallAuthContext = errors.New("recall: authenticated actor context is required")
var ErrInvalidInput = recallcontract.ErrInvalidInput
var ErrTopicUnavailable = recallcontract.ErrTopicUnavailable

type RecallService = recallcontract.Service
type RecallSearchRepository = recallcontract.SearchRepository
type RecallOntologyConfigProvider interface {
	OntologyMaintenanceRuntimeConfig(context.Context) (domain.OntologyMaintenanceConfig, error)
}
type RecallDependencies struct {
	Decisions           modelprovider.DecisionProvider
	DecisionSettings    modelprovider.DecisionSettings
	Logger              observability.LogProvider
	DiagnosticProtector observability.DiagnosticProtector
	Search              RecallSearchRepository
	Provider            embeddingcontract.EmbeddingProviderInterface
	OntologyConfig      RecallOntologyConfigProvider
	Metrics             observability.DiscoverabilityMetrics
}
type recallService struct {
	decisions           modelprovider.DecisionProvider
	decisionSettings    modelprovider.DecisionSettings
	logger              observability.LogProvider
	diagnosticProtector observability.DiagnosticProtector
	search              RecallSearchRepository
	retrieval           *Retrieval
	provider            embeddingcontract.EmbeddingProviderInterface
	ontologyConfig      RecallOntologyConfigProvider
	metrics             observability.DiscoverabilityMetrics
}

func NewRecallService(deps RecallDependencies) RecallService {
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &recallService{decisions: deps.Decisions, decisionSettings: deps.DecisionSettings, logger: deps.Logger, diagnosticProtector: deps.DiagnosticProtector, search: deps.Search,
		retrieval: &Retrieval{repository: deps.Search, decisions: deps.Decisions, logger: deps.Logger}, provider: deps.Provider, ontologyConfig: deps.OntologyConfig, metrics: metrics}
}

type RecallRequest = recallcontract.Request
type recallExecutionRequest struct {
	RecallRequest
	recallContract             *searchcontract.ActiveSearchContract
	recallEmbedding            []float32
	recallEmbeddingDegradation *RecallDegradationResult
	recallEmbeddingReady       bool
	organizationReady          bool
	organizationEnabled        bool
	organizationConfigDegraded bool
}

type RecallResult = recallcontract.RecallResult
type RecallSuggestedAction = recallcontract.RecallSuggestedAction
type RecallResultItem = recallcontract.RecallResultItem
type RelatedTopic = recallcontract.RelatedTopic
type RecallConflictSummary = recallcontract.RecallConflictSummary
type RecallDegradationResult = recallcontract.RecallDegradationResult
type RecallSearchStates = recallcontract.RecallSearchStates

func (s *recallService) Recall(ctx context.Context, req RecallRequest) (*RecallResult, error) {
	if err := validateRecallRequest(req); err != nil {
		return nil, fmt.Errorf("%w: %v", recallcontract.ErrInvalidInput, err)
	}
	req = normalizeRecallRequest(req)
	ctx = observability.WithReadPerformance(ctx, s.metrics)
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return nil, ErrRecallAuthContext
	}
	ctx, degradation, err := s.decisionContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.recallWithExecution(ctx, recallExecutionRequest{RecallRequest: req})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err == nil && result != nil && degradation != nil {
		result.Degradations = append(result.Degradations, *degradation)
		result.Degradation = &result.Degradations[0]
	}
	if err == nil && result != nil {
		if err = s.relatedTopics(ctx, req, result); err != nil {
			return nil, err
		}
		reconcileDecisionSelections(ctx, s.logger, result)
	}
	return result, err
}

func (s *recallService) recallWithExecution(ctx context.Context, req recallExecutionRequest) (result *RecallResult, err error) {
	if s.search == nil {
		return nil, errors.New("recall: search repository is required")
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return nil, ErrRecallAuthContext
	}
	if !req.organizationReady {
		req.organizationReady = true
		if s.ontologyConfig != nil {
			config, configErr := s.ontologyConfig.OntologyMaintenanceRuntimeConfig(ctx)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			req.organizationEnabled = configErr == nil && config.Enabled
			req.organizationConfigDegraded = configErr != nil
		}
	}
	if _, branchSelected := recallBranchFromContext(ctx); !branchSelected && len(actor.AllowedSpaces) > 1 {
		return s.recallAcrossSpaces(ctx, req, actor)
	}
	if _, selected := recallBranchFromContext(ctx); !selected && len(actor.AllowedSpaces) == 1 {
		ctx = context.WithValue(ctx, recallBranchContextKey{}, actor.AllowedSpaces[0])
	}
	branch, _ := recallBranchFromContext(ctx)
	spaceKind := branchKind(branch)
	teamSharedBranch := spaceKind == string(domain.MemorySpaceTeamShared)
	if !recallBranchMetricsSuppressed(ctx) {
		started := time.Now()
		defer func() {
			outcome := "ok"
			resultCount := 0
			if err != nil {
				outcome = "error"
			}
			if result != nil {
				resultCount = len(result.Results)
			}
			observability.RecordRecall(ctx, s.metrics, float64(time.Since(started).Microseconds())/1000, resultCount, outcome)
		}()
	}
	req.RecallRequest = normalizeRecallRequest(req.RecallRequest)
	contract := req.recallContract
	if contract == nil {
		contract, err = s.search.GetActiveSearchContract(ctx)
		if err != nil {
			return nil, err
		}
	}
	degradations := []RecallDegradationResult{}
	if teamSharedBranch && req.organizationConfigDegraded {
		degradations = append(degradations, organizationDegradation("evidence", "ontology_unavailable")...)
	}
	queryEmbedding := append([]float32(nil), req.recallEmbedding...)
	if req.recallEmbeddingReady {
		if req.recallEmbeddingDegradation != nil {
			degradations = append(degradations, *req.recallEmbeddingDegradation)
		}
	} else if req.Query != "" {
		embedCtx := observability.WithAIOperation(ctx, observability.AIOperationRecallEmbedding, 1)
		vector, vectorDegradation := s.queryEmbedding(embedCtx, contract, req.Query)
		queryEmbedding = vector
		if vectorDegradation != nil {
			degradations = append(degradations, *vectorDegradation)
		}
	}
	recalled, err := s.retrieval.RecallEvidence(ctx, recallcontract.RecallEvidenceInput{
		OrganizationEnabled: req.organizationEnabled && teamSharedBranch,
		TopicIDs:            req.TopicIDs,
		TeamID:              actor.TeamID.String(),
		Query:               req.Query,
		QueryEmbedding:      queryEmbedding,
		Limit:               req.Limit,
		ValidAt:             req.ValidAt,
		KnownAt:             req.KnownAt,
		KnownEvidenceIDs:    req.KnownEvidenceIDs,
		SpaceID:             branchID(branch),
		SpaceKind:           spaceKind,
	})
	if err != nil {
		return nil, err
	}
	if err := validateRecallConflictTeams(recalled, actor.TeamID.String()); err != nil {
		return nil, err
	}
	result = recallResultFromRepository(recalled, degradations)
	appendEvidenceVectorFailureDegradation(result, recalled.SearchState)
	if len(result.Degradations) > 0 {
		result.Degradation = &result.Degradations[0]
	}
	applyRecallSpaceKind(result, spaceKind)
	return result, nil
}

func (s *recallService) queryEmbedding(
	ctx context.Context,
	contract *searchcontract.ActiveSearchContract,
	query string,
) ([]float32, *RecallDegradationResult) {
	if s.provider == nil || !s.provider.IsAvailable() {
		return nil, &RecallDegradationResult{
			Frontier: "evidence",
			Optional: true,
			Code:     string(domain.ErrorProviderUnavailable),
			Message:  "vector recall is unavailable; full-text evidence recall was used",
		}
	}
	if got := s.provider.Dimensions(); got != 0 && got != contract.EmbeddingDimensions {
		return nil, &RecallDegradationResult{
			Frontier: "evidence",
			Optional: true,
			Code:     string(domain.ErrorProviderMalformed),
			Message:  "vector recall provider dimensions do not match the active search contract",
		}
	}
	if got := strings.TrimSpace(s.provider.ModelName()); got != "" && got != contract.EmbeddingModel {
		return nil, &RecallDegradationResult{
			Frontier: "evidence",
			Optional: true,
			Code:     string(domain.ErrorProviderMalformed),
			Message:  "vector recall provider model does not match the active search contract",
		}
	}
	vector, model, err := s.provider.Embed(ctx, query)
	if err != nil {
		return nil, &RecallDegradationResult{
			Frontier: "evidence",
			Optional: true,
			Code:     string(domain.ErrorProviderUnavailable),
			Message:  "vector recall provider failed; full-text evidence recall was used",
		}
	}
	if model != "" && model != contract.EmbeddingModel {
		return nil, &RecallDegradationResult{
			Frontier: "evidence",
			Optional: true,
			Code:     string(domain.ErrorProviderMalformed),
			Message:  "vector recall provider returned the wrong model",
		}
	}
	if err := validateRecallEmbedding(vector, contract.EmbeddingDimensions); err != nil {
		return nil, &RecallDegradationResult{
			Frontier: "evidence",
			Optional: true,
			Code:     string(domain.ErrorProviderMalformed),
			Message:  "vector recall provider returned an invalid embedding",
		}
	}
	return vector, nil
}

func normalizeRecallRequest(req RecallRequest) RecallRequest {
	req.Query = strings.TrimSpace(req.Query)
	if req.Limit <= 0 {
		req.Limit = defaultRecallResultLimit
	}
	if req.Limit > maxRecallResultLimit {
		req.Limit = maxRecallResultLimit
	}
	req.KnownEvidenceIDs = normalizeRecallRequestIDs(req.KnownEvidenceIDs)
	if req.TopicIDs != nil {
		req.TopicIDs = normalizeRecallRequestIDs(req.TopicIDs)
	}
	req.TopicLimit = normalizeRecallOptionalLimit(req.TopicLimit, 3, 10)
	return req
}

func normalizeRecallOptionalLimit(value *int, defaultValue int, maxValue int) *int {
	if value == nil {
		normalized := defaultValue
		return &normalized
	}
	normalized := *value
	if normalized < 0 {
		normalized = 0
	}
	if normalized > maxValue {
		normalized = maxValue
	}
	return &normalized
}

func recallOptionalLimitValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func normalizeRecallRequestIDs(values []string) []string {
	ids := domain.NormalizeReadIDList(values)
	for index, value := range ids {
		if id, err := uuid.Parse(value); err == nil {
			ids[index] = id.String()
		}
	}
	return domain.NormalizeReadIDList(ids)
}

func validateRecallEmbedding(vector []float32, dims int) error {
	if len(vector) != dims {
		return fmt.Errorf("embedding dimensions %d, expected %d", len(vector), dims)
	}
	for i, value := range vector {
		f := float64(value)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("embedding contains non-finite value at index %d", i)
		}
	}
	return nil
}

func recallResultFromRepository(
	recalled *recallcontract.RecallEvidenceResult,
	degradations []RecallDegradationResult,
) *RecallResult {
	searchState := string(domain.SearchProjectionCurrent)
	results := []RecallResultItem{}
	conflicts := []RecallConflictSummary{}
	if recalled != nil {
		searchState = recalled.SearchState
		degradations = append(degradations, recalled.Degradations...)
		results = make([]RecallResultItem, 0, len(recalled.Results))
		for _, item := range recalled.Results {
			results = append(results, RecallResultItem{
				ContextTruncated:      item.ContextTruncated,
				EvidenceID:            item.EvidenceID,
				EquivalentEvidenceIDs: append([]string{}, item.EquivalentEvidenceIDs...),
				EquivalentsTruncated:  item.EquivalentsTruncated,
				Rank:                  item.Rank,
				Context:               item.Context,
				Source:                item.Source,
				SourceType:            item.SourceType,
				CreatedAt:             recallCreatedAt(item.CreatedAt),
			})
		}
		conflicts = append(conflicts, recallEvidenceConflictSummaries(recalled.EvidenceConflicts)...)
		conflicts = limitRecallConflictSummaries(conflicts, 20)
	}
	result := &RecallResult{
		RecallID:      "rec_" + uuid.NewString(),
		RelatedTopics: []recallcontract.RelatedTopic{},
		Results:       results,
		Conflicts:     conflicts,
		SearchStates: RecallSearchStates{
			Evidence: searchState,
		},
		Degradations: append([]RecallDegradationResult(nil), degradations...),
		SearchState:  searchState,
	}
	if recalled != nil {
		result.DecisionStages = append([]modelprovider.DecisionStage(nil), recalled.DecisionStages...)
	}
	if len(result.Degradations) > 0 {
		result.Degradation = &result.Degradations[0]
	}
	return result
}

func recallCreatedAt(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	createdAt := value.UTC()
	return &createdAt
}

func recallResultEvidenceIDs(results []RecallResultItem) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, result := range results {
		for _, id := range append([]string{result.EvidenceID}, result.EquivalentEvidenceIDs...) {
			if id != "" && !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	return ids
}

func validateRecallRequest(req RecallRequest) error {
	if req.TopicIDs != nil {
		if len(req.TopicIDs) < 1 || len(req.TopicIDs) > 10 {
			return errors.New("topic_ids must contain between 1 and 10 unique UUIDs")
		}
		seen := map[uuid.UUID]bool{}
		for _, value := range req.TopicIDs {
			id, err := uuid.Parse(value)
			if err != nil || id == uuid.Nil || seen[id] {
				return errors.New("topic_ids must contain between 1 and 10 unique UUIDs")
			}
			seen[id] = true
		}
	}
	if req.TopicLimit != nil && (*req.TopicLimit < 0 || *req.TopicLimit > 10) {
		return errors.New("topic_limit must be between 0 and 10")
	}
	if strings.TrimSpace(req.Query) == "" && len(req.TopicIDs) == 0 {
		return errors.New("query or topic_ids is required")
	}
	return nil
}
