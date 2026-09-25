//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	requestctx "github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/search/contract"
)

const (
	readPerformanceBenchmarkDocumentCount      = 96
	readPerformanceBenchmarkMeasuredIterations = 200
)

type readPerformanceBenchmarkFixture struct {
	store        *searchFixtureStore
	teamID       string
	annTeamID    string
	ownerID      string
	annOwnerID   string
	privateTeam  string
	privateCtx   context.Context
	privateActor requestctx.Actor
	privateID    string
	entityID     string
	knownAt      time.Time
	counters     *readPerformanceBenchmarkCounters
}

func BenchmarkRecallReadPipeline(b *testing.B) {
	fixture := newReadPerformanceBenchmarkFixture(b)
	workloads := []struct {
		name      string
		operation observability.ReadOperation
		stage     observability.ReadStage
		run       func(context.Context) (any, error)
	}{
		{
			name: "lexical", operation: observability.ReadOperationEvidenceRecall, stage: observability.ReadStageFullText,
			run: func(ctx context.Context) (any, error) {
				return fixture.store.RecallEvidence(ctx, RecallEvidenceInput{
					TeamID: fixture.teamID, Query: "recall benchmark lexical marker", Limit: 10,
				})
			},
		},
		{
			name: "exact_vector", operation: observability.ReadOperationVectorSearch, stage: observability.ReadStageVector,
			run: func(ctx context.Context) (any, error) {
				return fixture.store.read.SearchExactVector(ctx, contract.ExactVectorSearchInput{
					TeamID: fixture.teamID, QueryEmbedding: []float32{1, 0, 0}, Limit: 10,
				})
			},
		},
		{
			name: "ann", operation: observability.ReadOperationEvidenceRecall, stage: observability.ReadStageVector,
			run: func(ctx context.Context) (any, error) {
				return fixture.store.RecallEvidence(ctx, RecallEvidenceInput{
					TeamID: fixture.annTeamID, Query: "zzbenchmarkannlexicalmiss", QueryEmbedding: []float32{1, 0, 0}, Limit: 10,
				})
			},
		},
		{
			name: "expansion", operation: observability.ReadOperationEvidenceRecall, stage: observability.ReadStageExpansion,
			run: func(ctx context.Context) (any, error) {
				return fixture.store.RecallEvidence(ctx, RecallEvidenceInput{
					TeamID: fixture.teamID, ExpandFromEntityIDs: []string{fixture.entityID}, Limit: 10,
				})
			},
		},
		{
			name: "historical", operation: observability.ReadOperationEvidenceRecall, stage: observability.ReadStageFullText,
			run: func(ctx context.Context) (any, error) {
				return fixture.store.RecallEvidence(ctx, RecallEvidenceInput{
					TeamID: fixture.teamID, Query: "recall benchmark lexical marker", KnownAt: &fixture.knownAt, Limit: 10,
				})
			},
		},
		{
			name: "private_space", operation: observability.ReadOperationEvidenceRecall, stage: observability.ReadStageFullText,
			run: func(ctx context.Context) (any, error) {
				privateCtx := requestctx.WithActor(ctx, fixture.privateActor)
				return fixture.store.RecallEvidence(privateCtx, RecallEvidenceInput{
					TeamID: fixture.privateTeam, Query: "private benchmark marker", Limit: 5,
					SpaceID: fixture.privateID, SpaceKind: string(domain.MemorySpaceCredentialPrivate),
				})
			},
		},
		{
			name: "relationship_recall", operation: observability.ReadOperationRelationshipRecall, stage: observability.ReadStageFullText,
			run: func(ctx context.Context) (any, error) {
				return fixture.store.RecallRelationships(ctx, RecallRelationshipsInput{
					TeamID: fixture.teamID, Query: "relationship benchmark", Limit: 10,
				})
			},
		},
	}

	for _, workload := range workloads {
		b.Run(workload.name, func(b *testing.B) {
			var resultSignature string
			for slotIndex, slot := range []string{"pair_a", "pair_b"} {
				measuredRuns := 0
				b.Run(slot, func(b *testing.B) {
					enabled := (measuredRuns+slotIndex)%2 != 0
					var metrics observability.DiscoverabilityMetrics
					var prometheusMetrics *observability.PrometheusMetrics
					if enabled {
						prometheusMetrics = observability.NewPrometheusMetrics()
						metrics = prometheusMetrics
					}
					ctx := observability.WithReadPerformance(context.Background(), metrics)
					for range 20 {
						if _, err := workload.run(ctx); err != nil {
							b.Fatalf("warmup read failed: %v", err)
						}
					}

					fixture.counters.reset()
					results := make([]any, b.N)
					durations := make([]time.Duration, b.N)
					b.ReportAllocs()
					b.ResetTimer()
					for index := 0; index < b.N; index++ {
						started := time.Now()
						result, err := workload.run(ctx)
						durations[index] = time.Since(started)
						if err != nil {
							b.Fatalf("measured read failed: %v", err)
						}
						results[index] = result
					}
					b.StopTimer()

					signature := readPerformanceBenchmarkSignature(b, results)
					if resultSignature == "" {
						resultSignature = signature
					} else if signature != resultSignature {
						b.Fatalf("instrumentation changed %s results", workload.name)
					}
					sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
					b.ReportMetric(float64(readPerformancePercentile(durations, 0.50).Nanoseconds()), "p50-ns/op")
					b.ReportMetric(float64(readPerformancePercentile(durations, 0.95).Nanoseconds()), "p95-ns/op")
					counts := fixture.counters.snapshot()
					if counts.statements == 0 || counts.transactions == 0 || counts.commits+counts.rollbacks != counts.transactions {
						b.Fatalf("incomplete database counters for %s: %+v", workload.name, counts)
					}
					iterations := float64(b.N)
					b.ReportMetric(float64(counts.statements)/iterations, "sql-statements/op")
					b.ReportMetric(float64(counts.transactions)/iterations, "transactions/op")
					b.ReportMetric(float64(counts.commits+counts.rollbacks)/iterations, "transaction-completions/op")
					if enabled {
						b.ReportMetric(1, "telemetry-enabled/op")
					} else {
						b.ReportMetric(0, "telemetry-enabled/op")
					}
					if enabled {
						recorder := httptest.NewRecorder()
						prometheusMetrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
						expected := fmt.Sprintf(
							`densemem_read_stage_duration_seconds_count{operation="%s",outcome="success",stage="%s"`,
							workload.operation, workload.stage,
						)
						if !strings.Contains(recorder.Body.String(), expected) {
							b.Fatalf("enabled %s benchmark emitted no %s/%s duration metric", workload.name, workload.operation, workload.stage)
						}
					}
					if b.N == readPerformanceBenchmarkMeasuredIterations {
						measuredRuns++
					}
				})
			}
		})
	}
}

func newReadPerformanceBenchmarkFixture(b *testing.B) *readPerformanceBenchmarkFixture {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(b)
	b.Cleanup(cleanup)

	counters := &readPerformanceBenchmarkCounters{}
	countedDB := newReadPerformanceCountedDB(appDB, counters)
	store := newReadPerformanceSearchFixtureStore(countedDB, rls)
	ledger := knowledgepostgres.NewStore(countedDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	semantic := knowledgepostgres.NewStore(countedDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	baseCtx := context.Background()
	teamID := createLedgerTeam(b, adminDB, rls, "recall-read-performance")
	ownerID := createLedgerProfile(b, adminDB, rls, teamID, "recall-read-performance")
	annTeamID := createLedgerTeam(b, adminDB, rls, "recall-read-performance-ann")
	annOwnerID := createLedgerProfile(b, adminDB, rls, annTeamID, "recall-read-performance-ann")
	privateTeam := createLedgerTeam(b, adminDB, rls, "recall-read-performance-private")
	insertSearchTestContractWithFallback(b, adminDB, rls, "recall-read-performance", 3, "vector_hnsw", "densemem_read_performance_hnsw", true)

	baseDocuments := createReadPerformanceBenchmarkDocuments(b, baseCtx, store, ledger, teamID, ownerID, "recall benchmark lexical marker", readPerformanceBenchmarkDocumentCount, "", 0)
	annDocuments := createReadPerformanceBenchmarkDocuments(b, baseCtx, store, ledger, annTeamID, annOwnerID, "ann benchmark marker", readPerformanceBenchmarkDocumentCount, "", 0)

	subject := createSemanticEntity(b, baseCtx, semantic, teamID, ownerID, "person", "Benchmark Reader")
	object := createSemanticEntity(b, baseCtx, semantic, teamID, ownerID, "project", "Dense Mem")
	decision := applySemanticDecision(b, baseCtx, semantic, knowledgepostgres.ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: baseDocuments.ingestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: object.EntityID,
		Support: &knowledgepostgres.EvidenceSupportInput{
			FragmentID: baseDocuments.fragments[0].FragmentID, SourceGroupKey: "recall:benchmark-support",
			SpanStart: 0, SpanEnd: len(baseDocuments.fragments[0].Content), Authority: "primary",
		},
	})
	require.NotNil(b, decision.Relationship)
	relationshipDoc, err := store.UpsertSearchDocument(baseCtx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship",
		SourceID: decision.Relationship.RelationshipID, SourceVersion: int64(decision.Relationship.Version), ProjectionFormat: 2,
		DocumentText: "relationship benchmark subject: Benchmark Reader predicate: works on object: Dense Mem polarity: positive",
	})
	require.NoError(b, err)

	privateCredential := &domain.Credential{
		ID: uuid.New(), TeamID: uuid.MustParse(privateTeam), Name: "read performance private fixture",
		KeyHash: "hash-read-performance-private", KeyPrefix: "read-performance-private", KeySuffix: "suffix",
		Scopes: []string{"read", "write"}, MemoryBinding: domain.CredentialBindingCredentialPrivate,
	}
	require.NoError(b, accesspostgres.NewCredentialRepository(adminDB, rls, nil).CreateCredential(baseCtx, privateCredential))
	privateAccess := []domain.MemorySpaceAccess{{ID: privateCredential.MemorySpaceID, Kind: domain.MemorySpaceCredentialPrivate}}
	privateActor := requestctx.Actor{
		TeamID: privateCredential.TeamID, IdentityID: privateCredential.ActorIdentityID,
		MembershipID: privateCredential.MembershipID, OwnerID: privateCredential.OwnerID,
		CredentialID: &privateCredential.ID, AuthMethod: "api_key", AllowedSpaces: privateAccess,
	}
	privateCtx := requestctx.WithActor(baseCtx, privateActor)
	privateDocuments := createReadPerformanceBenchmarkDocuments(
		b, privateCtx, store, ledger, privateTeam, privateCredential.OwnerID.String(),
		"private benchmark marker", 8, privateCredential.MemorySpaceID.String(), privateCredential.MemorySpaceGeneration,
	)

	baseVectors := baseDocuments.documentIDs
	baseVectors[relationshipDoc.SearchDocumentID] = []float32{1, 0, 0}
	completeSearchDocumentsForTest(b, store, teamID, baseVectors)
	completeSearchDocumentsForTest(b, store, annTeamID, annDocuments.documentIDs)
	completeSearchDocumentsForTest(b, store, privateTeam, privateDocuments.documentIDs)
	require.NoError(b, rls.WithSystemTx(baseCtx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec("CREATE INDEX densemem_read_performance_hnsw ON search_documents USING hnsw ((embedding::vector(3)) vector_cosine_ops) WITH (m = 16, ef_construction = 64)").Error; err != nil {
			return err
		}
		return tx.Exec("ANALYZE search_documents, evidence_fragments").Error
	}))

	return &readPerformanceBenchmarkFixture{
		store: store, teamID: teamID, ownerID: ownerID, annTeamID: annTeamID, annOwnerID: annOwnerID,
		privateTeam: privateTeam, privateCtx: privateCtx, privateActor: privateActor, privateID: privateCredential.MemorySpaceID.String(),
		entityID: subject.EntityID, knownAt: time.Now().Add(time.Minute), counters: counters,
	}
}

type readPerformanceBenchmarkDocuments struct {
	ingestID    string
	fragments   []knowledgepostgres.EvidenceFragment
	documentIDs map[string][]float32
}

func createReadPerformanceBenchmarkDocuments(
	t testing.TB,
	ctx context.Context,
	store *searchFixtureStore,
	ledger *knowledgepostgres.Store,
	teamID, ownerID, prefix string,
	count int,
	spaceID string,
	spaceGeneration int64,
) readPerformanceBenchmarkDocuments {
	t.Helper()
	evidence := make([]knowledgepostgres.EvidenceInput, count)
	for index := range evidence {
		content := fmt.Sprintf("%s deterministic evidence record %04d", prefix, index)
		if index == 0 && prefix == "recall benchmark lexical marker" {
			content = "Benchmark Reader works on Dense Mem. " + content
		}
		evidence[index] = knowledgepostgres.EvidenceInput{Content: content, SourceType: "document"}
	}
	ingest, err := ledger.CreateIngestForTest(ctx, knowledgepostgres.CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: spaceID, SpaceGeneration: spaceGeneration,
		IdempotencyKey: prefix, RequestHash: sha256Hex(prefix), Evidence: evidence,
	})
	require.NoError(t, err)
	documents := make(map[string][]float32, len(ingest.Evidence))
	for index, fragment := range ingest.Evidence {
		document, err := store.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
			TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragment.FragmentID,
			SourceVersion: 1, DocumentText: fragment.Content, SpaceID: spaceID, SpaceGeneration: spaceGeneration,
		})
		require.NoError(t, err)
		documents[document.SearchDocumentID] = readPerformanceVector(index)
	}
	return readPerformanceBenchmarkDocuments{ingestID: ingest.IngestID, fragments: ingest.Evidence, documentIDs: documents}
}

func readPerformanceVector(index int) []float32 {
	return []float32{1, float32(index+1) / 1000, float32(index%17+1) / 1000}
}

type readPerformanceBenchmarkCounters struct {
	statements   atomic.Int64
	transactions atomic.Int64
	commits      atomic.Int64
	rollbacks    atomic.Int64
}

type readPerformanceBenchmarkCount struct {
	statements   int64
	transactions int64
	commits      int64
	rollbacks    int64
}

func newReadPerformanceCountedDB(db *gorm.DB, counters *readPerformanceBenchmarkCounters) *gorm.DB {
	countedDB := db.Session(&gorm.Session{})
	pool := &readPerformanceBenchmarkConnPool{ConnPool: db.ConnPool, counters: counters}
	countedDB.ConnPool = pool
	countedDB.Statement.ConnPool = pool
	return countedDB
}

func (c *readPerformanceBenchmarkCounters) reset() {
	c.statements.Store(0)
	c.transactions.Store(0)
	c.commits.Store(0)
	c.rollbacks.Store(0)
}

func (c *readPerformanceBenchmarkCounters) snapshot() readPerformanceBenchmarkCount {
	return readPerformanceBenchmarkCount{
		statements: c.statements.Load(), transactions: c.transactions.Load(),
		commits: c.commits.Load(), rollbacks: c.rollbacks.Load(),
	}
}

type readPerformanceBenchmarkConnPool struct {
	gorm.ConnPool
	counters *readPerformanceBenchmarkCounters
}

func (pool *readPerformanceBenchmarkConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return pool.ConnPool.PrepareContext(ctx, query)
}

func (pool *readPerformanceBenchmarkConnPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	pool.counters.statements.Add(1)
	return pool.ConnPool.ExecContext(ctx, query, args...)
}

func (pool *readPerformanceBenchmarkConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	pool.counters.statements.Add(1)
	return pool.ConnPool.QueryContext(ctx, query, args...)
}

func (pool *readPerformanceBenchmarkConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	pool.counters.statements.Add(1)
	return pool.ConnPool.QueryRowContext(ctx, query, args...)
}

func (pool *readPerformanceBenchmarkConnPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var tx gorm.ConnPool
	var err error
	if beginner, ok := pool.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else if beginner, ok := pool.ConnPool.(gorm.ConnPoolBeginner); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else {
		return nil, fmt.Errorf("read performance benchmark: connection pool cannot begin a transaction")
	}
	if err != nil {
		return nil, err
	}
	pool.counters.transactions.Add(1)
	return &readPerformanceBenchmarkTx{ConnPool: tx, counters: pool.counters}, nil
}

type readPerformanceBenchmarkTx struct {
	gorm.ConnPool
	counters *readPerformanceBenchmarkCounters
}

func (tx *readPerformanceBenchmarkTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.counters.statements.Add(1)
	return tx.ConnPool.ExecContext(ctx, query, args...)
}

func (tx *readPerformanceBenchmarkTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.counters.statements.Add(1)
	return tx.ConnPool.QueryContext(ctx, query, args...)
}

func (tx *readPerformanceBenchmarkTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx.counters.statements.Add(1)
	return tx.ConnPool.QueryRowContext(ctx, query, args...)
}

func (tx *readPerformanceBenchmarkTx) Commit() error {
	err := tx.ConnPool.(gorm.TxCommitter).Commit()
	tx.counters.commits.Add(1)
	return err
}

func (tx *readPerformanceBenchmarkTx) Rollback() error {
	err := tx.ConnPool.(gorm.TxCommitter).Rollback()
	tx.counters.rollbacks.Add(1)
	return err
}

func readPerformanceBenchmarkSignature(b *testing.B, results []any) string {
	b.Helper()
	var first string
	for index, result := range results {
		payload, err := json.Marshal(result)
		if err != nil {
			b.Fatalf("marshal deterministic read result: %v", err)
		}
		sampleHash := sha256.Sum256(payload)
		signature := hex.EncodeToString(sampleHash[:])
		if index == 0 {
			first = signature
		} else if signature != first {
			b.Fatal("read results changed across identical benchmark iterations")
		}
	}
	return first
}

func readPerformancePercentile(durations []time.Duration, fraction float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	index := int(math.Ceil(fraction*float64(len(durations)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(durations) {
		index = len(durations) - 1
	}
	return durations[index]
}
