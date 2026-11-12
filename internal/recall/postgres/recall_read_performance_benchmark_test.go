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
	"strings"

	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

const (
	readPerformanceBenchmarkDocumentCount      = 96
	readPerformanceBenchmarkMeasuredIterations = 200
)

func readPerformanceFixedID(role string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("dense-mem:issue-458:"+role)).String()
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
	fixed ...bool,
) readPerformanceBenchmarkDocuments {
	t.Helper()
	evidence := make([]knowledgepostgres.EvidenceInput, count)
	for index := range evidence {
		content := fmt.Sprintf("%s deterministic evidence record %04d", prefix, index)
		if index == 0 && prefix == "recall benchmark lexical marker" {
			content = "Benchmark Reader works on Dense Mem. Relationship support evidence record 0000"
		}
		evidence[index] = knowledgepostgres.EvidenceInput{Content: content, SourceType: "document"}
		if len(fixed) > 0 && fixed[0] {
			evidence[index].FragmentID = readPerformanceFixedID(fmt.Sprintf("%s:%04d", prefix, index))
		}
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
	capture      *recallQueryCapture
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

func (c *readPerformanceBenchmarkCounters) record(query string, args []any) {
	if c.capture != nil {
		c.capture.add(query, args)
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
	pool.counters.record(query, args)
	return pool.ConnPool.ExecContext(ctx, query, args...)
}

func (pool *readPerformanceBenchmarkConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	pool.counters.statements.Add(1)
	pool.counters.record(query, args)
	return pool.ConnPool.QueryContext(ctx, query, args...)
}

func (pool *readPerformanceBenchmarkConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	pool.counters.statements.Add(1)
	pool.counters.record(query, args)
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
	tx.counters.record(query, args)
	return tx.ConnPool.ExecContext(ctx, query, args...)
}

func (tx *readPerformanceBenchmarkTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.counters.statements.Add(1)
	tx.counters.record(query, args)
	return tx.ConnPool.QueryContext(ctx, query, args...)
}

func (tx *readPerformanceBenchmarkTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx.counters.statements.Add(1)
	tx.counters.record(query, args)
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

type recallQueryStatement struct {
	SQL     string   `json:"sql"`
	Args    []string `json:"args"`
	rawSQL  string
	rawArgs []any
}

type recallQueryCapture struct {
	Statements []recallQueryStatement `json:"statements"`
}

func (c *recallQueryCapture) add(query string, args []any) {
	statement := recallQueryStatement{
		SQL: strings.Join(strings.Fields(query), " "), Args: make([]string, len(args)),
		rawSQL: query, rawArgs: append([]any(nil), args...),
	}
	for index, arg := range args {
		statement.Args[index] = fmt.Sprintf("%T:%v", arg, arg)
	}
	c.Statements = append(c.Statements, statement)
}
