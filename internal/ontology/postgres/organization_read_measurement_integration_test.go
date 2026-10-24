//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type organizationReadCounter struct {
	logger.Interface
	queries            atomic.Int64
	preparationQueries atomic.Int64
}

type organizationPreparationReadKey struct{}

func (c *organizationReadCounter) Trace(ctx context.Context, started time.Time, statement func() (string, int64), err error) {
	c.queries.Add(1)
	if ctx.Value(organizationPreparationReadKey{}) == true {
		c.preparationQueries.Add(1)
	}
	c.Interface.Trace(ctx, started, statement, err)
}

type organizationPreparationReads struct{ ontology.MaintenanceRepository }

func (r organizationPreparationReads) ClaimMaintenanceBatch(ctx context.Context, turn ontology.MaintenanceTurn, window string, now time.Time) (*ontology.MaintenanceClaim, error) {
	return r.MaintenanceRepository.ClaimMaintenanceBatch(context.WithValue(ctx, organizationPreparationReadKey{}, true), turn, window, now)
}

func organizationReadFixture(t *testing.T, catalogSize int) (*ontologyFixture, []ontology.SourceHandle, []ontology.Record) {
	t.Helper()
	f := newOrganizationFixture(t)
	relationship := maintenanceRelationship(t, f)
	snapshots, err := f.store.ReadSources(context.Background(), f.team, []ontology.SourceHandle{relationship})
	require.NoError(t, err)
	version, err := strconv.ParseInt(snapshots[0].State["predicate_version"], 10, 64)
	require.NoError(t, err)
	handles := []ontology.SourceHandle{
		maintenanceEntity(t, f, "PostgreSQL read measurement"),
		relationship,
		{Kind: ontology.PredicateSource, ID: snapshots[0].State["predicate"], Version: version},
	}
	for i := len(handles); i < ontology.MaxOrganizationSources; i++ {
		handles = append(handles, f.organizationEvidence(t, i%len(f.owners), fmt.Sprintf("PostgreSQL read measurement %d.", i), nil))
	}
	var sources []ontology.SourceDependency
	for _, handle := range handles[:4] {
		sources = append(sources, f.source(t, handle))
	}
	var records []ontology.Record
	revision := int64(0)
	for offset := 0; offset < catalogSize; offset += ontology.MaxChanges {
		var changes []ontology.Change
		for i := offset; i < min(offset+ontology.MaxChanges, catalogSize); i++ {
			record := testTopic(fmt.Sprintf("read-measurement-%03d", i))
			record.Definition.Description = "PostgreSQL read measurement"
			record.Sources = sources
			records = append(records, record)
			changes = append(changes, ontology.Change{Record: record})
		}
		publication, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), revision, changes...))
		require.NoError(t, err)
		revision = publication.Revision
	}
	require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
		ids := make([]string, len(records))
		for i, record := range records {
			ids[i] = record.ID
		}
		heads, err := loadHeads(tx, fence, ids, nil, nil)
		if err != nil {
			return err
		}
		for i, record := range records {
			records[i] = heads[record.ID]
		}
		return nil
	}))
	return f, handles, records
}

func organizationSingletonSources(t *testing.T, tx *gorm.DB, fence scope, handles []ontology.SourceHandle) []ontology.SourceSnapshot {
	t.Helper()
	result := make([]ontology.SourceSnapshot, 0, len(handles))
	for _, handle := range handles {
		snapshot, err := readSource(tx, fence, handle)
		if err != nil {
			require.ErrorIs(t, err, ontology.ErrSourceStale)
			snapshot = ontology.SourceSnapshot{SourceHandle: handle, TeamID: fence.TeamID, SpaceID: fence.SpaceID, Generation: fence.Generation, State: map[string]string{}}
		}
		result = append(result, snapshot)
	}
	return result
}

func organizationSingletonContext(t *testing.T, f *ontologyFixture, tx *gorm.DB, fence scope, actual ontology.OrganizationContext, handles []ontology.SourceHandle) ontology.OrganizationContext {
	t.Helper()
	expected := actual
	expected.Sources = organizationSingletonSources(t, tx, fence, handles)
	expected.Records = nil
	for _, record := range actual.Records {
		view, err := f.store.currentView(tx, fence, record.Record)
		require.NoError(t, err)
		expected.Records = append(expected.Records, view)
	}
	expected.Candidates = make([]ontology.RecordView, 0, len(actual.Candidates))
	for _, record := range actual.Candidates {
		view, err := f.store.currentView(tx, fence, record.Record)
		require.NoError(t, err)
		expected.Candidates = append(expected.Candidates, view)
	}
	return expected
}

type organizationReadMeasurement struct {
	CatalogSize int     `json:"catalog_records"`
	Sources     int     `json:"source_handles"`
	Phase       string  `json:"phase"`
	Queries     int64   `json:"sql_queries"`
	MedianMS    float64 `json:"median_ms"`
	P95MS       float64 `json:"p95_ms"`
	ContextHash string  `json:"context_hash,omitempty"`
}

func TestOntologyMaintenanceReadMeasurement(t *testing.T) {
	var measurements []organizationReadMeasurement
	canonical := map[int][2]string{}
	for _, catalogSize := range []int{20, 100} {
		f, handles, _ := organizationReadFixture(t, catalogSize)
		counter := &organizationReadCounter{Interface: f.app.Logger}
		f.app = f.app.Session(&gorm.Session{Logger: counter})
		f.store = NewStore(f.app, f.rls)
		before := schedulerCanonical(t, f)
		for _, count := range []int{1, 10, 20} {
			for _, phase := range []string{"sources", "context"} {
				measurement := organizationReadMeasurement{CatalogSize: catalogSize, Sources: count, Phase: phase}
				var samples []float64
				for repeat := range 22 {
					require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
						counter.queries.Store(0)
						started := time.Now()
						if phase == "sources" {
							actual, err := organizationSources(tx, fence, handles[:count])
							require.NoError(t, err)
							elapsed, queries := time.Since(started), counter.queries.Load()
							require.Equal(t, organizationSingletonSources(t, tx, fence, handles[:count]), actual)
							if repeat >= 2 {
								samples = append(samples, float64(elapsed.Microseconds())/1000)
								measurement.Queries = queries
							}
						} else {
							actual, err := f.store.organizationContext(tx, fence, handles[:count])
							require.NoError(t, err)
							elapsed, queries := time.Since(started), counter.queries.Load()
							expected := organizationSingletonContext(t, f, tx, fence, actual, handles[:count])
							require.Equal(t, expected, actual)
							hash, err := ontology.OrganizationContextHash(actual)
							require.NoError(t, err)
							expectedHash, err := ontology.OrganizationContextHash(expected)
							require.NoError(t, err)
							require.Equal(t, expectedHash, hash)
							if measurement.ContextHash != "" {
								require.Equal(t, measurement.ContextHash, hash)
							}
							measurement.ContextHash = hash
							if repeat >= 2 {
								samples = append(samples, float64(elapsed.Microseconds())/1000)
								measurement.Queries = queries
							}
						}
						return nil
					}))
				}
				sort.Float64s(samples)
				measurement.MedianMS = samples[len(samples)/2]
				measurement.P95MS = samples[(len(samples)*95-1)/100]
				measurements = append(measurements, measurement)
			}
		}
		canonical[catalogSize] = [2]string{before, schedulerCanonical(t, f)}
		require.Equal(t, canonical[catalogSize][0], canonical[catalogSize][1])
	}
	report := struct {
		Fixture      string                        `json:"fixture"`
		SourceSHA    string                        `json:"source_sha"`
		Measurements []organizationReadMeasurement `json:"measurements"`
		Canonical    map[int][2]string             `json:"canonical_before_after"`
	}{"ontology-batched-reads-v1", os.Getenv("DENSE_MEM_READ_MEASUREMENT_SOURCE_SHA"), measurements, canonical}
	encoded, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	t.Logf("ontology_read_measurement %s", encoded)
	if directory := os.Getenv("DENSE_MEM_ORGANIZATION_REPORT_DIR"); directory != "" {
		require.NoError(t, os.MkdirAll(directory, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "ontology-read-measurement.json"), encoded, 0600))
	}
}
