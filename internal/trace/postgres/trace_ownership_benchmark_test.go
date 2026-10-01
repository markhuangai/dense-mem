//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	traceapp "github.com/markhuangai/dense-mem/internal/trace"
)

type traceOwnershipCounters struct {
	statements, transactions, commits, rollbacks int
	capture                                      hash.Hash
	identities                                   *strings.Replacer
}

func (c *traceOwnershipCounters) record(query string, args []any) {
	c.statements++
	if c.capture == nil {
		return
	}
	fmt.Fprintln(c.capture, strings.Join(strings.Fields(query), " "))
	for _, arg := range args {
		fmt.Fprintln(c.capture, c.identities.Replace(fmt.Sprintf("%T:%v", arg, arg)))
	}
}

type traceOwnershipPool struct {
	gorm.ConnPool
	counters *traceOwnershipCounters
}

func (p *traceOwnershipPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	p.counters.record(query, args)
	return p.ConnPool.ExecContext(ctx, query, args...)
}

func (p *traceOwnershipPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	p.counters.record(query, args)
	return p.ConnPool.QueryContext(ctx, query, args...)
}

func (p *traceOwnershipPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	p.counters.record(query, args)
	return p.ConnPool.QueryRowContext(ctx, query, args...)
}

func (p *traceOwnershipPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var tx gorm.ConnPool
	var err error
	if beginner, ok := p.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else if beginner, ok := p.ConnPool.(gorm.ConnPoolBeginner); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else {
		return nil, fmt.Errorf("trace benchmark pool cannot begin a transaction")
	}
	if err != nil {
		return nil, err
	}
	p.counters.transactions++
	return &traceOwnershipTx{ConnPool: tx, counters: p.counters}, nil
}

type traceOwnershipTx struct {
	gorm.ConnPool
	counters *traceOwnershipCounters
}

func (tx *traceOwnershipTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.counters.record(query, args)
	return tx.ConnPool.ExecContext(ctx, query, args...)
}

func (tx *traceOwnershipTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.counters.record(query, args)
	return tx.ConnPool.QueryContext(ctx, query, args...)
}

func (tx *traceOwnershipTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx.counters.record(query, args)
	return tx.ConnPool.QueryRowContext(ctx, query, args...)
}

func (tx *traceOwnershipTx) Commit() error {
	err := tx.ConnPool.(gorm.TxCommitter).Commit()
	if err == nil {
		tx.counters.commits++
	}
	return err
}

func (tx *traceOwnershipTx) Rollback() error {
	err := tx.ConnPool.(gorm.TxCommitter).Rollback()
	if err == nil {
		tx.counters.rollbacks++
	}
	return err
}

func BenchmarkTraceOwnership(b *testing.B) {
	f := newTraceOwnershipFixture(b)
	originalPool, originalStatementPool := f.db.ConnPool, f.db.Statement.ConnPool
	for _, workload := range []struct {
		name                string
		viaService, maximum bool
	}{
		{"adapter_default", false, false}, {"adapter_maximum", false, true},
		{"service_default", true, false}, {"service_maximum", true, true},
	} {
		b.Run(workload.name, func(b *testing.B) {
			counters := &traceOwnershipCounters{identities: f.identities}
			countedDB := f.db.Session(&gorm.Session{Context: f.ctx})
			pool := &traceOwnershipPool{ConnPool: originalPool, counters: counters}
			countedDB.ConnPool, countedDB.Statement.ConnPool = pool, pool
			require.True(b, f.db.ConnPool == originalPool && f.db.Statement.ConnPool == originalStatementPool)
			store := newTraceStoreForTest(countedDB, f.rls)
			service := traceapp.NewSemantic(store)
			input := TraceRelationshipInput{TeamID: f.teamID, RelationshipID: traceOwnershipID(2, 1)}
			edges, events, runes := 24, 100, 2000
			if workload.maximum {
				input.MaxDepth, input.MaxEdges = 99, 999
				edges = 100
				if !workload.viaService {
					input.MaxEvents, input.MaxFragmentContentRunes = 999, 99999
					events, runes = 500, 8000
				}
			}
			run := func() (any, error) {
				if workload.viaService {
					result, err := service.Trace(f.ctx, "", traceapp.TraceRequest{
						RelationshipID: input.RelationshipID, MaxDepth: input.MaxDepth, MaxEdges: input.MaxEdges,
					})
					if err != nil {
						return nil, err
					}
					return result.Semantic, nil
				}
				return store.TraceRelationship(f.ctx, input)
			}
			for range 20 {
				result, err := run()
				require.NoError(b, err)
				assertTraceOwnershipBenchmarkResult(b, result, edges, events, runes)
			}
			capture := sha256.New()
			*counters = traceOwnershipCounters{capture: capture, identities: f.identities}
			durations := make([]time.Duration, b.N)
			var signature string
			b.ReportAllocs()
			b.ResetTimer()
			b.StopTimer()
			for i := 0; i < b.N; i++ {
				if i == 1 {
					counters.capture = nil
				}
				b.StartTimer()
				started := time.Now()
				result, err := run()
				durations[i] = time.Since(started)
				b.StopTimer()
				require.NoError(b, err)
				assertTraceOwnershipBenchmarkResult(b, result, edges, events, runes)
				actual, err := f.signature(result)
				require.NoError(b, err)
				if i == 0 {
					signature = actual
				}
				require.Equal(b, signature, actual)
			}
			require.Positive(b, counters.statements)
			require.Equal(b, b.N, counters.transactions)
			require.Equal(b, b.N, counters.commits)
			require.Zero(b, counters.rollbacks)
			require.True(b, f.db.ConnPool == originalPool && f.db.Statement.ConnPool == originalStatementPool)
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			b.ReportMetric(float64(durations[(len(durations)-1)/2].Nanoseconds()), "p50-ns/op")
			b.ReportMetric(float64(durations[(len(durations)*95+99)/100-1].Nanoseconds()), "p95-ns/op")
			b.ReportMetric(float64(counters.statements)/float64(b.N), "sql-statements/op")
			b.ReportMetric(float64(counters.transactions)/float64(b.N), "transactions/op")
			b.ReportMetric(0, "provider-calls/op")
			b.Logf("result_signature=%s", signature)
			b.Logf("query_contract_sha256=%s", hex.EncodeToString(capture.Sum(nil)))
		})
	}
}

func assertTraceOwnershipBenchmarkResult(t testing.TB, value any, edges, events, runes int) {
	t.Helper()
	var actualEdges, actualEvents int
	var evidence []TraceEvidenceFragment
	var truncated bool
	var reason string
	switch result := value.(type) {
	case *RelationshipTraceResult:
		actualEdges, actualEvents = len(result.SemanticEdges), len(result.VerificationEvents)
		evidence, truncated, reason = result.EvidenceFragments, result.Truncated, result.StoppedReason
	case *traceapp.SemanticTrace:
		actualEdges, actualEvents = len(result.SemanticEdges), len(result.VerificationEvents)
		evidence, truncated, reason = result.Evidence, result.Truncated, result.StoppedReason
	default:
		t.Fatalf("unexpected trace benchmark result %T", value)
	}
	require.Equal(t, edges, actualEdges)
	require.Equal(t, events, actualEvents)
	require.Len(t, evidence, 1)
	require.Len(t, []rune(evidence[0].Content), runes)
	require.True(t, evidence[0].ContentTruncated)
	require.True(t, truncated)
	require.Equal(t, "max_edges", reason)
}
