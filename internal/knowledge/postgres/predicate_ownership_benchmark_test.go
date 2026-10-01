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

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type predicateOwnershipBenchmarkCounters struct {
	statements   atomic.Int64
	transactions atomic.Int64
	commits      atomic.Int64
	rollbacks    atomic.Int64
	capture      *predicateOwnershipQueryCapture
}

var predicateOwnershipUUIDPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

type predicateOwnershipQueryCapture struct {
	hash         hash.Hash
	statements   int
	fingerprints []string
}

func newPredicateOwnershipQueryCapture() *predicateOwnershipQueryCapture {
	return &predicateOwnershipQueryCapture{hash: sha256.New()}
}

func (c *predicateOwnershipQueryCapture) add(query string, args []any) {
	c.statements++
	var normalized strings.Builder
	fmt.Fprintln(&normalized, strings.Join(strings.Fields(query), " "))
	for _, arg := range args {
		value := fmt.Sprintf("%T:%v", arg, arg)
		switch arg.(type) {
		case time.Time, *time.Time:
			value = fmt.Sprintf("%T:<time>", arg)
		}
		value = predicateOwnershipUUIDPattern.ReplaceAllString(value, "<uuid>")
		fmt.Fprintln(&normalized, value)
	}
	statement := normalized.String()
	_, _ = c.hash.Write([]byte(statement))
	fingerprint := sha256.Sum256([]byte(statement))
	c.fingerprints = append(c.fingerprints, hex.EncodeToString(fingerprint[:]))
}

func (c *predicateOwnershipQueryCapture) digest() string {
	return hex.EncodeToString(c.hash.Sum(nil))
}

type predicateOwnershipBenchmarkCount struct {
	statements   int64
	transactions int64
	commits      int64
	rollbacks    int64
}

func newPredicateOwnershipCountedDB(db *gorm.DB, counters *predicateOwnershipBenchmarkCounters) *gorm.DB {
	countedDB := db.Session(&gorm.Session{Context: db.Statement.Context})
	pool := &predicateOwnershipBenchmarkConnPool{ConnPool: db.ConnPool, counters: counters}
	countedDB.ConnPool = pool
	countedDB.Statement.ConnPool = pool
	return countedDB
}

func (c *predicateOwnershipBenchmarkCounters) reset() {
	c.statements.Store(0)
	c.transactions.Store(0)
	c.commits.Store(0)
	c.rollbacks.Store(0)
}

func (c *predicateOwnershipBenchmarkCounters) snapshot() predicateOwnershipBenchmarkCount {
	return predicateOwnershipBenchmarkCount{
		statements: c.statements.Load(), transactions: c.transactions.Load(),
		commits: c.commits.Load(), rollbacks: c.rollbacks.Load(),
	}
}

func (c *predicateOwnershipBenchmarkCounters) record(query string, args []any) {
	if c.capture != nil {
		c.capture.add(query, args)
	}
}

type predicateOwnershipBenchmarkConnPool struct {
	gorm.ConnPool
	counters *predicateOwnershipBenchmarkCounters
}

func (pool *predicateOwnershipBenchmarkConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return pool.ConnPool.PrepareContext(ctx, query)
}

func (pool *predicateOwnershipBenchmarkConnPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	pool.counters.statements.Add(1)
	pool.counters.record(query, args)
	return pool.ConnPool.ExecContext(ctx, query, args...)
}

func (pool *predicateOwnershipBenchmarkConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	pool.counters.statements.Add(1)
	pool.counters.record(query, args)
	return pool.ConnPool.QueryContext(ctx, query, args...)
}

func (pool *predicateOwnershipBenchmarkConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	pool.counters.statements.Add(1)
	pool.counters.record(query, args)
	return pool.ConnPool.QueryRowContext(ctx, query, args...)
}

func (pool *predicateOwnershipBenchmarkConnPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var tx gorm.ConnPool
	var err error
	if beginner, ok := pool.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else if beginner, ok := pool.ConnPool.(gorm.ConnPoolBeginner); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else {
		return nil, fmt.Errorf("predicate ownership benchmark: connection pool cannot begin a transaction")
	}
	if err != nil {
		return nil, err
	}
	pool.counters.transactions.Add(1)
	return &predicateOwnershipBenchmarkTx{ConnPool: tx, counters: pool.counters}, nil
}

type predicateOwnershipBenchmarkTx struct {
	gorm.ConnPool
	counters *predicateOwnershipBenchmarkCounters
}

func (tx *predicateOwnershipBenchmarkTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.counters.statements.Add(1)
	tx.counters.record(query, args)
	return tx.ConnPool.ExecContext(ctx, query, args...)
}

func (tx *predicateOwnershipBenchmarkTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.counters.statements.Add(1)
	tx.counters.record(query, args)
	return tx.ConnPool.QueryContext(ctx, query, args...)
}

func (tx *predicateOwnershipBenchmarkTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx.counters.statements.Add(1)
	tx.counters.record(query, args)
	return tx.ConnPool.QueryRowContext(ctx, query, args...)
}

func (tx *predicateOwnershipBenchmarkTx) Commit() error {
	err := tx.ConnPool.(gorm.TxCommitter).Commit()
	if err == nil {
		tx.counters.commits.Add(1)
	}
	return err
}

func (tx *predicateOwnershipBenchmarkTx) Rollback() error {
	err := tx.ConnPool.(gorm.TxCommitter).Rollback()
	if err == nil {
		tx.counters.rollbacks.Add(1)
	}
	return err
}

type predicateOwnershipBenchmarkFixture struct {
	store         *Store
	teamID        string
	ownerID       string
	registrations []SubmissionPredicateRegistrationInput
	preview       SynchronousRememberCommitInput
	counters      *predicateOwnershipBenchmarkCounters
}

func newPredicateOwnershipBenchmarkFixture(b *testing.B, catalogSize int) *predicateOwnershipBenchmarkFixture {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(b)
	b.Cleanup(cleanup)
	ctx := context.Background()
	insertSearchTestContract(b, adminDB, rls, "predicate-ownership-benchmark", 3, "exact", "")
	teamID := createLedgerTeam(b, adminDB, rls, fmt.Sprintf("predicate-ownership-%d", catalogSize))
	ownerID := createLedgerProfile(b, adminDB, rls, teamID, "predicate-ownership-benchmark-owner")
	unmetered := NewStore(appDB, rls, ConflictRuntimeConfig{})
	subject := createSemanticEntity(b, ctx, unmetered, teamID, ownerID, "project", "Dense-Mem")
	object := createSemanticEntity(b, ctx, unmetered, teamID, ownerID, "product", "PostgreSQL")
	require.NoError(b, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for index := range catalogSize {
			aliases := []string{}
			if index == 0 {
				aliases = append(aliases, "synonym zero")
			}
			if err := tx.Exec(`INSERT INTO team_predicate_definitions (
				team_id, predicate_key, version, aliases, allowed_subject_kinds,
				allowed_object_kinds, relationship_kind, current_cardinality,
				lifecycle_state, origin, metadata
			) VALUES (?::uuid, ?, 1, ?::text[], ARRAY['project']::text[],
				ARRAY['product']::text[], 'state', 'many', 'active', 'fixture', '{}'::jsonb)`,
				teamID, fmt.Sprintf("predicate_%d", index), pq.Array(aliases)).Error; err != nil {
				return err
			}
		}
		return tx.Exec(`ANALYZE team_predicate_definitions`).Error
	}))
	requestedKey := func(index int) string {
		switch index {
		case 0:
			return "synonym zero"
		case 1:
			return "Predicate 1"
		default:
			return fmt.Sprintf("predicate_%d", index)
		}
	}
	registrations := make([]SubmissionPredicateRegistrationInput, catalogSize)
	for index := range registrations {
		registrations[index] = SubmissionPredicateRegistrationInput{
			RelationshipRef: fmt.Sprintf("registration-%d", index), PredicateKey: requestedKey(index),
			SubjectKind: "project", ObjectKind: "product", RelationshipKind: "state", CurrentCardinality: "many",
		}
	}
	preview := conflictRememberFixtureInput(teamID, ownerID, subject.EntityID, object.EntityID,
		"Dense-Mem uses PostgreSQL.", "predicate-ownership-benchmark", nil)
	original := preview.Commit.RelationshipObservations[0]
	preview.Commit.RelationshipObservations = nil
	preview.Commit.RelationshipResults = nil
	for index := range 8 {
		ref := fmt.Sprintf("preview-%d", index)
		entry := original
		entry.RelationshipRef = ref
		entry.Observation.Ref = ref
		entry.Observation.OriginalPredicate = requestedKey(index)
		entry.Observation.PredicateKey = ""
		entry.Observation.PredicateVersion = 0
		preview.Commit.RelationshipObservations = append(preview.Commit.RelationshipObservations, entry)
		preview.Commit.RelationshipResults = append(preview.Commit.RelationshipResults,
			SubmissionRelationshipResultInput{RelationshipRef: ref, Disposition: "stored"})
		registration := registrations[index]
		registration.RelationshipRef = ref
		preview.Commit.PredicateRegistrations = append(preview.Commit.PredicateRegistrations, registration)
	}
	counters := &predicateOwnershipBenchmarkCounters{}
	countedDB := newPredicateOwnershipCountedDB(appDB, counters)
	return &predicateOwnershipBenchmarkFixture{
		store:  NewStore(countedDB, rls, ConflictRuntimeConfig{}),
		teamID: teamID, ownerID: ownerID, registrations: registrations, preview: preview, counters: counters,
	}
}

func BenchmarkPredicateOwnership(b *testing.B) {
	ctx := context.Background()
	for _, catalogSize := range []int{8, 513} {
		b.Run(fmt.Sprintf("catalog_%d", catalogSize), func(b *testing.B) {
			fixture := newPredicateOwnershipBenchmarkFixture(b, catalogSize)
			workloads := []struct {
				name string
				run  func() (string, error)
			}{
				{
					name: "registration_preflight",
					run: func() (string, error) {
						issues, err := fixture.store.ValidateSubmissionPredicateRegistrations(ctx, SubmissionPredicateRegistrationValidationInput{
							TeamID: fixture.teamID, OwnerProfileID: fixture.ownerID, Registrations: fixture.registrations,
						})
						if err != nil {
							return "", err
						}
						if len(issues) != 0 {
							return "", fmt.Errorf("preflight produced %d issues: %+v", len(issues), issues)
						}
						return "issues:0", nil
					},
				},
				{
					name: "embedding_preview",
					run: func() (string, error) {
						plan, err := fixture.store.PlanRememberEmbeddings(ctx, fixture.preview)
						if err != nil {
							return "", err
						}
						if len(plan.Documents) != 9 {
							return "", fmt.Errorf("preview returned %d documents, want 9", len(plan.Documents))
						}
						hashes := make([]string, len(plan.Documents))
						for index, document := range plan.Documents {
							hashes[index] = document.DocumentHash
						}
						return strings.Join(hashes, ","), nil
					},
				},
			}
			for _, workload := range workloads {
				b.Run(workload.name, func(b *testing.B) {
					for range 20 {
						_, err := workload.run()
						require.NoError(b, err)
					}
					capture := newPredicateOwnershipQueryCapture()
					fixture.counters.capture = capture
					_, err := workload.run()
					require.NoError(b, err)
					fixture.counters.capture = nil
					fixture.counters.reset()
					durations := make([]time.Duration, b.N)
					var signature string
					b.ReportAllocs()
					b.ResetTimer()
					for index := range b.N {
						started := time.Now()
						next, err := workload.run()
						durations[index] = time.Since(started)
						if err != nil {
							b.Fatalf("%s failed: %v", workload.name, err)
						}
						if index == 0 {
							signature = next
						} else if next != signature {
							b.Fatalf("%s result changed: %q != %q", workload.name, next, signature)
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
				})
			}
		})
	}
}
