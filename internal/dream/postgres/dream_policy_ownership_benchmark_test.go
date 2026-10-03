//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

const dreamPolicyBenchmarkWarmups = 20
const dreamPolicyBenchmarkMeasured = 200

type dreamPolicyBenchmarkCounters struct {
	statements   atomic.Int64
	transactions atomic.Int64
	completions  atomic.Int64
}

func (c *dreamPolicyBenchmarkCounters) reset() {
	c.statements.Store(0)
	c.transactions.Store(0)
	c.completions.Store(0)
}

type dreamPolicyBenchmarkConnPool struct {
	gorm.ConnPool
	counters *dreamPolicyBenchmarkCounters
}

func (p *dreamPolicyBenchmarkConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.ConnPool.PrepareContext(ctx, query)
}

func (p *dreamPolicyBenchmarkConnPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	p.counters.statements.Add(1)
	return p.ConnPool.ExecContext(ctx, query, args...)
}

func (p *dreamPolicyBenchmarkConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	p.counters.statements.Add(1)
	return p.ConnPool.QueryContext(ctx, query, args...)
}

func (p *dreamPolicyBenchmarkConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	p.counters.statements.Add(1)
	return p.ConnPool.QueryRowContext(ctx, query, args...)
}

func (p *dreamPolicyBenchmarkConnPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var tx gorm.ConnPool
	var err error
	if beginner, ok := p.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else if beginner, ok := p.ConnPool.(gorm.ConnPoolBeginner); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else {
		return nil, fmt.Errorf("dream policy benchmark: connection pool cannot begin a transaction")
	}
	if err != nil {
		return nil, err
	}
	p.counters.transactions.Add(1)
	return &dreamPolicyBenchmarkTx{ConnPool: tx, counters: p.counters}, nil
}

type dreamPolicyBenchmarkTx struct {
	gorm.ConnPool
	counters *dreamPolicyBenchmarkCounters
}

func (tx *dreamPolicyBenchmarkTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.counters.statements.Add(1)
	return tx.ConnPool.ExecContext(ctx, query, args...)
}

func (tx *dreamPolicyBenchmarkTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.counters.statements.Add(1)
	return tx.ConnPool.QueryContext(ctx, query, args...)
}

func (tx *dreamPolicyBenchmarkTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx.counters.statements.Add(1)
	return tx.ConnPool.QueryRowContext(ctx, query, args...)
}

func (tx *dreamPolicyBenchmarkTx) Commit() error {
	err := tx.ConnPool.(gorm.TxCommitter).Commit()
	if err == nil {
		tx.counters.completions.Add(1)
	}
	return err
}

func (tx *dreamPolicyBenchmarkTx) Rollback() error {
	err := tx.ConnPool.(gorm.TxCommitter).Rollback()
	if err == nil {
		tx.counters.completions.Add(1)
	}
	return err
}

func dreamPolicyCountedDB(db *gorm.DB, counters *dreamPolicyBenchmarkCounters) *gorm.DB {
	counted := db.Session(&gorm.Session{})
	pool := &dreamPolicyBenchmarkConnPool{ConnPool: db.ConnPool, counters: counters}
	counted.ConnPool = pool
	counted.Statement.ConnPool = pool
	return counted
}

type dreamPolicyBenchmarkFixture struct {
	store    *Store
	counters *dreamPolicyBenchmarkCounters
	graph    []UpsertHypothesisInput
	feedback UpdateHypothesisStatusInput
}

func newDreamPolicyBenchmarkFixture(b *testing.B) *dreamPolicyBenchmarkFixture {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(b)
	b.Cleanup(cleanup)
	ctx := context.Background()
	teamID := createLedgerTeam(b, adminDB, rls, "dream-policy-benchmark")
	ownerID := createLedgerProfile(b, adminDB, rls, teamID, "dream-policy-benchmark-owner")
	reviewerID := createLedgerProfile(b, adminDB, rls, teamID, "dream-policy-benchmark-reviewer")
	semantic := newDreamFixtureStore(appDB, rls)
	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	subject := createSemanticEntity(b, ctx, semantic, teamID, ownerID, "project", "Benchmark policy owner")
	middle := createSemanticEntity(b, ctx, semantic, teamID, ownerID, "product", "Benchmark middle")
	sourceObject := createSemanticEntity(b, ctx, semantic, teamID, ownerID, "product", "Benchmark source object")
	first := createActiveDreamRelationship(b, ctx, ledger, semantic, teamID, ownerID,
		"dream-policy-bench-first", "Benchmark owner uses middle.", subject.EntityID, middle.EntityID, "source:first")
	second := createActiveDreamRelationship(b, ctx, ledger, semantic, teamID, ownerID,
		"dream-policy-bench-second", "Benchmark middle uses source object.", middle.EntityID, sourceObject.EntityID, "source:second")
	inputs, err := semantic.ListDreamInputs(ctx, DreamInputListInput{TeamID: teamID, Limit: 10})
	require.NoError(b, err)
	firstInput := requireDreamInput(b, inputs, first.Relationship.RelationshipID)
	secondInput := requireDreamInput(b, inputs, second.Relationship.RelationshipID)
	graphRun, err := semantic.ClaimDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: teamID, InitiatedByProfileID: ownerID, RunDate: "2026-09-29",
		WindowKey: "manual:dream-policy-benchmark", LeaseToken: uuid.NewString(),
		LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(b, err)
	fixture := &dreamPolicyBenchmarkFixture{
		graph:    make([]UpsertHypothesisInput, dreamPolicyBenchmarkWarmups+dreamPolicyBenchmarkMeasured),
		counters: &dreamPolicyBenchmarkCounters{},
	}
	for i := range fixture.graph {
		graphObject := createSemanticEntity(b, ctx, semantic, teamID, ownerID, "product", fmt.Sprintf("Benchmark graph target %03d", i))
		statement := fmt.Sprintf("Benchmark owner may use graph target %03d.", i)
		fixture.graph[i] = evidenceGroundedDreamProposal(teamID, ownerID, graphRun.RunID,
			firstInput, secondInput, subject.EntityID, graphObject.EntityID, "uses", statement)
		fixture.graph[i].ContentHash = sha256Hex(statement)

	}
	feedbackObject := createSemanticEntity(b, ctx, semantic, teamID, ownerID, "product", "Benchmark feedback target")
	record, inserted, err := semantic.UpsertScheduledHypothesis(ctx, UpsertHypothesisInput{
		TeamID: teamID, RunID: graphRun.RunID, Statement: "Benchmark feedback hypothesis.",
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", PredicateVersion: 1,
		ObjectEntityID: feedbackObject.EntityID, SourceVersions: map[string]int{firstInput.RelationshipID: firstInput.Version},
		ContentHash: sha256Hex("Benchmark feedback hypothesis."), GeneratorKind: "evaluation_seed",
	})
	require.NoError(b, err)
	require.True(b, inserted)
	fixture.feedback = UpdateHypothesisStatusInput{
		TeamID: teamID, ActorProfileID: reviewerID, HypothesisID: record.HypothesisID,
		Status: "reinforced", Decision: "reinforce",
	}
	fixture.store = NewStore(dreamPolicyCountedDB(appDB, fixture.counters), rls)
	return fixture
}

func benchmarkDreamPolicyWorkload(b *testing.B, counters *dreamPolicyBenchmarkCounters, run func(int) (string, error)) {
	if b.N != dreamPolicyBenchmarkMeasured {
		b.StopTimer()
		return
	}
	b.StopTimer()
	for i := 0; i < dreamPolicyBenchmarkWarmups; i++ {
		if _, err := run(i); err != nil {
			b.Fatalf("warmup %d: %v", i, err)
		}
	}
	counters.reset()
	durations := make([]time.Duration, b.N)
	var signature string
	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		started := time.Now()
		next, err := run(dreamPolicyBenchmarkWarmups + i)
		durations[i] = time.Since(started)
		if err != nil {
			b.Fatalf("operation %d: %v", i, err)
		}
		if i == 0 {
			signature = next
		} else if next != signature {
			b.Fatalf("result signature changed: %q != %q", next, signature)
		}
	}
	b.StopTimer()
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	statements := counters.statements.Load()
	transactions := counters.transactions.Load()
	completions := counters.completions.Load()
	if statements == 0 || transactions == 0 || transactions != completions {
		b.Fatalf("incomplete database counts: statements=%d transactions=%d completions=%d", statements, transactions, completions)
	}
	b.ReportMetric(float64(durations[(len(durations)-1)/2].Nanoseconds()), "p50-ns/op")
	b.ReportMetric(float64(durations[(len(durations)*95+99)/100-1].Nanoseconds()), "p95-ns/op")
	b.ReportMetric(float64(statements)/float64(b.N), "sql-statements/op")
	b.ReportMetric(float64(transactions)/float64(b.N), "transactions/op")
	b.ReportMetric(float64(completions)/float64(b.N), "transaction-completions/op")
	b.ReportMetric(0, "provider-calls/op")
	b.Logf("result_signature=%s", signature)
}

func BenchmarkDreamPolicyOwnership(b *testing.B) {
	fixture := newDreamPolicyBenchmarkFixture(b)
	ctx := context.Background()
	b.Run("graph_proposal", func(b *testing.B) {
		benchmarkDreamPolicyWorkload(b, fixture.counters, func(i int) (string, error) {
			record, inserted, err := fixture.store.UpsertHypothesis(ctx, fixture.graph[i])
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("graph|%s|%t", record.Status, inserted), nil
		})
	})
	b.Run("feedback", func(b *testing.B) {
		benchmarkDreamPolicyWorkload(b, fixture.counters, func(int) (string, error) {
			record, err := fixture.store.UpdateHypothesisStatus(ctx, fixture.feedback)
			if err != nil {
				return "", err
			}
			return "feedback|" + record.Status, nil
		})
	})
}
