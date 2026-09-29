//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash"
	"reflect"
	"regexp"
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
	privacyservice "github.com/markhuangai/dense-mem/internal/privacy/service"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var privacyOwnershipUUIDPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
var privacyOwnershipHashPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{64}\b`)

type privacyOwnershipQueryCapture struct {
	hash         hash.Hash
	statements   int
	fingerprints []string
}

func newPrivacyOwnershipQueryCapture() *privacyOwnershipQueryCapture {
	return &privacyOwnershipQueryCapture{hash: sha256.New()}
}

func (c *privacyOwnershipQueryCapture) add(query string, args []any) {
	c.statements++
	var normalized strings.Builder
	fmt.Fprintln(&normalized, strings.Join(strings.Fields(query), " "))
	for _, arg := range args {
		value := fmt.Sprintf("%T:%v", arg, arg)
		switch arg.(type) {
		case time.Time, *time.Time:
			value = fmt.Sprintf("%T:<time>", arg)
		default:
			if pointer := reflect.ValueOf(arg); pointer.IsValid() && pointer.Kind() == reflect.Pointer && !pointer.IsNil() {
				value = fmt.Sprintf("%T:%v", arg, pointer.Elem().Interface())
			}
		}
		value = privacyOwnershipUUIDPattern.ReplaceAllString(value, "<uuid>")
		value = privacyOwnershipHashPattern.ReplaceAllString(value, "<hash>")
		fmt.Fprintln(&normalized, value)
	}
	statement := normalized.String()
	_, _ = c.hash.Write([]byte(statement))
	fingerprint := sha256.Sum256([]byte(statement))
	c.fingerprints = append(c.fingerprints, hex.EncodeToString(fingerprint[:]))
}

func (c *privacyOwnershipQueryCapture) digest() string {
	return hex.EncodeToString(c.hash.Sum(nil))
}

type privacyOwnershipCounts struct {
	statements   int64
	transactions int64
	commits      int64
	rollbacks    int64
}

type privacyOwnershipCounters struct {
	statements   atomic.Int64
	transactions atomic.Int64
	commits      atomic.Int64
	rollbacks    atomic.Int64
	capture      *privacyOwnershipQueryCapture
}

func (c *privacyOwnershipCounters) reset() {
	c.statements.Store(0)
	c.transactions.Store(0)
	c.commits.Store(0)
	c.rollbacks.Store(0)
}

func (c *privacyOwnershipCounters) snapshot() privacyOwnershipCounts {
	return privacyOwnershipCounts{c.statements.Load(), c.transactions.Load(), c.commits.Load(), c.rollbacks.Load()}
}

func (c *privacyOwnershipCounters) record(query string, args []any) {
	c.statements.Add(1)
	if c.capture != nil {
		c.capture.add(query, args)
	}
}

type privacyOwnershipPool struct {
	gorm.ConnPool
	counters *privacyOwnershipCounters
}

func (p *privacyOwnershipPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	p.counters.record(query, args)
	return p.ConnPool.ExecContext(ctx, query, args...)
}

func (p *privacyOwnershipPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	p.counters.record(query, args)
	return p.ConnPool.QueryContext(ctx, query, args...)
}

func (p *privacyOwnershipPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	p.counters.record(query, args)
	return p.ConnPool.QueryRowContext(ctx, query, args...)
}

func (p *privacyOwnershipPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var tx gorm.ConnPool
	var err error
	if beginner, ok := p.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else if beginner, ok := p.ConnPool.(gorm.ConnPoolBeginner); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else {
		return nil, fmt.Errorf("privacy benchmark connection pool cannot begin a transaction")
	}
	if err != nil {
		return nil, err
	}
	p.counters.transactions.Add(1)
	return &privacyOwnershipTx{ConnPool: tx, counters: p.counters}, nil
}

type privacyOwnershipTx struct {
	gorm.ConnPool
	counters *privacyOwnershipCounters
}

func (tx *privacyOwnershipTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.counters.record(query, args)
	return tx.ConnPool.ExecContext(ctx, query, args...)
}

func (tx *privacyOwnershipTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.counters.record(query, args)
	return tx.ConnPool.QueryContext(ctx, query, args...)
}

func (tx *privacyOwnershipTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx.counters.record(query, args)
	return tx.ConnPool.QueryRowContext(ctx, query, args...)
}

func (tx *privacyOwnershipTx) Commit() error {
	err := tx.ConnPool.(gorm.TxCommitter).Commit()
	if err == nil {
		tx.counters.commits.Add(1)
	}
	return err
}

func (tx *privacyOwnershipTx) Rollback() error {
	err := tx.ConnPool.(gorm.TxCommitter).Rollback()
	if err == nil {
		tx.counters.rollbacks.Add(1)
	}
	return err
}

func privacyOwnershipCountedDB(db *gorm.DB, counters *privacyOwnershipCounters) *gorm.DB {
	counted := db.Session(&gorm.Session{})
	pool := &privacyOwnershipPool{ConnPool: db.ConnPool, counters: counters}
	counted.ConnPool = pool
	counted.Statement.ConnPool = pool
	return counted
}

type privacyOwnershipFixture struct {
	adminDB   *gorm.DB
	rls       *storagepostgres.RLS
	teamID    uuid.UUID
	targetID  uuid.UUID
	spaceID   uuid.UUID
	operation uuid.UUID
	repo      *Store
	service   *privacyservice.PrivateMemoryService
	counters  *privacyOwnershipCounters
	now       time.Time
}

func newPrivacyOwnershipFixture(b *testing.B) *privacyOwnershipFixture {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(b)
	b.Cleanup(cleanup)
	teamID := uuid.MustParse(createLedgerTeam(b, adminDB, rls, "privacy-ownership-benchmark"))
	ownerID := createLedgerSSOIdentity(b, adminDB, rls, teamID)
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	target := createOwnedCredential(b, credentialRepo, teamID, ownerID, "benchmark-target", domain.CredentialBindingCredentialPrivate)
	counters := &privacyOwnershipCounters{}
	repo := NewStore(privacyOwnershipCountedDB(appDB, counters), rls)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	repo.SetNow(func() time.Time { return now })
	return &privacyOwnershipFixture{
		adminDB: adminDB, rls: rls, teamID: teamID, targetID: target.ID, spaceID: target.MemorySpaceID,
		repo: repo, service: privacyservice.NewPrivateMemoryService(privacyservice.PrivateMemoryServiceConfig{Repository: repo}),
		counters: counters, now: now,
	}
}

func (f *privacyOwnershipFixture) request(ctx context.Context) (string, error) {
	operation, err := f.service.RequestCredentialErasure(ctx, f.teamID, f.targetID, privacyservice.PrivateMemoryCommand{
		IdempotencyKey: "privacy-ownership-benchmark", AcknowledgeIrreversible: true,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|%s|%d", operation.Status, operation.Action, operation.AttemptCount), nil
}

func (f *privacyOwnershipFixture) resetRequest(ctx context.Context) error {
	return f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM private_memory_erasure_operations WHERE space_id = ?`, f.spaceID).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE memory_spaces
			SET lifecycle_state = 'active', generation = 1, sealed_at = NULL, retired_at = NULL
			WHERE id = ?`, f.spaceID).Error
	})
}

func (f *privacyOwnershipFixture) seedRelease(ctx context.Context) error {
	if err := f.resetRequest(ctx); err != nil {
		return err
	}
	if _, err := f.request(ctx); err != nil {
		return err
	}
	claim, err := f.repo.ClaimNext(ctx, "privacy-benchmark-worker", time.Minute)
	if err != nil {
		return err
	}
	if claim == nil {
		return fmt.Errorf("privacy benchmark operation was not claimed")
	}
	f.operation = claim.ID
	return nil
}

func (f *privacyOwnershipFixture) prepareRelease(ctx context.Context, attemptCount int) error {
	return f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE private_memory_erasure_operations
			SET status = 'processing', attempt_count = ?, fence = 1,
			    worker_id = 'privacy-benchmark-worker', lease_until = ?,
			    next_attempt_at = NULL, last_error_code = '', completed_at = NULL
			WHERE id = ?`, attemptCount, f.now.Add(time.Hour), f.operation).Error
	})
}

func (f *privacyOwnershipFixture) release(ctx context.Context) (string, error) {
	err := f.repo.ReleaseClaim(ctx, f.operation, "privacy-benchmark-worker", 1, "manifest_mismatch")
	return "", err
}

func (f *privacyOwnershipFixture) releaseSignature(expectedStatus string) (string, error) {
	var status, errorCode string
	var nextAttemptAt sql.NullTime
	err := f.adminDB.Raw(`SELECT status, last_error_code, next_attempt_at
		FROM private_memory_erasure_operations WHERE id = ?`, f.operation).Row().Scan(&status, &errorCode, &nextAttemptAt)
	if err != nil {
		return "", err
	}
	if status != expectedStatus || errorCode != "manifest_mismatch" || nextAttemptAt.Valid != (expectedStatus == "queued") {
		return "", fmt.Errorf("privacy benchmark release changed outcome: %s %s %v", status, errorCode, nextAttemptAt.Valid)
	}
	if nextAttemptAt.Valid && !nextAttemptAt.Time.Equal(f.now.Add(time.Second)) {
		return "", fmt.Errorf("privacy benchmark retry time changed: %s", nextAttemptAt.Time)
	}
	return status + "|" + errorCode, nil
}

func BenchmarkPrivacyOwnership(b *testing.B) {
	fixture := newPrivacyOwnershipFixture(b)
	ctx := context.Background()
	workloads := []struct {
		name    string
		setup   func() error
		prepare func() error
		run     func() (string, error)
		verify  func() (string, error)
	}{
		{
			name: "new_request", prepare: func() error { return fixture.resetRequest(ctx) },
			run: func() (string, error) { return fixture.request(ctx) },
		},
		{
			name: "replay", setup: func() error {
				if err := fixture.resetRequest(ctx); err != nil {
					return err
				}
				_, err := fixture.request(ctx)
				return err
			}, run: func() (string, error) { return fixture.request(ctx) },
		},
		{
			name: "failed_claim_release_queued", setup: func() error { return fixture.seedRelease(ctx) },
			prepare: func() error { return fixture.prepareRelease(ctx, 1) },
			run:     func() (string, error) { return fixture.release(ctx) },
			verify:  func() (string, error) { return fixture.releaseSignature("queued") },
		},
		{
			name: "failed_claim_release_exhausted", setup: func() error { return fixture.seedRelease(ctx) },
			prepare: func() error { return fixture.prepareRelease(ctx, 5) },
			run:     func() (string, error) { return fixture.release(ctx) },
			verify:  func() (string, error) { return fixture.releaseSignature("failed") },
		},
	}
	for _, workload := range workloads {
		b.Run(workload.name, func(b *testing.B) {
			b.StopTimer()
			if workload.setup != nil {
				require.NoError(b, workload.setup())
			}
			one := func() (string, error) {
				if workload.prepare != nil {
					if err := workload.prepare(); err != nil {
						return "", err
					}
				}
				signature, err := workload.run()
				if err != nil {
					return "", err
				}
				if workload.verify != nil {
					return workload.verify()
				}
				return signature, nil
			}
			for range 20 {
				_, err := one()
				require.NoError(b, err)
			}
			capture := newPrivacyOwnershipQueryCapture()
			if workload.prepare != nil {
				require.NoError(b, workload.prepare())
			}
			fixture.counters.capture = capture
			capturedSignature, err := workload.run()
			fixture.counters.capture = nil
			require.NoError(b, err)
			if workload.verify != nil {
				capturedSignature, err = workload.verify()
				require.NoError(b, err)
			}
			fixture.counters.reset()
			durations := make([]time.Duration, b.N)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if workload.prepare != nil {
					require.NoError(b, workload.prepare())
				}
				b.StartTimer()
				started := time.Now()
				signature, err := workload.run()
				durations[i] = time.Since(started)
				b.StopTimer()
				if err != nil {
					b.Fatalf("%s failed: %v", workload.name, err)
				}
				if workload.verify != nil {
					signature, err = workload.verify()
					require.NoError(b, err)
				}
				if signature != capturedSignature {
					b.Fatalf("%s result changed: %q != %q", workload.name, signature, capturedSignature)
				}
			}
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
			b.Logf("result_signature=%s", capturedSignature)
			b.Logf("query_contract_sha256=%s statements=%d", capture.digest(), capture.statements)
			b.Logf("query_statement_sha256=%s", strings.Join(capture.fingerprints, ","))
		})
	}
}
