package serverapp

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"gorm.io/gorm"
)

type searchApplication struct {
	Repository        searchcontract.SearchRepository
	Contract          *searchmaintenance.EnsureActiveSearchContractResult
	EmbeddingProvider embeddingcontract.EmbeddingProviderInterface
	RetryEmbedding    embeddingcontract.EmbeddingProviderInterface
	Convergence       searchapp.SearchConvergenceReader
	Reconciliation    searchapp.SearchReconciliationService
}

func buildSearchRepositoryApplication(
	startupCtx context.Context,
	cfg config.Config,
	db *postgres.DB,
	rls *postgres.RLS,
	knowledgeOwner *knowledgepostgres.Store,
) (*searchpostgres.Store, *searchmaintenance.EnsureActiveSearchContractResult, error) {
	searchOwner := searchpostgres.NewStore(db.GetDB(), rls)
	if searchOwner == nil || knowledgeOwner == nil {
		return nil, nil, errors.New("search: native maintenance store is required")
	}
	contract, err := searchOwner.EnsureActiveSearchContract(startupCtx, searchapp.EnsureActiveSearchContractInput{
		Provider:   "openai",
		Model:      cfg.GetAIEmbeddingModel(),
		Dimensions: cfg.GetAIEmbeddingDimensions(),
	})
	if err != nil {
		return nil, nil, err
	}
	return searchOwner, contract, nil
}

func searchRecallConflictReader() recallpostgres.RelationshipConflictReader {
	return func(ctx context.Context, tx *gorm.DB, teamID string, knownAt *time.Time, results []recallcontract.RecallEvidenceHit) ([]tracecontract.RelationshipConflictCaseRecord, error) {
		relationshipIDs := make([]string, 0)
		seen := make(map[string]struct{})
		for _, hit := range results {
			for _, relationshipID := range hit.RelationshipIDs {
				if _, ok := seen[relationshipID]; ok {
					continue
				}
				seen[relationshipID] = struct{}{}
				relationshipIDs = append(relationshipIDs, relationshipID)
			}
		}
		conflicts, err := conflictpostgres.LoadRelationshipConflictRecordsInSpace(ctx, tx, teamID, relationshipIDs, knownAt, "")
		if err != nil {
			return nil, err
		}
		filtered := make([]tracecontract.RelationshipConflictCaseRecord, 0, len(conflicts))
		seenConflicts := make(map[string]struct{})
		for _, conflict := range conflicts {
			if conflict.Status != string(domain.RelationshipConflictOpen) &&
				conflict.Status != string(domain.RelationshipConflictOverdue) &&
				conflict.Status != string(domain.RelationshipConflictResolved) {
				continue
			}
			if _, ok := seenConflicts[conflict.ConflictID]; ok {
				continue
			}
			seenConflicts[conflict.ConflictID] = struct{}{}
			filtered = append(filtered, conflict)
		}
		return filtered, nil
	}
}

func buildSearchProviders(
	cfg config.Config,
	searchOwner *searchpostgres.Store,
	contract *searchmaintenance.EnsureActiveSearchContractResult,
	projection knowledgecontract.SearchProjectionRepository,
	metrics observability.DiscoverabilityMetrics,
	logger observability.LogProvider,
) *searchApplication {
	openaiProvider := embedding.NewOpenAIEmbeddingProvider(&cfg, nil)
	openaiProvider.SetMetrics(metrics)
	retryEmbedder := embedding.NewRetryEmbeddingProviderWithKey(openaiProvider, logger, cfg.GetAIAPIKey())
	retryEmbedder.SetMetrics(metrics)
	return &searchApplication{
		Repository:        searchOwner,
		Contract:          contract,
		EmbeddingProvider: openaiProvider,
		RetryEmbedding:    retryEmbedder,
		Convergence:       searchapp.NewSearchConvergenceService(searchOwner, projection),
		Reconciliation: searchapp.NewSearchReconciliationService(searchapp.SearchReconciliationDependencies{
			Repository:      searchOwner,
			Projection:      projection,
			Provider:        openaiProvider,
			ProviderTimeout: time.Duration(cfg.GetAIEmbeddingTimeoutSeconds()) * time.Second,
		}),
	}
}
