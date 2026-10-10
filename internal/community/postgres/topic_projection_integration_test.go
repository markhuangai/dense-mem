//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	community "github.com/markhuangai/dense-mem/internal/community/contract"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCommunityTopicPublicationAndQuality(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.setMode(t, false)
	baseline, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 20})
	require.NoError(t, err)
	baselineSources := map[string]bool{}
	baselineBad := 0
	allowed := map[string]bool{}
	for _, source := range f.sources {
		allowed[source.RelationshipID] = true
	}
	for _, record := range baseline {
		for _, relationship := range record.Relationships {
			baselineSources[relationship.RelationshipID] = true
			if !allowed[relationship.RelationshipID] {
				baselineBad++
			}
		}
	}
	f.setMode(t, true)
	canonical := f.canonicalSnapshot(t)
	f.seedCohort(t)
	require.Equal(t, 3, f.drain(t, 10))
	ids := f.currentIDs(t)
	require.Len(t, ids, 3)
	require.Equal(t, canonical, f.canonicalSnapshot(t))
	records, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 20})
	require.NoError(t, err)
	require.Len(t, records, 3)
	indices := map[string]int{}
	for index, source := range f.sources {
		indices[source.RelationshipID] = index
	}
	keys := map[string]string{}
	for key, id := range f.topics {
		keys[id] = key
	}
	actual := map[string][]int{}
	for _, record := range records {
		key := keys[record.LogicalCommunityID]
		require.NotEmpty(t, key)
		require.Equal(t, ids[record.LogicalCommunityID], record.CommunityID)
		require.Equal(t, record.RelationshipCount, len(record.Relationships))
		require.False(t, record.RelationshipsTruncated)
		require.Contains(t, record.Summary, "Entities:")
		for _, relationship := range record.Relationships {
			index, known := indices[relationship.RelationshipID]
			require.True(t, known, "unrelated relationship is a Bad@K failure")
			require.Contains(t, relationship.EvidenceIDs, f.evidenceIDs[index])
			actual[key] = append(actual[key], index)
		}
	}
	judgments := evalharness.CommunityTopicCohort()
	score := evalharness.ScoreCommunityTopics(judgments, actual)
	require.Equal(t, 1.0, score.SourcePreservation)
	require.Equal(t, 1.0, score.DistinctFactCoverage)
	require.Zero(t, score.FalseConsolidations)
	require.LessOrEqual(t, score.BadAtK, baselineBad)
	for _, limit := range []int{1, 3, 5, 20, 100} {
		preview, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 100, RelationshipLimit: limit})
		require.NoError(t, err)
		for _, record := range preview {
			require.LessOrEqual(t, len(record.Relationships), min(limit, 20))
			require.Equal(t, record.RelationshipCount > len(record.Relationships), record.RelationshipsTruncated)
		}
	}
	require.Zero(t, f.drain(t, 5))
	require.Equal(t, ids, f.currentIDs(t))
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE community_topic_versions SET version=version+1 WHERE team_id=?::uuid AND dependency_key=?`, f.teamID, "relationship:"+f.sources[0].RelationshipID).Error
	}))
	require.Equal(t, 1, f.drain(t, 5))
	require.Equal(t, ids, f.currentIDs(t), "unchanged content must reuse its published projection")
	status, err := f.service.Status(context.Background(), f.teamID)
	require.NoError(t, err)
	require.Equal(t, 3, status.ProjectionCoverage.CurrentTopics)
	require.Zero(t, status.ProjectionCoverage.PendingTopics)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		var attempts int
		if err := tx.Raw(`SELECT count(*) FROM community_summary_attempts WHERE team_id=?::uuid`, f.teamID).Row().Scan(&attempts); err != nil {
			return err
		}
		require.Zero(t, attempts, "topic projection added a model summarization attempt")
		return nil
	}))
	data, err := json.Marshal(judgments)
	require.NoError(t, err)
	digest := sha256.Sum256(data)
	var lock struct {
		CohortSHA256    string `json:"cohort_sha256"`
		GeneratorSHA256 string `json:"generator_sha256"`
	}
	lockData, err := os.ReadFile("../../../tests/eval/source_locks/community_topics_v1.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(lockData, &lock))
	require.Equal(t, lock.CohortSHA256, "sha256:"+hex.EncodeToString(digest[:]))
	generator, err := os.ReadFile("../../evalharness/community_cohort.go")
	require.NoError(t, err)
	generatorDigest := sha256.Sum256(generator)
	require.Equal(t, lock.GeneratorSHA256, "sha256:"+hex.EncodeToString(generatorDigest[:]))
	if directory := os.Getenv("DENSE_MEM_ORGANIZATION_REPORT_DIR"); directory != "" {
		report := map[string]any{"schema_version": "dense-mem.community.topic_comparison.v1", "issue": 245, "cohort_sha256": lock.CohortSHA256,
			"source_count": len(f.sources), "baseline_retained_source_count": len(baselineSources), "candidate": score,
			"baseline_bad_at_k": baselineBad, "candidate_bad_at_k": score.BadAtK, "projection_summary_provider_calls": 0, "verified": !t.Failed()}
		encoded, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(directory, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "community-topic-comparison.json"), append(encoded, '\n'), 0600))
	}
}

func TestCommunityTopicRetractionIsolationAndIndependentRefresh(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	f.drain(t, 10)
	before := f.currentIDs(t)
	port := knowledge.NewStore(f.appDB, f.rls, knowledge.ConflictRuntimeConfig{})
	_, err := port.RetractEvidence(f.actor(f.reader), knowledge.RetractEvidenceInput{TeamID: f.teamID, OwnerProfileID: f.reader,
		EvidenceIDs: []string{f.evidenceIDs[0]}, Reason: "wrong owner", IdempotencyKey: uuid.NewString(), RequestHash: "sha256:" + strings.Repeat("0", 64)})
	require.ErrorIs(t, err, knowledge.ErrEvidenceLifecycleNotFound)
	require.Equal(t, before, f.currentIDs(t))
	_, err = f.store.ClaimTopicProjection(f.actor(f.reader), time.Now())
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	_, err = f.store.TopicProjectionCoverage(f.actor(f.reader), f.otherTeam)
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	f.retract(t, 0)
	records, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 20})
	require.NoError(t, err)
	require.Len(t, records, 2, "one stale topic hid healthy topics")
	coverage, err := f.store.TopicProjectionCoverage(f.actor(f.reader), f.teamID)
	require.NoError(t, err)
	require.Equal(t, 1, coverage.StaleTopics)
	require.Equal(t, 2, coverage.CurrentTopics)
	f.drain(t, 10)
	after := f.currentIDs(t)
	require.NotEqual(t, before[f.topics["runtime"]], after[f.topics["runtime"]])
	require.Equal(t, before[f.topics["storage"]], after[f.topics["storage"]])
	require.Equal(t, before[f.topics["maintenance"]], after[f.topics["maintenance"]])
	records, err = f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 20})
	require.NoError(t, err)
	for _, record := range records {
		for _, relationship := range record.Relationships {
			require.NotContains(t, relationship.EvidenceIDs, f.evidenceIDs[0])
		}
	}
	history, err := f.store.GetCommunity(f.actor(f.reader), CommunityGetInput{TeamID: f.teamID, CommunityID: before[f.topics["runtime"]]})
	require.NoError(t, err)
	require.Equal(t, "superseded", history.Status)
	require.Equal(t, f.topics["runtime"], history.LogicalCommunityID)
}

func TestCommunityTopicPublicationFencesSourceChangesAndCompetingClaims(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	work, err := f.store.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.NotNil(t, work)
	index := -1
	for i, source := range f.sources {
		for _, input := range work.Inputs {
			if source.RelationshipID == input.RelationshipID {
				index = i
				break
			}
		}
	}
	require.NotEqual(t, -1, index)
	f.retract(t, index)
	err = f.store.AppendTopicProjection(context.Background(), community.TopicProjectionBatch{Work: *work}, time.Now())
	require.ErrorIs(t, err, community.ErrCommunitySourceStale)
	require.Empty(t, f.currentIDs(t))
	require.NoError(t, f.store.FailTopicProjection(context.Background(), *work, "source_changed", time.Now()))
	f.drain(t, 10)
	require.Len(t, f.currentIDs(t), 3)
	current := f.currentIDs(t)
	definition, err := f.ontology.GetRecord(context.Background(), f.teamID, work.TopicID, 0)
	require.NoError(t, err)
	definition.Definition.Description = "Changed community snapshot marker"
	f.publish(t, ontology.Change{ExpectedVersion: definition.Version, Record: definition.Record})
	work, err = f.store.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.NotNil(t, work)
	f.expireLease(t, work.CommunityID)
	replacement, err := f.store.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.NotNil(t, replacement)
	require.Equal(t, work.CommunityID, replacement.CommunityID)
	err = f.store.AppendTopicProjection(context.Background(), community.TopicProjectionBatch{Work: *work}, time.Now())
	require.ErrorIs(t, err, community.ErrCommunityRunAlreadyClaimed)
	require.NoError(t, f.store.FailTopicProjection(context.Background(), *replacement, "interrupted", time.Now()))
	f.drain(t, 10)
	require.NotEqual(t, current[work.TopicID], f.currentIDs(t)[work.TopicID])
}

func TestCommunityTopicFailurePauseAndLegacySwitch(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	work, err := f.store.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.NoError(t, f.store.FailTopicProjection(context.Background(), *work, "projection_failed", time.Now()))
	f.drain(t, 10)
	coverage, err := f.store.TopicProjectionCoverage(f.actor(f.reader), f.teamID)
	require.NoError(t, err)
	require.Equal(t, 1, coverage.FailedTopics)
	require.Equal(t, 2, coverage.CurrentTopics)
	records, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 20})
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_state SET paused=true WHERE singleton`).Error
	}))
	progress, err := f.service.RunProjectionTurn(context.Background())
	require.NoError(t, err)
	require.False(t, progress)
	paused, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 20})
	require.NoError(t, err)
	require.Equal(t, communityOwnershipIDs(records), communityOwnershipIDs(paused))
	err = f.store.PublishCommunitySnapshot(context.Background(), f.publication)
	require.ErrorIs(t, err, ontology.ErrMaintenanceDisabled)
	f.setMode(t, false)
	legacy, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 20})
	require.NoError(t, err)
	for _, record := range legacy {
		for _, topicID := range f.topics {
			require.NotEqual(t, topicID, record.LogicalCommunityID)
		}
	}
	require.NotEmpty(t, legacy)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		var count int
		if err := tx.Raw(`SELECT count(*) FROM community_records WHERE team_id=?::uuid AND status='current' AND topic_id IS NOT NULL`, f.teamID).Row().Scan(&count); err != nil {
			return err
		}
		require.Equal(t, 2, count)
		return nil
	}))
}

func BenchmarkCommunityTopicProjection(b *testing.B) {
	f := newTopicProjectionFixture(b)
	f.seedCohort(b)
	f.drain(b, 10)
	for _, mode := range []string{"legacy", "ontology"} {
		b.Run(mode, func(b *testing.B) {
			f.setMode(b, mode == "ontology")
			input := CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 3, RelationshipLimit: 5}
			for range 20 {
				_, err := f.store.RecallCommunities(f.actor(f.reader), input)
				require.NoError(b, err)
			}
			durations := make([]time.Duration, b.N)
			b.ResetTimer()
			for i := range b.N {
				start := time.Now()
				records, err := f.store.RecallCommunities(f.actor(f.reader), input)
				durations[i] = time.Since(start)
				require.NoError(b, err)
				require.NotEmpty(b, records)
			}
			b.StopTimer()
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			if len(durations) > 0 {
				b.ReportMetric(float64(durations[(len(durations)-1)*95/100].Nanoseconds()), "p95-ns/op")
			}
		})
	}
}
