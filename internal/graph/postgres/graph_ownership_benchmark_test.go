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

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type graphOwnershipFixture struct {
	teamID, ownerID string
	nodeIDs         []string
	edgeIDs         []string
	store           *Store
	db              *gorm.DB
	rls             *storagepostgres.RLS
}

func newGraphOwnershipFixture(t testing.TB) *graphOwnershipFixture {
	t.Helper()
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	teamID := createLedgerTeam(t, adminDB, rls, "graph-ownership")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "graph-owner")
	f := &graphOwnershipFixture{teamID: teamID, ownerID: ownerID, db: appDB, rls: rls}
	for i := 0; i < 7; i++ {
		f.nodeIDs = append(f.nodeIDs, fmt.Sprintf("00000000-0000-4000-8000-%012d", 1000+i))
		if i < 6 {
			f.edgeIDs = append(f.edgeIDs, fmt.Sprintf("00000000-0000-4000-8000-%012d", 2000+i))
		}
	}
	require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
		var spaceID string
		if err := tx.Raw(`SELECT dense_mem_team_shared_space(?::uuid)::text`, teamID).Row().Scan(&spaceID); err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO team_predicate_definitions (
			team_id, predicate_key, version, aliases, allowed_subject_kinds,
			allowed_object_kinds, relationship_kind, current_cardinality,
			lifecycle_state, origin, metadata, created_at)
			SELECT ?::uuid, predicate_key, version, aliases, allowed_subject_kinds,
			allowed_object_kinds, relationship_kind, current_cardinality,
			lifecycle_state, 'built_in', metadata, created_at
			FROM predicate_definitions WHERE predicate_key = 'uses' AND version = 1`, teamID).Error; err != nil {
			return err
		}
		for i, nodeID := range f.nodeIDs {
			if err := tx.Exec(`INSERT INTO entity_records (team_id, entity_id, entity_kind, space_id)
				VALUES (?::uuid, ?::uuid, 'concept', ?::uuid)`, teamID, nodeID, spaceID).Error; err != nil {
				return err
			}
			name := fmt.Sprintf("Graph Node %d", i)
			if err := tx.Exec(`INSERT INTO entity_names (team_id, entity_id, owner_profile_id, display_name,
				normalized_name, name_kind, space_id) VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?, 'canonical', ?::uuid)`,
				teamID, nodeID, ownerID, name, strings.ToLower(name), spaceID).Error; err != nil {
				return err
			}
		}
		for i, edgeID := range f.edgeIDs {
			if err := tx.Exec(`INSERT INTO relationship_records (
				team_id, relationship_id, owner_profile_id, semantic_group_key,
				subject_entity_id, predicate_key, predicate_version, object_entity_id,
				relationship_kind, current_cardinality, status, polarity,
				support_count, source_group_count, space_id)
				VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, 'uses', 1, ?::uuid,
				'state', 'many', 'active', '+', 1, 1, ?::uuid)`,
				teamID, edgeID, ownerID, fmt.Sprintf("graph-ownership-%d", i),
				f.nodeIDs[i], f.nodeIDs[i+1], spaceID).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	f.store = NewStore(appDB, rls)
	return f
}

func graphOwnershipSignature(snapshot *graphcontract.Snapshot) string {
	var nodes, edges []string
	for _, node := range snapshot.Nodes {
		nodes = append(nodes, fmt.Sprintf("%s:%s:%s:%s", node.Type, node.ID, node.Title, node.Status))
	}
	for _, edge := range snapshot.Edges {
		edges = append(edges, fmt.Sprintf("%s:%s:%s", edge.ID, edge.Source, edge.Target))
	}
	return fmt.Sprintf("%s|%s|%d|%d|%t|%s|%s", snapshot.Scope, snapshot.Query,
		snapshot.Depth, snapshot.Limit, snapshot.Truncated, strings.Join(nodes, ","), strings.Join(edges, ","))
}

var graphOwnershipUUID = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

type graphOwnershipCounters struct {
	statements, transactions, commits, rollbacks int
	capture                                      hash.Hash
}

func (c *graphOwnershipCounters) record(query string, args []any) {
	c.statements++
	if c.capture == nil {
		return
	}
	fmt.Fprintln(c.capture, strings.Join(strings.Fields(query), " "))
	for _, arg := range args {
		fmt.Fprintln(c.capture, graphOwnershipUUID.ReplaceAllString(fmt.Sprintf("%T:%v", arg, arg), "<uuid>"))
	}
}

type graphOwnershipPool struct {
	gorm.ConnPool
	counters *graphOwnershipCounters
}

func (p *graphOwnershipPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	p.counters.record(query, args)
	return p.ConnPool.ExecContext(ctx, query, args...)
}

func (p *graphOwnershipPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	p.counters.record(query, args)
	return p.ConnPool.QueryContext(ctx, query, args...)
}

func (p *graphOwnershipPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	p.counters.record(query, args)
	return p.ConnPool.QueryRowContext(ctx, query, args...)
}

func (p *graphOwnershipPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var tx gorm.ConnPool
	var err error
	if beginner, ok := p.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else if beginner, ok := p.ConnPool.(gorm.ConnPoolBeginner); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else {
		return nil, fmt.Errorf("graph benchmark pool cannot begin a transaction")
	}
	if err != nil {
		return nil, err
	}
	p.counters.transactions++
	return &graphOwnershipTx{ConnPool: tx, counters: p.counters}, nil
}

type graphOwnershipTx struct {
	gorm.ConnPool
	counters *graphOwnershipCounters
}

func (tx *graphOwnershipTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.counters.record(query, args)
	return tx.ConnPool.ExecContext(ctx, query, args...)
}

func (tx *graphOwnershipTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.counters.record(query, args)
	return tx.ConnPool.QueryContext(ctx, query, args...)
}

func (tx *graphOwnershipTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx.counters.record(query, args)
	return tx.ConnPool.QueryRowContext(ctx, query, args...)
}

func (tx *graphOwnershipTx) Commit() error {
	err := tx.ConnPool.(gorm.TxCommitter).Commit()
	if err == nil {
		tx.counters.commits++
	}
	return err
}

func (tx *graphOwnershipTx) Rollback() error {
	err := tx.ConnPool.(gorm.TxCommitter).Rollback()
	if err == nil {
		tx.counters.rollbacks++
	}
	return err
}

func BenchmarkGraphOwnership(b *testing.B) {
	f := newGraphOwnershipFixture(b)
	workloads := []struct {
		name  string
		query graphcontract.Query
		edges int
	}{
		{"overview", graphcontract.Query{TeamID: f.teamID, Limit: 80}, 6},
		{"local_default_depth", graphcontract.Query{TeamID: f.teamID, Scope: "local", AnchorType: "entity", AnchorID: f.nodeIDs[0], Limit: 80}, 2},
		{"local_max_depth", graphcontract.Query{TeamID: f.teamID, Scope: "local", AnchorType: "entity", AnchorID: f.nodeIDs[0], Depth: 99, Limit: 80}, 5},
	}
	for _, workload := range workloads {
		b.Run(workload.name, func(b *testing.B) {
			counters := &graphOwnershipCounters{}
			countedDB := f.db.Session(&gorm.Session{})
			pool := &graphOwnershipPool{ConnPool: f.db.ConnPool, counters: counters}
			countedDB.ConnPool = pool
			countedDB.Statement.ConnPool = pool
			store := NewStore(countedDB, f.rls)
			run := func() (string, error) {
				snapshot, err := store.SemanticGraph(context.Background(), workload.query)
				if err != nil {
					return "", err
				}
				if len(snapshot.Edges) != workload.edges {
					return "", fmt.Errorf("%s returned %d edges, want %d", workload.name, len(snapshot.Edges), workload.edges)
				}
				return graphOwnershipSignature(snapshot), nil
			}
			for range 20 {
				_, err := run()
				require.NoError(b, err)
			}
			contractHash := sha256.New()
			*counters = graphOwnershipCounters{capture: contractHash}
			signatures := make([]string, b.N)
			durations := make([]time.Duration, b.N)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if i == 1 {
					counters.capture = nil
				}
				started := time.Now()
				result, err := run()
				durations[i] = time.Since(started)
				if err != nil {
					b.Fatal(err)
				}
				signatures[i] = result
				if i > 0 && result != signatures[0] {
					b.Fatalf("graph result changed across iterations: %q != %q", result, signatures[0])
				}
			}
			b.StopTimer()
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			if counters.statements == 0 || counters.transactions != b.N || counters.commits != b.N || counters.rollbacks != 0 {
				b.Fatalf("incomplete graph SQL/transaction counts: %+v", counters)
			}
			b.ReportMetric(float64(durations[(len(durations)-1)/2].Nanoseconds()), "p50-ns/op")
			b.ReportMetric(float64(durations[(len(durations)*95+99)/100-1].Nanoseconds()), "p95-ns/op")
			b.ReportMetric(float64(counters.statements)/float64(b.N), "sql-statements/op")
			b.ReportMetric(float64(counters.transactions)/float64(b.N), "transactions/op")
			b.ReportMetric(0, "provider-calls/op")
			b.Logf("result_signature=%s", signatures[0])
			b.Logf("query_contract_sha256=%s", hex.EncodeToString(contractHash.Sum(nil)))
		})
	}
}
