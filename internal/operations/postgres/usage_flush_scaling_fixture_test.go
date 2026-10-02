//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	operationsapp "github.com/markhuangai/dense-mem/internal/operations"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var errUsageFlushReplyLost = errors.New("usage fixture: committed flush reply lost")

type usageFlushFixture struct {
	adminDB, appDB *gorm.DB
	rls            *storagepostgres.RLS
	teamID         uuid.UUID
	version        string
	counters       *usageFlushCounters
	repo           *UsageMetricsRepositoryImpl
	probe          *usageFlushProbe
	service        *operationsapp.UsageMetricsServiceImpl
}

func newUsageFlushFixture(t testing.TB) *usageFlushFixture {
	t.Helper()
	if os.Getenv("DATABASE_URL") != "" {
		t.Fatal("usage flush scaling requires DATABASE_URL unset and disposable PostgreSQL")
	}
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	f := &usageFlushFixture{
		adminDB: adminDB, appDB: appDB, rls: rls,
		teamID: usageFlushID("team", 0), counters: &usageFlushCounters{},
	}
	require.NoError(t, adminDB.Raw("SHOW server_version").Scan(&f.version).Error)
	require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO teams (id, name, description, metadata, config)
			VALUES (?, 'Usage Flush Fixture', '', '{}'::jsonb, '{}'::jsonb)`, f.teamID).Error; err != nil {
			return err
		}
		for i := 0; i < 1000; i++ {
			owner := usageFlushID("owner", i)
			if err := tx.Exec(`INSERT INTO actor_identities
				(id, kind, team_id, provider, subject, display_name, active)
				VALUES (?, 'human', NULL, 'usage-flush-fixture', ?, 'Usage Owner', true)`, owner, owner.String()).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO team_memberships
				(actor_identity_id, team_id, status, maximum_grants, sso_profile_name)
				VALUES (?, ?, 'active', ARRAY['read']::text[], 'Usage Owner')`, owner, f.teamID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO ownership_aliases
				(team_id, legacy_owner_id, canonical_identity_id, credential_id, reason)
				VALUES (?, ?, ?, NULL, 'sso')`, f.teamID, owner, owner).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	countedDB := appDB.Session(&gorm.Session{Context: context.Background()})
	pool := &usageFlushPool{ConnPool: appDB.ConnPool, counters: f.counters}
	countedDB.ConnPool = pool
	countedDB.Statement.ConnPool = pool
	f.repo = NewUsageMetricsRepository(countedDB, rls)
	return f
}

func usageFlushID(kind string, index int) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("dense-mem-usage-flush:%s:%d", kind, index)))
}

func (f *usageFlushFixture) reset(t testing.TB, buckets int, shape string) []domain.UsageMetricEvent {
	t.Helper()
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec("TRUNCATE usage_metric_flushes, usage_metric_buckets, usage_credential_buckets").Error; err != nil {
			return err
		}
		// DELETE preserves the SSO owner aliases that TRUNCATE CASCADE would erase.
		return tx.Exec("DELETE FROM credentials WHERE team_id = ?", f.teamID).Error
	}))
	events := make([]domain.UsageMetricEvent, buckets)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		for i := range events {
			ownerIndex := i
			if shape == "ten_per_owner" {
				ownerIndex = i / 10
			}
			event := domain.UsageMetricEvent{
				Timestamp: time.Date(2026, 10, 1, 12, 0, i%20, 0, time.UTC),
				TeamID:    f.teamID, KeyID: usageFlushID("owner", ownerIndex),
				Route: "/mcp", Method: "POST", Status: 500,
				Latency: time.Duration(1+i%7) * time.Millisecond, MCPToolCalls: 2, MCPToolFailures: 1,
			}
			if shape != "no_credentials" {
				event.CredentialID = usageFlushID("credential", i)
				if err := tx.Exec(`INSERT INTO credentials
					(id, actor_identity_id, owner_identity_id, team_id, kind, name, scopes, status)
					VALUES (?, ?, ?, ?, 'session', 'Usage Session', ARRAY['read']::text[], 'active')`,
					event.CredentialID, event.KeyID, event.KeyID, f.teamID).Error; err != nil {
					return err
				}
			}
			events[i] = event
		}
		return nil
	}))
	f.probe = &usageFlushProbe{UsageMetricsRepository: f.repo}
	f.service = operationsapp.NewUsageMetricsService(f.probe, nil)
	*f.counters = usageFlushCounters{}
	return events
}

func (f *usageFlushFixture) resetUsage(t testing.TB) {
	t.Helper()
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec("TRUNCATE usage_metric_flushes, usage_metric_buckets, usage_credential_buckets").Error
	}))
	f.probe = &usageFlushProbe{UsageMetricsRepository: f.repo}
	f.service = operationsapp.NewUsageMetricsService(f.probe, nil)
	*f.counters = usageFlushCounters{}
}

func usageFlushLater(events []domain.UsageMetricEvent, seconds int, latency int64) []domain.UsageMetricEvent {
	result := append([]domain.UsageMetricEvent(nil), events...)
	for i := range result {
		result[i].Timestamp = result[i].Timestamp.Add(time.Duration(seconds) * time.Second)
		result[i].Latency = time.Duration(latency+int64(i%11)) * time.Millisecond
		result[i].MCPToolCalls = 3
		result[i].MCPToolFailures = 2
	}
	return result
}

func (f *usageFlushFixture) record(events []domain.UsageMetricEvent) {
	for _, event := range events {
		f.service.RecordRequest(context.Background(), event)
	}
}

type usageFlushState struct {
	Owners      []domain.UsageMetricBucket `json:"owners"`
	Credentials []domain.UsageMetricBucket `json:"credentials"`
}

func (f *usageFlushFixture) state(t testing.TB) usageFlushState {
	t.Helper()
	result := usageFlushState{Owners: []domain.UsageMetricBucket{}, Credentials: []domain.UsageMetricBucket{}}
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT bucket_start, team_id, key_id, route, method, status_class,
			request_count, error_count, mcp_tool_calls, mcp_tool_failures, total_latency_ms,
			max_latency_ms, last_seen_at FROM usage_metric_buckets`).Scan(&result.Owners).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT bucket_start, team_id, credential_id, route, method, status_class,
			request_count, error_count, mcp_tool_calls, mcp_tool_failures, total_latency_ms,
			max_latency_ms, last_seen_at FROM usage_credential_buckets`).Scan(&result.Credentials).Error
	}))
	return result
}

func usageFlushExpected(events []domain.UsageMetricEvent) usageFlushState {
	owners, credentials := map[uuid.UUID]domain.UsageMetricBucket{}, map[uuid.UUID]domain.UsageMetricBucket{}
	for _, event := range events {
		for index, key := range []uuid.UUID{event.KeyID, event.CredentialID} {
			if key == uuid.Nil {
				continue
			}
			rows := owners
			if index == 1 {
				rows = credentials
			}
			row := rows[key]
			row.BucketStart, row.TeamID = event.Timestamp.Truncate(time.Minute), event.TeamID
			row.Route, row.Method, row.StatusClass = "/mcp", "POST", 5
			if index == 0 {
				row.KeyID = key
			} else {
				row.CredentialID = key
			}
			row.RequestCount++
			row.ErrorCount++
			row.MCPToolCalls += event.MCPToolCalls
			row.MCPToolFailures += event.MCPToolFailures
			row.TotalLatencyMS += event.Latency.Milliseconds()
			row.MaxLatencyMS = max(row.MaxLatencyMS, event.Latency.Milliseconds())
			if event.Timestamp.After(row.LastSeenAt) {
				row.LastSeenAt = event.Timestamp
			}
			rows[key] = row
		}
	}
	result := usageFlushState{Owners: []domain.UsageMetricBucket{}, Credentials: []domain.UsageMetricBucket{}}
	for _, row := range owners {
		result.Owners = append(result.Owners, row)
	}
	for _, row := range credentials {
		result.Credentials = append(result.Credentials, row)
	}
	return result
}

func (f *usageFlushFixture) verify(t testing.TB, events []domain.UsageMetricEvent) usageFlushState {
	t.Helper()
	actual, expected := f.state(t), usageFlushExpected(events)
	usageFlushSort(&actual)
	usageFlushSort(&expected)
	require.Equal(t, expected.Owners, actual.Owners)
	require.Equal(t, expected.Credentials, actual.Credentials)
	return actual
}

func usageFlushSort(state *usageFlushState) {
	// PostgreSQL drivers may decode an unchanged UTC instant with time.Local.
	for _, rows := range [][]domain.UsageMetricBucket{state.Owners, state.Credentials} {
		for i := range rows {
			rows[i].BucketStart = rows[i].BucketStart.UTC()
			rows[i].LastSeenAt = rows[i].LastSeenAt.UTC()
		}
	}
	sort.Slice(state.Owners, func(i, j int) bool { return state.Owners[i].KeyID.String() < state.Owners[j].KeyID.String() })
	sort.Slice(state.Credentials, func(i, j int) bool {
		return state.Credentials[i].CredentialID.String() < state.Credentials[j].CredentialID.String()
	})
}

func usageFlushSignature(state usageFlushState) string {
	usageFlushSort(&state)
	encoded, err := json.Marshal(state)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

type usageFlushProbe struct {
	operationscontract.UsageMetricsRepository
	before       func()
	loseReply    bool
	lastID       uuid.UUID
	bucketCount  int
	repositoryNS int64
}

func (p *usageFlushProbe) UpsertBuckets(ctx context.Context, id uuid.UUID, buckets []domain.UsageMetricBucket) error {
	if p.before != nil {
		before := p.before
		p.before = nil
		before()
	}
	p.lastID, p.bucketCount = id, len(buckets)
	started := time.Now()
	err := p.UsageMetricsRepository.UpsertBuckets(ctx, id, buckets)
	p.repositoryNS += time.Since(started).Nanoseconds()
	if err == nil && p.loseReply {
		p.loseReply = false
		return errUsageFlushReplyLost
	}
	return err
}

type usageFlushCounters struct {
	Ledger, Owners, Credentials, Setup, Other int64
	Transactions, Commits, Rollbacks          int64
	LedgerNS, OwnersNS, CredentialsNS         int64
	SetupNS, BeginNS, CommitNS                int64
}

func (c *usageFlushCounters) note(query string, elapsed time.Duration) {
	query = strings.TrimSpace(query)
	switch {
	case strings.HasPrefix(query, "INSERT INTO usage_metric_flushes"):
		c.Ledger++
		c.LedgerNS += elapsed.Nanoseconds()
	case strings.HasPrefix(query, "INSERT INTO usage_metric_buckets"):
		c.Owners++
		c.OwnersNS += elapsed.Nanoseconds()
	case strings.HasPrefix(query, "INSERT INTO usage_credential_buckets"):
		c.Credentials++
		c.CredentialsNS += elapsed.Nanoseconds()
	case strings.HasPrefix(query, "SELECT set_config("):
		c.Setup++
		c.SetupNS += elapsed.Nanoseconds()
	default:
		c.Other++
	}
}

type usageFlushPool struct {
	gorm.ConnPool
	counters *usageFlushCounters
}

func (p *usageFlushPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	started := time.Now()
	result, err := p.ConnPool.ExecContext(ctx, query, args...)
	p.counters.note(query, time.Since(started))
	return result, err
}

func (p *usageFlushPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	started := time.Now()
	result, err := p.ConnPool.QueryContext(ctx, query, args...)
	p.counters.note(query, time.Since(started))
	return result, err
}

func (p *usageFlushPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	started := time.Now()
	result := p.ConnPool.QueryRowContext(ctx, query, args...)
	p.counters.note(query, time.Since(started))
	return result
}

func (p *usageFlushPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	started := time.Now()
	var tx gorm.ConnPool
	var err error
	if beginner, ok := p.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else if beginner, ok := p.ConnPool.(gorm.ConnPoolBeginner); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else {
		return nil, errors.New("usage flush fixture cannot begin transaction")
	}
	if err != nil {
		return nil, err
	}
	p.counters.Transactions++
	p.counters.BeginNS += time.Since(started).Nanoseconds()
	return &usageFlushTx{usageFlushPool: &usageFlushPool{ConnPool: tx, counters: p.counters}}, nil
}

type usageFlushTx struct{ *usageFlushPool }

func (tx *usageFlushTx) Commit() error {
	started := time.Now()
	err := tx.ConnPool.(gorm.TxCommitter).Commit()
	tx.counters.CommitNS += time.Since(started).Nanoseconds()
	if err == nil {
		tx.counters.Commits++
	}
	return err
}

func (tx *usageFlushTx) Rollback() error {
	err := tx.ConnPool.(gorm.TxCommitter).Rollback()
	if err == nil {
		tx.counters.Rollbacks++
	}
	return err
}
