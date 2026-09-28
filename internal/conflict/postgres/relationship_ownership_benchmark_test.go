//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type conflictOwnershipBenchmarkCounters struct {
	statements   atomic.Int64
	transactions atomic.Int64
	commits      atomic.Int64
	rollbacks    atomic.Int64
	capture      *conflictOwnershipQueryCapture
}

var conflictOwnershipUUIDPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
var conflictOwnershipSnapshotHashPattern = regexp.MustCompile(`relationship-conflict-snapshot:rc:[0-9a-f]{64}`)

type conflictOwnershipQueryCapture struct {
	hash         hash.Hash
	statements   int
	fingerprints []string
}

func newConflictOwnershipQueryCapture() *conflictOwnershipQueryCapture {
	return &conflictOwnershipQueryCapture{hash: sha256.New()}
}

func (c *conflictOwnershipQueryCapture) add(query string, args []any) {
	c.statements++
	var normalized strings.Builder
	fmt.Fprintln(&normalized, strings.Join(strings.Fields(query), " "))
	for _, arg := range args {
		value := fmt.Sprintf("%T:%v", arg, arg)
		switch arg.(type) {
		case time.Time, *time.Time:
			value = fmt.Sprintf("%T:<time>", arg)
		}
		value = conflictOwnershipUUIDPattern.ReplaceAllString(value, "<uuid>")
		fmt.Fprintln(&normalized, conflictOwnershipSnapshotHashPattern.ReplaceAllString(value, "relationship-conflict-snapshot:rc:<hash>"))
	}
	statement := normalized.String()
	_, _ = c.hash.Write([]byte(statement))
	fingerprint := sha256.Sum256([]byte(statement))
	c.fingerprints = append(c.fingerprints, hex.EncodeToString(fingerprint[:]))
}

func (c *conflictOwnershipQueryCapture) digest() string {
	return hex.EncodeToString(c.hash.Sum(nil))
}

type conflictOwnershipBenchmarkCount struct {
	statements   int64
	transactions int64
	commits      int64
	rollbacks    int64
}

func newConflictOwnershipCountedDB(db *gorm.DB, counters *conflictOwnershipBenchmarkCounters) *gorm.DB {
	countedDB := db.Session(&gorm.Session{})
	pool := &conflictOwnershipBenchmarkConnPool{ConnPool: db.ConnPool, counters: counters}
	countedDB.ConnPool = pool
	countedDB.Statement.ConnPool = pool
	return countedDB
}

func (c *conflictOwnershipBenchmarkCounters) reset() {
	c.statements.Store(0)
	c.transactions.Store(0)
	c.commits.Store(0)
	c.rollbacks.Store(0)
}

func (c *conflictOwnershipBenchmarkCounters) snapshot() conflictOwnershipBenchmarkCount {
	return conflictOwnershipBenchmarkCount{
		statements: c.statements.Load(), transactions: c.transactions.Load(),
		commits: c.commits.Load(), rollbacks: c.rollbacks.Load(),
	}
}

func (c *conflictOwnershipBenchmarkCounters) record(query string, args []any) {
	if c.capture != nil {
		c.capture.add(query, args)
	}
}

type conflictOwnershipBenchmarkConnPool struct {
	gorm.ConnPool
	counters *conflictOwnershipBenchmarkCounters
}

func (pool *conflictOwnershipBenchmarkConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return pool.ConnPool.PrepareContext(ctx, query)
}

func (pool *conflictOwnershipBenchmarkConnPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	pool.counters.statements.Add(1)
	pool.counters.record(query, args)
	return pool.ConnPool.ExecContext(ctx, query, args...)
}

func (pool *conflictOwnershipBenchmarkConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	pool.counters.statements.Add(1)
	pool.counters.record(query, args)
	return pool.ConnPool.QueryContext(ctx, query, args...)
}

func (pool *conflictOwnershipBenchmarkConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	pool.counters.statements.Add(1)
	pool.counters.record(query, args)
	return pool.ConnPool.QueryRowContext(ctx, query, args...)
}

func (pool *conflictOwnershipBenchmarkConnPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var tx gorm.ConnPool
	var err error
	if beginner, ok := pool.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else if beginner, ok := pool.ConnPool.(gorm.ConnPoolBeginner); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else {
		return nil, fmt.Errorf("conflict ownership benchmark: connection pool cannot begin a transaction")
	}
	if err != nil {
		return nil, err
	}
	pool.counters.transactions.Add(1)
	return &conflictOwnershipBenchmarkTx{ConnPool: tx, counters: pool.counters}, nil
}

type conflictOwnershipBenchmarkTx struct {
	gorm.ConnPool
	counters *conflictOwnershipBenchmarkCounters
}

func (tx *conflictOwnershipBenchmarkTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.counters.statements.Add(1)
	tx.counters.record(query, args)
	return tx.ConnPool.ExecContext(ctx, query, args...)
}

func (tx *conflictOwnershipBenchmarkTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.counters.statements.Add(1)
	tx.counters.record(query, args)
	return tx.ConnPool.QueryContext(ctx, query, args...)
}

func (tx *conflictOwnershipBenchmarkTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx.counters.statements.Add(1)
	tx.counters.record(query, args)
	return tx.ConnPool.QueryRowContext(ctx, query, args...)
}

func (tx *conflictOwnershipBenchmarkTx) Commit() error {
	err := tx.ConnPool.(gorm.TxCommitter).Commit()
	if err == nil {
		tx.counters.commits.Add(1)
	}
	return err
}

func (tx *conflictOwnershipBenchmarkTx) Rollback() error {
	err := tx.ConnPool.(gorm.TxCommitter).Rollback()
	if err == nil {
		tx.counters.rollbacks.Add(1)
	}
	return err
}

type conflictOwnershipBenchmarkFixture struct {
	adminDB     *gorm.DB
	rls         *storagepostgres.RLS
	teamID      string
	ownerID     string
	conflictID  string
	knownAt     time.Time
	reviewRunID string
	store       *Store
	counters    *conflictOwnershipBenchmarkCounters
}

func newConflictOwnershipBenchmarkFixture(b *testing.B) *conflictOwnershipBenchmarkFixture {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(b)
	b.Cleanup(cleanup)
	ctx := context.Background()
	insertSearchTestContract(b, adminDB, rls, "conflict-ownership-benchmark", 3, "exact", "")
	teamID := createLedgerTeam(b, adminDB, rls, "conflict-ownership-benchmark-team")
	ownerA := createLedgerProfile(b, adminDB, rls, teamID, "benchmark-owner-a")
	ownerB := createLedgerProfile(b, adminDB, rls, teamID, "benchmark-owner-b")
	ownerC := createLedgerProfile(b, adminDB, rls, teamID, "benchmark-owner-c")
	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	const subjectID = "00000000-0000-4000-8000-000000000101"
	const preferredObjectID = "00000000-0000-4000-8000-000000000102"
	const otherObjectID = "00000000-0000-4000-8000-000000000103"
	// Fixed entity IDs keep position ordering identical across independent base and candidate databases.
	require.NoError(b, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, teamID); err != nil {
			return err
		}
		for _, entity := range []struct{ id, kind, name, normalized string }{
			{subjectID, "project", "Dense-Mem", "dense-mem"},
			{preferredObjectID, "product", "PostgreSQL", "postgresql"},
			{otherObjectID, "product", "GraphDB", "graphdb"},
		} {
			if err := tx.Exec(`INSERT INTO entity_records (team_id, entity_id, entity_kind) VALUES (?::uuid, ?::uuid, ?)`,
				teamID, entity.id, entity.kind).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO entity_names (team_id, entity_id, owner_profile_id, display_name, normalized_name, name_kind)
				VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?, 'canonical')`,
				teamID, entity.id, ownerA, entity.name, entity.normalized).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	first := commitConflictRememberFixture(b, ctx, ledger, teamID, ownerA, subjectID, preferredObjectID,
		"Dense-Mem uses PostgreSQL.", "benchmark-preferred")
	_ = commitConflictRememberFixture(b, ctx, ledger, teamID, ownerB, subjectID, otherObjectID,
		"Dense-Mem uses GraphDB.", "benchmark-other-b")
	_ = commitConflictRememberFixture(b, ctx, ledger, teamID, ownerC, subjectID, preferredObjectID,
		"Dense-Mem uses PostgreSQL for the team.", "benchmark-preferred")
	var conflictID string
	require.NoError(b, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT conflict_id::text FROM relationship_conflict_position_members
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid AND active LIMIT 1`,
			teamID, first.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&conflictID)
	}))
	require.NotEmpty(b, conflictID)
	counters := &conflictOwnershipBenchmarkCounters{}
	countedDB := newConflictOwnershipCountedDB(appDB, counters)
	countedLedger := knowledgepostgres.NewStore(countedDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	return &conflictOwnershipBenchmarkFixture{
		adminDB: adminDB, rls: rls, teamID: teamID, ownerID: ownerA, conflictID: conflictID,
		knownAt: time.Now().UTC().Add(time.Minute), reviewRunID: uuid.NewString(),
		store: NewStore(countedDB, rls, countedLedger), counters: counters,
	}
}

func (f *conflictOwnershipBenchmarkFixture) prepareReview(ctx context.Context) error {
	return f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE relationship_conflict_cases
			SET status = 'open', lease_worker_id = 'conflict-ownership-benchmark',
			last_review_run_id = ?::uuid, lease_until = clock_timestamp() + interval '1 hour',
			attempts = 0
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, f.reviewRunID, f.teamID, f.conflictID).Error
	})
}

func BenchmarkConflictOwnership(b *testing.B) {
	fixture := newConflictOwnershipBenchmarkFixture(b)
	ctx := context.Background()
	workloads := []struct {
		name    string
		prepare func(context.Context) error
		run     func(context.Context) (string, error)
	}{
		{
			name: "queue_projection",
			run: func(ctx context.Context) (string, error) {
				page, err := fixture.store.ListConflictQueue(ctx, domain.ConflictQueueQuery{TeamID: fixture.teamID, Limit: 10})
				if err != nil {
					return "", err
				}
				if len(page.Items) != 1 || len(page.Items[0].Positions) != 2 {
					return "", fmt.Errorf("queue projection changed shape: %+v", page)
				}
				item := page.Items[0]
				counts := []int{item.Positions[0].SupporterCount, item.Positions[1].SupporterCount}
				sort.Ints(counts)
				return fmt.Sprintf("%s|%d|%d|%d", item.Status, len(item.Positions), counts[0], counts[1]), nil
			},
		},
		{
			name: "historical_hydration",
			run: func(ctx context.Context) (string, error) {
				var signature string
				err := fixture.rls.WithTeamProfileTx(ctx, fixture.store.db, fixture.teamID, fixture.ownerID, func(tx *gorm.DB) error {
					records, err := LoadRelationshipConflictRecordsByID(ctx, tx, fixture.teamID, []string{fixture.conflictID}, &fixture.knownAt)
					if err != nil {
						return err
					}
					if len(records) != 1 || len(records[0].Positions) != 2 {
						return fmt.Errorf("historical projection changed shape: %+v", records)
					}
					record := records[0]
					counts := []int{record.Positions[0].SupporterCount, record.Positions[1].SupporterCount}
					sort.Ints(counts)
					signature = fmt.Sprintf("%s|%d|%d|%d", record.Status, len(record.Positions), counts[0], counts[1])
					return nil
				})
				return signature, err
			},
		},
		{
			name:    "deterministic_review",
			prepare: fixture.prepareReview,
			run: func(ctx context.Context) (string, error) {
				result, err := fixture.store.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
					TeamID: fixture.teamID, WorkerID: "conflict-ownership-benchmark",
					ReviewRunID: fixture.reviewRunID, ConflictID: fixture.conflictID, Now: time.Now().UTC(),
				})
				if err != nil {
					return "", err
				}
				return result.Outcome + "|" + result.Stage, nil
			},
		},
	}
	for _, workload := range workloads {
		b.Run(workload.name, func(b *testing.B) {
			for range 20 {
				if workload.prepare != nil {
					require.NoError(b, workload.prepare(ctx))
				}
				_, err := workload.run(ctx)
				require.NoError(b, err)
			}
			capture := newConflictOwnershipQueryCapture()
			fixture.counters.capture = capture
			if workload.prepare != nil {
				require.NoError(b, workload.prepare(ctx))
			}
			_, err := workload.run(ctx)
			require.NoError(b, err)
			fixture.counters.capture = nil
			fixture.counters.reset()
			durations := make([]time.Duration, b.N)
			var signature string
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				if workload.prepare != nil {
					b.StopTimer()
					require.NoError(b, workload.prepare(ctx))
					b.StartTimer()
				}
				started := time.Now()
				next, err := workload.run(ctx)
				durations[index] = time.Since(started)
				if err != nil {
					b.Fatalf("%s failed: %v", workload.name, err)
				}
				if index == 0 {
					signature = next
				} else if next != signature {
					b.Fatalf("%s result changed across iterations: %q != %q", workload.name, next, signature)
				}
			}
			b.StopTimer()
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			counts := fixture.counters.snapshot()
			if counts.statements == 0 || counts.transactions == 0 || counts.transactions != counts.commits+counts.rollbacks {
				b.Fatalf("%s database counts are incomplete: %+v", workload.name, counts)
			}
			b.ReportMetric(float64(durations[(len(durations)-1)/2].Nanoseconds()), "p50-ns/op")
			b.ReportMetric(float64(durations[(len(durations)*95+99)/100-1].Nanoseconds()), "p95-ns/op")
			b.ReportMetric(float64(counts.statements)/float64(b.N), "sql-statements/op")
			b.ReportMetric(float64(counts.transactions)/float64(b.N), "transactions/op")
			b.ReportMetric(float64(counts.commits+counts.rollbacks)/float64(b.N), "transaction-completions/op")
			b.ReportMetric(0, "provider-calls/op")
			b.Logf("result_signature=%s", signature)
			b.Logf("query_contract_sha256=%s statements=%d", capture.digest(), capture.statements)
			b.Logf("query_statement_sha256=%s", strings.Join(capture.fingerprints, ","))
		})
	}
}
