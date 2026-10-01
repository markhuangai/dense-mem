//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"
)

func BenchmarkCommunityOwnership(b *testing.B) {
	f := newCommunityOwnershipFixture(b)
	workloads := []struct {
		name  string
		input CommunityRecallInput
	}{
		{"returned_evidence", CommunityRecallInput{TeamID: f.teamID, ReturnedEvidenceIDs: []string{f.evidenceIDs[0]}}},
		{"known_evidence", CommunityRecallInput{TeamID: f.teamID, KnownEvidenceIDs: []string{f.evidenceIDs[1]}}},
		{"known_relationship", CommunityRecallInput{TeamID: f.teamID, KnownRelationshipIDs: []string{f.sources[2].RelationshipID}}},
		{"seed_relationship", CommunityRecallInput{TeamID: f.teamID, SeedRelationshipIDs: []string{f.sources[3].RelationshipID}}},
		{"entity_overlap", CommunityRecallInput{TeamID: f.teamID, ExpandFromEntityIDs: []string{f.expandID}}},
		{"remaining", CommunityRecallInput{TeamID: f.teamID}},
		{"default_hydration", f.mixedInput()},
		{"maximum_hydration", func() CommunityRecallInput { i := f.mixedInput(); i.RelationshipLimit = 20; return i }()},
		{"covered_groups", func() CommunityRecallInput {
			i := f.mixedInput()
			i.CoveredGroupKeys = []string{f.sources[4].SemanticGroupKey, f.sources[5].SemanticGroupKey}
			return i
		}()},
	}
	for _, workload := range workloads {
		b.Run(workload.name, func(b *testing.B) {
			if b.N != 200 {
				b.StopTimer()
				return
			}
			b.StopTimer()
			run := func() (string, error) {
				records, err := f.store.RecallCommunities(context.Background(), workload.input)
				if err != nil {
					return "", err
				}
				if len(records) == 0 {
					return "", fmt.Errorf("empty community recall")
				}
				return f.signature(records), nil
			}
			for range 20 {
				if _, err := run(); err != nil {
					b.Fatal(err)
				}
			}
			f.counters.reset()
			durations := make([]time.Duration, b.N)
			signature := ""
			b.ReportAllocs()
			b.ResetTimer()
			b.StartTimer()
			for i := range b.N {
				start := time.Now()
				next, err := run()
				durations[i] = time.Since(start)
				if err != nil {
					b.Fatal(err)
				}
				if i == 0 {
					signature = next
				} else if next != signature {
					b.Fatal("community result signature changed")
				}
			}
			b.StopTimer()
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			if f.counters.transactions.Load() != int64(b.N) || f.counters.completions.Load() != int64(b.N) {
				b.Fatal("community recall must complete exactly one transaction per operation")
			}
			b.ReportMetric(float64(durations[(len(durations)-1)/2].Nanoseconds()), "p50-ns/op")
			b.ReportMetric(float64(durations[(len(durations)*95+99)/100-1].Nanoseconds()), "p95-ns/op")
			b.ReportMetric(float64(f.counters.statements.Load())/float64(b.N), "sql-statements/op")
			b.ReportMetric(float64(f.counters.transactions.Load())/float64(b.N), "transactions/op")
			b.ReportMetric(float64(f.counters.completions.Load())/float64(b.N), "transaction-completions/op")
			b.ReportMetric(0, "provider-calls/op")
			b.Logf("result_signature=%s", signature)
		})
	}
}
