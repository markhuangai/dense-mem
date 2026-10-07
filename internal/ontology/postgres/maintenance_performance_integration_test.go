//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type maintenanceLatency struct {
	MedianMS float64 `json:"median_ms"`
	P95MS    float64 `json:"p95_ms"`
}

func TestOntologyMaintenanceWriteLatency(t *testing.T) {
	type pair struct {
		Writers int                `json:"writers"`
		Before  maintenanceLatency `json:"before"`
		After   maintenanceLatency `json:"after"`
	}
	var pairs []pair
	for repeat := range 5 {
		f := newOntologyFixtureWithMaintenance(t, false)
		source := maintenanceEntity(t, f, "Write latency entity")
		measure := func(writers int) maintenanceLatency {
			durations := make(chan float64, writers*100)
			errs := make(chan error, writers)
			var workers sync.WaitGroup
			for writer := range writers {
				workers.Add(1)
				go func() {
					defer workers.Done()
					own := maintenanceEntity(t, f, fmt.Sprintf("writer-%d-%d-%d", repeat, writer, time.Now().UnixNano()))
					for index := range 100 {
						start := time.Now()
						err := f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
							return tx.Exec(`UPDATE entity_records SET identity_context=jsonb_build_object('measurement',?::text),version=version+1 WHERE team_id=?::uuid AND entity_id=?::uuid`, fmt.Sprintf("%d:%d", index, start.UnixNano()), f.team, own.ID).Error
						})
						if err != nil {
							errs <- err
							return
						}
						durations <- float64(time.Since(start)) / float64(time.Millisecond)
					}
				}()
			}
			workers.Wait()
			close(errs)
			close(durations)
			for err := range errs {
				require.NoError(t, err)
			}
			var samples []float64
			for duration := range durations {
				samples = append(samples, duration)
			}
			require.Len(t, samples, writers*100)
			sort.Float64s(samples)
			return maintenanceLatency{MedianMS: samples[len(samples)/2], P95MS: samples[(len(samples)*95-1)/100]}
		}
		before := map[int]maintenanceLatency{1: measure(1), 8: measure(8)}
		migrator, err := storage.NewMigrator(f.admin)
		require.NoError(t, err)
		require.NoError(t, migrator.RunUp(context.Background()))
		require.NoError(t, f.admin.Exec(`GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO ontology_app; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO ontology_app`).Error)
		for _, writers := range []int{1, 8} {
			pairs = append(pairs, pair{Writers: writers, Before: before[writers], After: measure(writers)})
		}
		_, err = f.store.ReadSources(context.Background(), f.team, []ontology.SourceHandle{source})
		require.NoError(t, err)
	}
	if directory := os.Getenv("DENSE_MEM_ORGANIZATION_REPORT_DIR"); directory != "" {
		require.NoError(t, os.MkdirAll(directory, 0700))
		data, err := json.MarshalIndent(pairs, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "maintenance-write-latency.json"), data, 0600))
	}
	for index, pair := range pairs {
		for _, metric := range []struct {
			name          string
			before, after float64
		}{{"median", pair.Before.MedianMS, pair.After.MedianMS}, {"p95", pair.Before.P95MS, pair.After.P95MS}} {
			t.Logf("pair=%d writers=%d %s before=%.3fms after=%.3fms", index/2+1, pair.Writers, metric.name, metric.before, metric.after)
			require.False(t, metric.after-metric.before > 1 && metric.after > metric.before*1.1, "write latency exceeds both 10%% and 1ms for pair %d/%d writers/%s", index/2+1, pair.Writers, metric.name)
		}
	}
}
