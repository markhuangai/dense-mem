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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type directoryPageOwnershipFixture struct {
	db           *gorm.DB
	rls          *storagepostgres.RLS
	repo         *DirectoryIdentityRepositoryImpl
	service      *accessservice.DirectoryIdentityService
	connectorIDs []uuid.UUID
	users        []*domain.DirectoryUser
	groups       []*domain.DirectoryGroup
}

func directoryPageOwnershipID(number int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012d", number))
}

func newDirectoryPageOwnershipFixture(t testing.TB) *directoryPageOwnershipFixture {
	t.Helper()
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	repo := NewDirectoryIdentityRepository(appDB, rls)
	f := &directoryPageOwnershipFixture{db: appDB, rls: rls, repo: repo}
	f.service = accessservice.NewDirectoryIdentityService(repo, accessservice.DirectoryIdentityConfig{})
	ctx := context.Background()
	for connectorIndex, size := range []int{101, 1} {
		provider := &domain.SSOProvider{
			ID:   directoryPageOwnershipID(3000 + connectorIndex),
			Name: fmt.Sprintf("directory ownership provider %d", connectorIndex),
			Kind: domain.SSOProviderKindGenericOIDC, IssuerURL: "https://idp.example.test",
			ClientID: "directory-page-ownership", Enabled: true, GroupsScopes: []string{},
		}
		require.NoError(t, NewSSORepository(appDB, rls).CreateProvider(ctx, provider))
		connector := &domain.DirectoryConnector{
			ID: directoryPageOwnershipID(4000 + connectorIndex), ProviderID: provider.ID,
			Status:           domain.DirectoryConnectorObserve,
			GroupPattern:     "^(?P<team>.+?)(?P<role>Member)$",
			RoleEntitlements: map[string]domain.DirectoryRoleEntitlement{"Member": {Role: "member", Scopes: []string{"read"}}},
			MaxAutoTeams:     5, CredentialVersion: 1,
		}
		require.NoError(t, repo.CreateDirectoryConnector(ctx, connector))
		f.connectorIDs = append(f.connectorIDs, connector.ID)
		for i := range size {
			user, err := repo.CreateDirectoryUser(ctx, domain.DirectoryUser{
				ID: directoryPageOwnershipID(1000 + connectorIndex*10000 + i), ConnectorID: connector.ID,
				ExternalID: fmt.Sprintf("External-%03d", i), UserName: fmt.Sprintf("user-%03d@example.test", i),
				Email: fmt.Sprintf("user-%03d@example.test", i), DisplayName: fmt.Sprintf("User %03d", i), Active: true,
			})
			require.NoError(t, err)
			nameIndex := i
			if i == 1 {
				nameIndex = 0
			}
			group, err := repo.CreateDirectoryGroupWithMembers(ctx, domain.DirectoryGroup{
				ID: directoryPageOwnershipID(2000 + connectorIndex*10000 + i), ConnectorID: connector.ID,
				ExternalID: fmt.Sprintf("GroupExternal-%03d", i), DisplayName: fmt.Sprintf("Group-%03d", nameIndex), Active: true,
			}, []uuid.UUID{user.ID})
			require.NoError(t, err)
			group.Members = []domain.DirectoryUser{*user}
			if connectorIndex == 0 {
				f.users = append(f.users, user)
				f.groups = append(f.groups, group)
			}
		}
	}
	return f
}

func directoryPageOwnershipSignature(users []*domain.DirectoryUser, groups []*domain.DirectoryGroup, total int) string {
	var signature strings.Builder
	fmt.Fprintf(&signature, "total=%d", total)
	userSignature := func(user *domain.DirectoryUser) {
		fmt.Fprintf(&signature, "|user:%s:%s:%s:%s:%s:%s:%t:%t:%t:%t", user.ID, user.ConnectorID,
			user.ExternalID, user.UserName, user.Email, user.DisplayName, user.Active,
			user.IdentityID != uuid.Nil, !user.CreatedAt.IsZero(), !user.UpdatedAt.IsZero())
	}
	for _, user := range users {
		userSignature(user)
	}
	for _, group := range groups {
		fmt.Fprintf(&signature, "|group:%s:%s:%s:%s:%t:%t:%t:members=%d", group.ID, group.ConnectorID,
			group.ExternalID, group.DisplayName, group.Active, !group.CreatedAt.IsZero(), !group.UpdatedAt.IsZero(), len(group.Members))
		for _, user := range group.Members {
			userSignature(&user)
		}
	}
	return signature.String()
}

var directoryPageOwnershipUUID = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

type directoryPageOwnershipCounters struct {
	statements, transactions, commits, rollbacks int
	capture                                      hash.Hash
}

func (c *directoryPageOwnershipCounters) record(query string, args []any) {
	c.statements++
	if c.capture == nil {
		return
	}
	fmt.Fprintln(c.capture, strings.Join(strings.Fields(query), " "))
	for _, arg := range args {
		fmt.Fprintln(c.capture, directoryPageOwnershipUUID.ReplaceAllString(fmt.Sprintf("%T:%v", arg, arg), "<uuid>"))
	}
}

type directoryPageOwnershipPool struct {
	gorm.ConnPool
	counters *directoryPageOwnershipCounters
}

func (p *directoryPageOwnershipPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	p.counters.record(query, args)
	return p.ConnPool.ExecContext(ctx, query, args...)
}

func (p *directoryPageOwnershipPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	p.counters.record(query, args)
	return p.ConnPool.QueryContext(ctx, query, args...)
}

func (p *directoryPageOwnershipPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	p.counters.record(query, args)
	return p.ConnPool.QueryRowContext(ctx, query, args...)
}

func (p *directoryPageOwnershipPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var tx gorm.ConnPool
	var err error
	if beginner, ok := p.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else if beginner, ok := p.ConnPool.(gorm.ConnPoolBeginner); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else {
		return nil, fmt.Errorf("directory benchmark pool cannot begin a transaction")
	}
	if err != nil {
		return nil, err
	}
	p.counters.transactions++
	return &directoryPageOwnershipTx{ConnPool: tx, counters: p.counters}, nil
}

type directoryPageOwnershipTx struct {
	gorm.ConnPool
	counters *directoryPageOwnershipCounters
}

func (tx *directoryPageOwnershipTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.counters.record(query, args)
	return tx.ConnPool.ExecContext(ctx, query, args...)
}

func (tx *directoryPageOwnershipTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.counters.record(query, args)
	return tx.ConnPool.QueryContext(ctx, query, args...)
}

func (tx *directoryPageOwnershipTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx.counters.record(query, args)
	return tx.ConnPool.QueryRowContext(ctx, query, args...)
}

func (tx *directoryPageOwnershipTx) Commit() error {
	err := tx.ConnPool.(gorm.TxCommitter).Commit()
	if err == nil {
		tx.counters.commits++
	}
	return err
}

func (tx *directoryPageOwnershipTx) Rollback() error {
	err := tx.ConnPool.(gorm.TxCommitter).Rollback()
	if err == nil {
		tx.counters.rollbacks++
	}
	return err
}

func BenchmarkDirectoryPageOwnership(b *testing.B) {
	f := newDirectoryPageOwnershipFixture(b)
	for _, kind := range []string{"users", "groups"} {
		for _, limit := range []int{0, 1, 100} {
			b.Run(fmt.Sprintf("%s/count_%d", kind, limit), func(b *testing.B) {
				counters := &directoryPageOwnershipCounters{}
				countedDB := f.db.Session(&gorm.Session{})
				pool := &directoryPageOwnershipPool{ConnPool: f.db.ConnPool, counters: counters}
				countedDB.ConnPool = pool
				countedDB.Statement.ConnPool = pool
				repo := NewDirectoryIdentityRepository(countedDB, f.rls)
				service := accessservice.NewDirectoryIdentityService(repo, accessservice.DirectoryIdentityConfig{})
				request := domain.DirectoryPageRequest{Limit: limit}
				expected := directoryPageOwnershipSignature(f.users[:limit], nil, 101)
				if kind == "groups" {
					expected = directoryPageOwnershipSignature(nil, f.groups[:limit], 101)
				}
				run := func() (string, error) {
					if kind == "users" {
						users, total, err := service.ListUsersPage(context.Background(), f.connectorIDs[0], request)
						if err != nil {
							return "", err
						}
						return directoryPageOwnershipSignature(users, nil, total), nil
					}
					groups, total, err := service.ListGroupsPage(context.Background(), f.connectorIDs[0], request)
					if err != nil {
						return "", err
					}
					return directoryPageOwnershipSignature(nil, groups, total), nil
				}
				for range 20 {
					result, err := run()
					require.NoError(b, err)
					require.Equal(b, expected, result)
				}
				queryHash := sha256.New()
				*counters = directoryPageOwnershipCounters{capture: queryHash}
				durations := make([]time.Duration, b.N)
				b.ReportAllocs()
				b.ResetTimer()
				for i := range b.N {
					if i == 1 {
						counters.capture = nil
					}
					started := time.Now()
					result, err := run()
					durations[i] = time.Since(started)
					if err != nil {
						b.Fatal(err)
					}
					if result != expected {
						b.Fatalf("directory page result changed: %s", result)
					}
				}
				b.StopTimer()
				if counters.statements == 0 || counters.transactions != b.N || counters.commits != b.N || counters.rollbacks != 0 {
					b.Fatalf("incomplete directory SQL/transaction counts: %+v", counters)
				}
				sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
				b.ReportMetric(float64(durations[(len(durations)-1)/2].Nanoseconds()), "p50-ns/op")
				b.ReportMetric(float64(durations[(len(durations)*95+99)/100-1].Nanoseconds()), "p95-ns/op")
				b.ReportMetric(float64(counters.statements)/float64(b.N), "sql-statements/op")
				b.ReportMetric(float64(counters.transactions)/float64(b.N), "transactions/op")
				b.ReportMetric(0, "provider-calls/op")
				resultHash := sha256.Sum256([]byte(expected))
				b.Logf("result_signature_sha256=%x", resultHash)
				b.Logf("query_contract_sha256=%s", hex.EncodeToString(queryHash.Sum(nil)))
			})
		}
	}
}
