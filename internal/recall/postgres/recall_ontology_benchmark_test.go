//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func BenchmarkRecallOntology(b *testing.B) {
	for _, workload := range []string{"grouping", "discovery", "ordinary_fallback"} {
		b.Run(workload, func(b *testing.B) {
			f := newRecallOntologyFixture(b)
			handles := []ontology.SourceHandle{}
			meanings := map[string]string{}
			for i := range 96 {
				text := fmt.Sprintf("Project %d stores its data in PostgreSQL.", i)
				if i < 12 {
					text = "Atlas stores its data in PostgreSQL."
					if i%2 == 1 {
						text = "PostgreSQL stores Atlas's data."
					}
				}
				handle := f.evidence(b, i%2, text, nil)
				if i < 12 {
					handles = append(handles, handle)
					meanings[text] = "atlas-storage"
				}
			}
			if workload != "ordinary_fallback" {
				producer, _ := f.organizer(b, meanings)
				_, err := producer.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
				require.NoError(b, err)
			}
			query := "Atlas PostgreSQL"
			if workload == "discovery" {
				query = "database"
			}
			for _, mode := range []string{"disabled", "enabled"} {
				b.Run(mode, func(b *testing.B) {
					input := RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: query, QueryEmbedding: []float32{1, 0, 0}, Limit: 10, OrganizationEnabled: mode == "enabled"}
					ctx := f.actor(2, "member")
					for range 20 {
						_, err := f.search.RecallEvidence(ctx, input)
						require.NoError(b, err)
					}
					durations := make([]time.Duration, b.N)
					b.ReportAllocs()
					b.ResetTimer()
					for i := range b.N {
						started := time.Now()
						_, err := f.search.RecallEvidence(ctx, input)
						durations[i] = time.Since(started)
						require.NoError(b, err)
					}
					b.StopTimer()
					sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
					if len(durations) > 0 {
						b.ReportMetric(float64(durations[(len(durations)-1)*95/100].Nanoseconds()), "p95-ns/op")
					}
				})
			}
		})
	}
}
