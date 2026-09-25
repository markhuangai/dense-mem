//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	recallservice "github.com/markhuangai/dense-mem/internal/recall"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/search/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func newReadPerformanceSearchFixtureStore(db *gorm.DB, rls storagepostgres.RLSHelper) *searchFixtureStore {
	store := newSearchFixtureStore(db, rls)
	store.recall.relationshipConflicts = func(
		ctx context.Context,
		tx *gorm.DB,
		teamID string,
		knownAt *time.Time,
		results []RecallEvidenceHit,
	) ([]RelationshipConflictCaseRecord, error) {
		relationshipIDs := make([]string, 0)
		for _, result := range results {
			relationshipIDs = append(relationshipIDs, result.RelationshipIDs...)
		}
		return conflictpostgres.LoadRelationshipConflictRecordsInSpace(ctx, tx, teamID, relationshipIDs, knownAt, "")
	}
	return store
}

func TestRecallReadPerformanceMetricsMatchDisabledResults(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-read-performance-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-read-performance-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-read-performance", 3, "exact", "")
	ledgerRepo := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	searchRepo := newReadPerformanceSearchFixtureStore(appDB, rls)
	content := "recall read performance PostgreSQL fixture"
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, "recall-read-performance", content)
	document, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence",
		SourceID: ingest.Evidence[0].FragmentID, SourceVersion: 1, DocumentText: ingest.Evidence[0].Content,
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, searchRepo, teamID, map[string][]float32{
		document.SearchDocumentID: {1, 0, 0},
	})

	input := RecallEvidenceInput{TeamID: teamID, Query: "recall read performance fixture", Limit: 5}
	disabled, err := searchRepo.RecallEvidence(ctx, input)
	require.NoError(t, err)
	require.Len(t, disabled.Results, 1)

	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	measuredCtx := observability.WithReadPerformance(ctx, metrics)
	enabled, err := searchRepo.RecallEvidence(measuredCtx, input)
	require.NoError(t, err)
	require.Equal(t, disabled, enabled)

	readiness, err := searchRepo.CheckSearchReadiness(measuredCtx)
	require.NoError(t, err)
	require.NotNil(t, readiness)
	fullTextHits, err := searchRepo.SearchFullText(measuredCtx, contract.FullTextSearchInput{
		TeamID: teamID,
		Query:  "recall read performance fixture",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Len(t, fullTextHits, 1)
	vectorHits, err := searchRepo.SearchExactVector(measuredCtx, contract.ExactVectorSearchInput{
		TeamID:         teamID,
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          5,
	})
	require.NoError(t, err)
	require.Len(t, vectorHits, 1)

	empty, err := searchRepo.RecallEvidence(measuredCtx, RecallEvidenceInput{
		TeamID: teamID,
		Query:  "read-performance-no-matching-document",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Empty(t, empty.Results)

	cancelledCtx, cancel := context.WithCancel(measuredCtx)
	cancel()
	_, err = searchRepo.RecallEvidence(cancelledCtx, input)
	require.Error(t, err)
	_, err = searchRepo.SearchFullText(measuredCtx, contract.FullTextSearchInput{TeamID: teamID})
	require.Error(t, err)

	requireReadStageSample(t, metrics, observability.ReadOperationEvidenceRecall, observability.ReadStageFullText, observability.ReadOutcomeSuccess, 1)
	requireReadStageSample(t, metrics, observability.ReadOperationEvidenceRecall, observability.ReadStageHydration, observability.ReadOutcomeSuccess, 1)
	requireReadStageSample(t, metrics, observability.ReadOperationEvidenceRecall, observability.ReadStageSelection, observability.ReadOutcomeSuccess, 1)
	requireReadStageSample(t, metrics, observability.ReadOperationEvidenceRecall, observability.ReadStageConflicts, observability.ReadOutcomeSuccess, 0)
	requireReadStageSample(t, metrics, observability.ReadOperationEvidenceRecall, observability.ReadStageFusion, observability.ReadOutcomeSuccess, 0)
	requireReadStageSample(t, metrics, observability.ReadOperationSearchReadiness, observability.ReadStageReadiness, observability.ReadOutcomeSuccess, 1)
	requireReadStageSample(t, metrics, observability.ReadOperationFullTextSearch, observability.ReadStageFullText, observability.ReadOutcomeSuccess, 1)
	requireReadStageSample(t, metrics, observability.ReadOperationVectorSearch, observability.ReadStageVector, observability.ReadOutcomeSuccess, 1)
	requireReadStageSample(t, metrics, observability.ReadOperationSearchContract, observability.ReadStageContract, observability.ReadOutcomeSuccess, 1)
	requireReadStageSample(t, metrics, observability.ReadOperationSearchContract, observability.ReadStageContract, observability.ReadOutcomeCancellation, 0)
	requireReadStageSample(t, metrics, observability.ReadOperationFullTextSearch, observability.ReadStageTotal, observability.ReadOutcomeError, 0)
	requireReadStageSample(t, metrics, observability.ReadOperationEvidenceRecall, observability.ReadStageFullText, observability.ReadOutcomeSuccess, 0)
	requireReadStageSample(t, metrics, observability.ReadOperationEvidenceRecall, observability.ReadStageTotal, observability.ReadOutcomeSuccess, 0)
	require.Greater(t, metrics.ReadSQLStatementCount(observability.ReadOperationEvidenceRecall, observability.ReadStageFullText), 0)
	require.Greater(t, metrics.ReadSQLStatementCount(observability.ReadOperationEvidenceRecall, observability.ReadStageTransactionSetup), 0)
}

func TestRecallReadPerformanceMetricsRemainRequestScoped(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-read-performance-concurrent-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-read-performance-concurrent-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-read-performance-concurrent", 3, "exact", "")
	searchRepo := newReadPerformanceSearchFixtureStore(appDB, rls)
	document, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: uuid.NewString(),
		SourceVersion: 1, DocumentText: "concurrent read performance marker",
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, searchRepo, teamID, map[string][]float32{document.SearchDocumentID: {1, 0, 0}})

	textMetrics := observability.NewInMemoryDiscoverabilityMetrics()
	vectorMetrics := observability.NewInMemoryDiscoverabilityMetrics()
	textCtx := observability.WithReadPerformance(ctx, textMetrics)
	vectorCtx := observability.WithReadPerformance(ctx, vectorMetrics)
	start := make(chan struct{})
	type readResult struct {
		hits []contract.SearchHit
		err  error
	}
	textResult := make(chan readResult, 1)
	vectorResult := make(chan readResult, 1)
	go func() {
		<-start
		hits, err := searchRepo.SearchFullText(textCtx, contract.FullTextSearchInput{
			TeamID: teamID, Query: "concurrent read performance marker", Limit: 5,
		})
		textResult <- readResult{hits: hits, err: err}
	}()
	go func() {
		<-start
		hits, err := searchRepo.SearchExactVector(vectorCtx, contract.ExactVectorSearchInput{
			TeamID: teamID, QueryEmbedding: []float32{1, 0, 0}, Limit: 5,
		})
		vectorResult <- readResult{hits: hits, err: err}
	}()
	close(start)
	textRead := <-textResult
	vectorRead := <-vectorResult
	require.NoError(t, textRead.err)
	require.NoError(t, vectorRead.err)
	require.Len(t, textRead.hits, 1)
	require.Len(t, vectorRead.hits, 1)

	requireReadStageSample(t, textMetrics, observability.ReadOperationFullTextSearch, observability.ReadStageFullText, observability.ReadOutcomeSuccess, 1)
	requireReadStageAbsent(t, textMetrics, observability.ReadOperationVectorSearch, observability.ReadStageVector)
	requireReadStageSample(t, vectorMetrics, observability.ReadOperationVectorSearch, observability.ReadStageVector, observability.ReadOutcomeSuccess, 1)
	requireReadStageAbsent(t, vectorMetrics, observability.ReadOperationFullTextSearch, observability.ReadStageFullText)
	require.Greater(t, textMetrics.ReadSQLStatementCount(observability.ReadOperationFullTextSearch, observability.ReadStageFullText), 0)
	require.Zero(t, textMetrics.ReadSQLStatementCount(observability.ReadOperationVectorSearch, observability.ReadStageVector))
	require.Greater(t, vectorMetrics.ReadSQLStatementCount(observability.ReadOperationVectorSearch, observability.ReadStageVector), 0)
	require.Zero(t, vectorMetrics.ReadSQLStatementCount(observability.ReadOperationFullTextSearch, observability.ReadStageFullText))
}

func TestRecallReadBenchmarkCountsTransactionStatements(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	teamID := createLedgerTeam(t, adminDB, rls, "read-performance-transaction-count")
	counters := &readPerformanceBenchmarkCounters{}
	countedDB := newReadPerformanceCountedDB(appDB, counters)
	var value int
	err := rls.WithTeamTx(context.Background(), countedDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw("SELECT 1").Scan(&value).Error
	})
	require.NoError(t, err)
	require.Equal(t, 1, value)
	counts := counters.snapshot()
	require.Equal(t, int64(5), counts.statements)
	require.Equal(t, int64(1), counts.transactions)
	require.Equal(t, int64(1), counts.commits)
	require.Zero(t, counts.rollbacks)
}

func TestRecallReadPerformanceMetricsCountMultiSpaceStagesOnce(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "read-performance-multi-space")
	sharedOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "read-performance-shared-owner")
	insertSearchTestContract(t, adminDB, rls, "read-performance-multi-space", 3, "exact", "")
	searchRepo := newReadPerformanceSearchFixtureStore(appDB, rls)
	ledgerRepo := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	createReadPerformanceBenchmarkDocuments(t, ctx, searchRepo, ledgerRepo, teamID, sharedOwnerID,
		"multispace performance marker shared", 1, "", 0)

	credential := &domain.Credential{
		ID: uuid.New(), TeamID: uuid.MustParse(teamID), Name: "read performance private space",
		KeyHash: "hash-read-performance-multispace", KeyPrefix: "dm_" + uuid.NewString()[:20], KeySuffix: "suffix",
		Scopes: []string{"read", "write"}, MemoryBinding: domain.CredentialBindingCredentialPrivate,
	}
	require.NoError(t, accesspostgres.NewCredentialRepository(adminDB, rls, nil).CreateCredential(ctx, credential))
	sharedSpace, err := privacypostgres.NewMemorySpaceRepository(appDB, rls).GetTeamShared(ctx, credential.TeamID)
	require.NoError(t, err)
	require.NotNil(t, sharedSpace)
	actor := requestctx.Actor{
		TeamID: credential.TeamID, IdentityID: credential.ActorIdentityID,
		MembershipID: credential.MembershipID, OwnerID: credential.OwnerID,
		CredentialID: &credential.ID, AuthMethod: "api_key",
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedSpace.ID, Kind: domain.MemorySpaceTeamShared},
			{ID: credential.MemorySpaceID, Kind: domain.MemorySpaceCredentialPrivate},
		},
	}
	actorCtx := requestctx.WithActor(ctx, actor)
	createReadPerformanceBenchmarkDocuments(t, actorCtx, searchRepo, ledgerRepo,
		teamID, credential.OwnerID.String(), "multispace performance marker private", 1,
		credential.MemorySpaceID.String(), credential.MemorySpaceGeneration)

	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	service := recallservice.NewRecallService(recallservice.RecallDependencies{Search: searchRepo, Metrics: metrics})
	relationshipLimit := 0
	result, err := service.Recall(actorCtx, recallservice.RecallRequest{
		Query: "multispace performance marker", Limit: 10, RelationshipLimit: &relationshipLimit,
	})
	require.NoError(t, err)
	require.Len(t, result.Results, 2)
	spaceKinds := make(map[string]int)
	for _, item := range result.Results {
		spaceKinds[item.SpaceKind]++
	}
	require.Equal(t, map[string]int{
		string(domain.MemorySpaceTeamShared): 1, string(domain.MemorySpaceCredentialPrivate): 1,
	}, spaceKinds)

	stageCounts := make(map[observability.ReadStage]int)
	for _, sample := range metrics.ReadStageSamples() {
		if sample.Operation != observability.ReadOperationEvidenceRecall {
			continue
		}
		require.Equal(t, observability.ReadOutcomeSuccess, sample.Outcome)
		stageCounts[sample.Stage]++
		if sample.Stage == observability.ReadStageTransactionSetup || sample.Stage == observability.ReadStageConflicts {
			require.Zero(t, sample.Items)
		} else {
			require.Equal(t, 1, sample.Items)
		}
	}
	require.Equal(t, map[observability.ReadStage]int{
		observability.ReadStageTotal: 2, observability.ReadStageFullText: 2,
		observability.ReadStageFusion: 2, observability.ReadStageHydration: 2,
		observability.ReadStageSelection: 2, observability.ReadStageConflicts: 2,
		observability.ReadStageTransactionSetup: 6,
	}, stageCounts)
	require.Equal(t, 2, metrics.ReadSQLStatementCount(observability.ReadOperationEvidenceRecall, observability.ReadStageFullText))
	require.Equal(t, 24, metrics.ReadSQLStatementCount(observability.ReadOperationEvidenceRecall, observability.ReadStageTransactionSetup))
	require.Equal(t, 2, metrics.ReadSQLStatementCount(observability.ReadOperationEvidenceRecall, observability.ReadStageTotal))
	require.Equal(t, 3, metrics.ReadSQLStatementCount(observability.ReadOperationSearchContract, observability.ReadStageContract))
}

func requireReadStageSample(t testing.TB, metrics *observability.InMemoryDiscoverabilityMetrics, operation observability.ReadOperation, stage observability.ReadStage, outcome observability.ReadOutcome, items int) {
	t.Helper()
	for _, sample := range metrics.ReadStageSamples() {
		if sample.Operation == operation && sample.Stage == stage && sample.Outcome == outcome && sample.Items == items {
			return
		}
	}
	require.FailNow(t, "matching read-stage observation was not recorded", "%s/%s outcome=%s items=%d", operation, stage, outcome, items)
}

func requireReadStageAbsent(t testing.TB, metrics *observability.InMemoryDiscoverabilityMetrics, operation observability.ReadOperation, stage observability.ReadStage) {
	t.Helper()
	for _, sample := range metrics.ReadStageSamples() {
		if sample.Operation == operation && sample.Stage == stage {
			require.FailNow(t, "unexpected read-stage observation", "%s/%s outcome=%s items=%d", operation, stage, sample.Outcome, sample.Items)
		}
	}
}
