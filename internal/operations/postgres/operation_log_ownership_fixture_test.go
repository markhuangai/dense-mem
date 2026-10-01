//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	operationsapp "github.com/markhuangai/dense-mem/internal/operations"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type operationLogOwnershipFixture struct {
	adminDB, appDB               *gorm.DB
	rls                          *storagepostgres.RLS
	teamA, teamB                 uuid.UUID
	profileA, profileB, profileC uuid.UUID
	now                          time.Time
	counters                     *operationLogOwnershipCounters
	repo                         *OperationLogRepositoryImpl
	service                      *operationsapp.OperationLogServiceImpl
}

func newOperationLogOwnershipFixture(t testing.TB) *operationLogOwnershipFixture {
	t.Helper()
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	f := &operationLogOwnershipFixture{
		adminDB: adminDB, appDB: appDB, rls: rls,
		teamA:    uuid.MustParse("00000000-0000-4000-8000-000000000001"),
		teamB:    uuid.MustParse("00000000-0000-4000-8000-000000000002"),
		profileA: uuid.MustParse("00000000-0000-4000-8000-000000000011"),
		profileB: uuid.MustParse("00000000-0000-4000-8000-000000000012"),
		profileC: uuid.MustParse("00000000-0000-4000-8000-000000000013"),
		now:      time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		counters: &operationLogOwnershipCounters{},
	}
	require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO operation_logs (
			id, timestamp, severity, severity_rank, message, source, team_id, profile_id, correlation_id, attrs
		) SELECT
			('00000000-0000-4000-8000-' || lpad((2000 + series)::text, 12, '0'))::uuid,
			?::timestamptz - series * interval '1 second',
			(ARRAY['TRACE','DEBUG','INFO','WARN','ERROR','FATAL'])[(series - 1) % 6 + 1],
			((series - 1) % 6) * 10, 'operation ownership', 'fixture',
			CASE WHEN series <= 1200 THEN ?::uuid ELSE ?::uuid END,
			CASE WHEN series > 1200 THEN ?::uuid WHEN series % 2 = 1 THEN ?::uuid ELSE ?::uuid END,
			CASE WHEN series <= 3 THEN 'corr-1' ELSE 'other-corr' END,
			jsonb_build_object(
				'invocation_id', CASE WHEN series <= 600 OR series > 1200 THEN 'invocation-1' ELSE 'other-invocation' END,
				'request_hash', CASE WHEN series <= 3 THEN 'hash-1' ELSE 'other-hash' END,
				'classification', (ARRAY['execution','replay','conflict'])[(series - 1) % 3 + 1],
				'retryable', series % 2 = 0,
				'reference_type', 'submission',
				'reference_id', CASE WHEN series <= 3 THEN 'ref-1' ELSE 'other-ref' END
			) || jsonb_build_object(
				(ARRAY['submission_id','attempt_id','canonical_attempt_id'])[(series - 1) % 3 + 1],
				CASE WHEN series <= 3 THEN 'attempt-1' ELSE 'other-attempt' END
			)
		FROM generate_series(1, 2000) AS series`,
			f.now, f.teamA, f.teamB, f.profileC, f.profileA, f.profileB).Error
	}))
	countedDB := appDB.Session(&gorm.Session{})
	pool := &operationLogOwnershipPool{ConnPool: appDB.ConnPool, counters: f.counters}
	countedDB.ConnPool = pool
	countedDB.Statement.ConnPool = pool
	f.repo = NewOperationLogRepository(countedDB, rls)
	f.service = operationsapp.NewOperationLogService(f.repo, nil)
	return f
}

func operationLogOwnershipSignature(page *domain.OperationLogPage) string {
	data, err := json.Marshal(page)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

type operationLogOwnershipCounters struct {
	statements, transactions, commits, rollbacks int
	capture                                      hash.Hash
	queries                                      []string
}

func (c *operationLogOwnershipCounters) reset() {
	*c = operationLogOwnershipCounters{capture: sha256.New()}
}

func (c *operationLogOwnershipCounters) counts() [4]int {
	return [4]int{c.statements, c.transactions, c.commits, c.rollbacks}
}

func (c *operationLogOwnershipCounters) querySignature() string {
	return hex.EncodeToString(c.capture.Sum(nil))
}

func (c *operationLogOwnershipCounters) record(query string, args []any) {
	c.statements++
	if c.capture == nil {
		return
	}
	c.queries = append(c.queries, query)
	fmt.Fprintf(c.capture, "%q\n", query)
	for _, arg := range args {
		fmt.Fprintf(c.capture, "%T:%v\n", arg, arg)
	}
}

type operationLogOwnershipPool struct {
	gorm.ConnPool
	counters *operationLogOwnershipCounters
}

func (p *operationLogOwnershipPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	p.counters.record(query, args)
	return p.ConnPool.ExecContext(ctx, query, args...)
}

func (p *operationLogOwnershipPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	p.counters.record(query, args)
	return p.ConnPool.QueryContext(ctx, query, args...)
}

func (p *operationLogOwnershipPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	p.counters.record(query, args)
	return p.ConnPool.QueryRowContext(ctx, query, args...)
}

func (p *operationLogOwnershipPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var tx gorm.ConnPool
	var err error
	if beginner, ok := p.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else if beginner, ok := p.ConnPool.(gorm.ConnPoolBeginner); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else {
		return nil, fmt.Errorf("operation log ownership pool cannot begin a transaction")
	}
	if err != nil {
		return nil, err
	}
	p.counters.transactions++
	return &operationLogOwnershipTx{ConnPool: tx, counters: p.counters}, nil
}

type operationLogOwnershipTx struct {
	gorm.ConnPool
	counters *operationLogOwnershipCounters
}

func (tx *operationLogOwnershipTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.counters.record(query, args)
	return tx.ConnPool.ExecContext(ctx, query, args...)
}

func (tx *operationLogOwnershipTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.counters.record(query, args)
	return tx.ConnPool.QueryContext(ctx, query, args...)
}

func (tx *operationLogOwnershipTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx.counters.record(query, args)
	return tx.ConnPool.QueryRowContext(ctx, query, args...)
}

func (tx *operationLogOwnershipTx) Commit() error {
	err := tx.ConnPool.(gorm.TxCommitter).Commit()
	if err == nil {
		tx.counters.commits++
	}
	return err
}

func (tx *operationLogOwnershipTx) Rollback() error {
	err := tx.ConnPool.(gorm.TxCommitter).Rollback()
	if err == nil {
		tx.counters.rollbacks++
	}
	return err
}
