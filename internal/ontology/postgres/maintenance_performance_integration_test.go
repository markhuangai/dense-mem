//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
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

func TestOntologyMaintenanceCommunityDependencyCounters(t *testing.T) {
	f := newOntologyFixtureWithMaintenance(t, false)
	source := maintenanceEntity(t, f, "Pre-migration counter source")
	f.restoreOntologySchema(t)
	require.NoError(t, f.admin.Exec(`GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO ontology_app; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO ontology_app`).Error)
	write := func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE entity_records SET version=version+1 WHERE team_id=?::uuid AND entity_id=?::uuid`, f.team, source.ID).Error
	}
	read := func() int64 {
		var version int64
		require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT version FROM community_topic_versions WHERE team_id=?::uuid AND space_id=?::uuid
				AND space_generation=? AND dependency_key=?`, f.team, f.space, f.generation, "entity:"+source.ID).Row().Scan(&version)
		}))
		return version
	}
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, write))
	require.EqualValues(t, 1, read())
	rollback := errors.New("counter transaction rollback")
	require.ErrorIs(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		if err := write(tx); err != nil {
			return err
		}
		return rollback
	}), rollback)
	require.EqualValues(t, 1, read())
	errs := make(chan error, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			errs <- f.rls.WithTeamTx(context.Background(), f.app, f.team, write)
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 9, read())
	other := maintenanceOtherTeam(t, f)
	migrator := ontologyMigrationProvider(t, f.admin)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO community_topic_versions(team_id,space_id,space_generation,dependency_key,version) VALUES(?::uuid,?::uuid,?,'stale-probe',1)`, f.team, f.space, f.generation+1).Error
	}))
	for _, phase := range []string{"optimized", "rollback", "reapplied"} {
		t.Run(phase, func(t *testing.T) {
			for _, mode := range []string{"team", "profile"} {
				run := func(team, owner string, fn func(*gorm.DB) error) error {
					if mode == "profile" {
						return f.rls.WithTeamProfileTx(context.Background(), f.app, team, owner, fn)
					}
					return f.rls.WithTeamTx(context.Background(), f.app, team, fn)
				}
				for _, owner := range f.owners {
					require.NoError(t, run(f.team, owner, func(tx *gorm.DB) error {
						var count int
						if err := tx.Raw(`SELECT count(*) FROM community_topic_versions WHERE team_id=?::uuid AND dependency_key=?`, f.team, "entity:"+source.ID).Row().Scan(&count); err != nil {
							return err
						}
						require.Equal(t, 1, count)
						if err := tx.Raw(`SELECT count(*) FROM community_topic_versions WHERE team_id=?::uuid AND dependency_key='stale-probe'`, f.team).Row().Scan(&count); err != nil {
							return err
						}
						require.Zero(t, count)
						return tx.Exec(`INSERT INTO community_topic_versions(team_id,space_id,space_generation,dependency_key,version) VALUES(?::uuid,?::uuid,?,?,1)`, f.team, f.space, f.generation, phase+mode+owner).Error
					}))
				}
				for _, mutation := range []string{
					`UPDATE community_topic_versions SET space_generation=space_generation+1 WHERE team_id=?::uuid AND dependency_key=?`,
					`UPDATE community_topic_versions SET space_id=(SELECT id FROM memory_spaces WHERE team_id=community_topic_versions.team_id AND kind='private' LIMIT 1) WHERE team_id=?::uuid AND dependency_key=?`,
				} {
					err := run(f.team, f.owners[0], func(tx *gorm.DB) error {
						return tx.Exec(mutation, f.team, "entity:"+source.ID).Error
					})
					require.ErrorContains(t, err, "row-level security policy")
				}
				require.NoError(t, run(other.team, other.owners[0], func(tx *gorm.DB) error {
					var count int
					if err := tx.Raw(`SELECT count(*) FROM community_topic_versions WHERE team_id=?::uuid`, f.team).Row().Scan(&count); err != nil {
						return err
					}
					require.Zero(t, count)
					result := tx.Exec(`UPDATE community_topic_versions SET version=version+1 WHERE team_id=?::uuid`, f.team)
					require.Zero(t, result.RowsAffected)
					return result.Error
				}))
				err := run(other.team, other.owners[0], func(tx *gorm.DB) error {
					return tx.Exec(`INSERT INTO community_topic_versions(team_id,space_id,space_generation,dependency_key,version) VALUES(?::uuid,?::uuid,?,?,1)`, f.team, f.space, f.generation, "foreign").Error
				})
				require.ErrorContains(t, err, "row-level security policy")
			}
			require.EqualValues(t, 9, read())
		})
		switch phase {
		case "optimized":
			_, err := migrator.ApplyVersion(context.Background(), 20261016010001, false)
			require.NoError(t, err)
		case "rollback":
			_, err := migrator.ApplyVersion(context.Background(), 20261016010001, true)
			require.NoError(t, err)
		}
	}
}

func TestOntologyMaintenanceWriteLatency(t *testing.T) {
	type pair struct {
		Writers int                `json:"writers"`
		Before  maintenanceLatency `json:"before"`
		After   maintenanceLatency `json:"after"`
	}
	var pairs []pair
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		t.Skip("set DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 for disposable ontology PostgreSQL cases")
	}
	for repeat := range 5 {
		t.Run(fmt.Sprintf("pair-%d", repeat+1), func(t *testing.T) {
			f := newMaintenanceLatencyFixture(t)
			source := maintenanceEntity(t, f, "Write latency entity")
			measure := func(writers int) maintenanceLatency {
				sources := make([]ontology.SourceHandle, writers)
				for writer := range writers {
					sources[writer] = maintenanceEntity(t, f, fmt.Sprintf("writer-%d-%d-%d", repeat, writer, time.Now().UnixNano()))
				}
				startWriters := make(chan struct{})
				durations := make(chan float64, writers*100)
				errs := make(chan error, writers)
				var workers sync.WaitGroup
				for _, own := range sources {
					workers.Add(1)
					go func() {
						defer workers.Done()
						<-startWriters
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
				close(startWriters)
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
		})
	}
	require.Len(t, pairs, 10)
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
			require.False(t, metric.after-metric.before > 2 && metric.after > metric.before*1.1, "write latency exceeds both 10%% and 2ms for pair %d/%d writers/%s", index/2+1, pair.Writers, metric.name)
		}
	}
}
