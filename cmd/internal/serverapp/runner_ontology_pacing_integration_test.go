//go:build integration

package serverapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/recall"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

const (
	originalPacingEvidence  = "Obsidian cools."
	correctedPacingEvidence = "Fluorite fluoresces."
	pacingBatches           = 3
	pacingSamplesPerBatch   = 16
	pacingSampleInterval    = 150 * time.Millisecond
)

type ontologyPacingMeasurement struct {
	Mode              string  `json:"mode"`
	TickMS            int64   `json:"tick_ms"`
	Batches           int     `json:"batches"`
	SamplesPerTool    int     `json:"samples_per_tool"`
	RememberP95MS     float64 `json:"remember_p95_ms"`
	RecallP95MS       float64 `json:"recall_p95_ms"`
	CorrectionBatch   int     `json:"correction_batch"`
	CorrectionTopicMS int64   `json:"correction_to_current_topic_ms"`
	BacklogDrainMS    int64   `json:"backlog_drain_ms"`
}

func TestRunnerOntologyPacingUnderForegroundLoad(t *testing.T) {
	tick := time.Minute
	if os.Getenv("DENSE_MEM_CI_PRECHECK_RUN_ID") == "" {
		tick = 3 * time.Second
	}
	var measurements []ontologyPacingMeasurement
	for _, mode := range []string{"normal", "three_ticks"} {
		t.Run(mode, func(t *testing.T) {
			report := measureOntologyPacing(t, mode, tick)
			measurements = append(measurements, report)
			body, err := json.Marshal(report)
			require.NoError(t, err)
			t.Logf("ontology_pacing_measurement %s", body)
		})
	}
	if len(measurements) == 0 && !t.Failed() {
		t.Skip("PostgreSQL pacing fixtures are unavailable")
	}
	require.Len(t, measurements, 2)
	require.Equal(t, measurements[0].CorrectionBatch, measurements[1].CorrectionBatch)
}

func measureOntologyPacing(t *testing.T, mode string, tick time.Duration) ontologyPacingMeasurement {
	t.Helper()
	f := newRunnerServiceFixture(t)
	f.provider.failures = 0
	ctx, cancel := context.WithTimeout(context.Background(), 3*tick+time.Minute)
	defer cancel()
	client := f.native.Client
	drain := func() {
		var status ontology.MaintenanceStatus
		for range 8 {
			var err error
			status, err = f.maintenance.Status(ctx)
			require.NoError(t, err)
			if status.CoverageComplete {
				return
			}
			_, err = f.maintenance.RunTurn(ctx)
			require.NoError(t, err)
		}
		t.Fatalf("fixture did not reach complete Topic coverage: assignment=%+v placement=%+v", status.Counts, status.Communities)
	}
	batchCount := func() int {
		var count int
		require.NoError(t, f.native.DB.QueryRowContext(ctx,
			`SELECT count(*) FROM topic_maintenance_batches WHERE team_id=$1::uuid AND status='completed' AND community_topic_id IS NULL`,
			f.native.Scope.TeamID).Scan(&count))
		return count
	}

	original, err := rememberPacingEvidence(ctx, client, "team_shared", originalPacingEvidence, "pacing-mineral", "first", "")
	require.NoError(t, err)
	_, err = f.maintenance.Command(ctx, domain.TopicMaintenanceCommand{Action: "resume", OperationKey: uuid.NewString()}, "", "")
	require.NoError(t, err)
	drain()
	initialBatches := batchCount()
	require.Equal(t, 1, initialBatches)
	var originalRecall recall.RecallResult
	require.NoError(t, client.CallTool(ctx, "recall_memory", map[string]any{"query": originalPacingEvidence, "limit": 5, "topic_limit": 1}, &originalRecall))
	require.Len(t, originalRecall.RelatedTopics, 1)
	topicID := originalRecall.RelatedTopics[0].TopicID

	status, err := f.maintenance.Status(ctx)
	require.NoError(t, err)
	require.NotNil(t, status.Window)
	discover := func() {
		turn, err := f.ontology.ClaimMaintenanceTurn(ctx, status.Window.ID, time.Now(), time.Minute)
		require.NoError(t, err)
		require.NotNil(t, turn)
		_, err = f.ontology.DiscoverMaintenance(ctx, *turn, ontology.MaintenancePageSize)
		releaseErr := f.ontology.ReleaseMaintenanceTurn(ctx, *turn)
		require.NoError(t, errors.Join(err, releaseErr))
	}
	for index := range (pacingBatches - 1) * ontology.MaxOrganizationSources {
		text := fmt.Sprintf("Botanical entry %d describes Celandine blossoms.", index+1)
		_, err := rememberPacingEvidence(ctx, client, "team_shared", text, "", "", "")
		require.NoError(t, err)
		discover()
	}
	corrected, err := rememberPacingEvidence(ctx, client, "team_shared", correctedPacingEvidence, "pacing-mineral", "second", "first")
	require.NoError(t, err)
	correctedAt := time.Now()
	var current string
	require.NoError(t, f.native.DB.QueryRowContext(ctx, `SELECT updated.fragment_id::text
		FROM evidence_fragments AS original JOIN evidence_sources AS source USING(team_id,source_id)
		JOIN evidence_fragments AS updated ON updated.team_id=source.team_id AND updated.source_id=source.source_id
		 AND updated.source_revision_id=source.current_revision_id
		WHERE original.team_id=$1::uuid AND original.fragment_id=$2::uuid`, f.native.Scope.TeamID, original).Scan(&current))
	require.Equal(t, corrected, current)
	require.NotEqual(t, original, corrected)
	discover()
	start := time.Now()

	var rememberTimes, recallTimes []time.Duration
	group, foreground := errgroup.WithContext(ctx)
	group.Go(func() error {
		for sample := range pacingBatches * pacingSamplesPerBatch {
			delay := time.Duration(sample) * pacingSampleInterval
			if mode == "three_ticks" {
				delay = time.Duration(sample/pacingSamplesPerBatch)*tick + time.Duration(sample%pacingSamplesPerBatch)*pacingSampleInterval
			}
			if err := waitForPacingTime(foreground, start.Add(delay)); err != nil {
				return err
			}
			started := time.Now()
			text := fmt.Sprintf("Private measurement entry %d.", sample)
			if _, err := rememberPacingEvidence(foreground, client, "private", text, "", "", ""); err != nil {
				return err
			}
			rememberTimes = append(rememberTimes, time.Since(started))
			started = time.Now()
			if err := client.CallTool(foreground, "recall_memory", map[string]any{"query": correctedPacingEvidence, "limit": 5, "topic_limit": 3}, nil); err != nil {
				return err
			}
			recallTimes = append(recallTimes, time.Since(started))
		}
		return nil
	})
	defer func() {
		cancel()
		_ = group.Wait()
	}()

	var topicTime time.Duration
	var correctionBatch int
	for batch := range pacingBatches {
		if mode == "three_ticks" {
			require.NoError(t, waitForPacingTime(foreground, start.Add(time.Duration(batch)*tick)))
		}
		expected := initialBatches + batch + 1
		for range 8 {
			if batchCount() >= expected {
				break
			}
			progress, err := f.maintenance.RunTurn(foreground)
			require.NoError(t, err)
			require.True(t, progress, "a measurement batch is still pending")
		}
		require.Equal(t, expected, batchCount())
		if topicTime == 0 {
			var members recall.RecallResult
			err := client.CallTool(foreground, "recall_memory", map[string]any{"topic_ids": []string{topicID}, "limit": 10}, &members)
			var toolErr *evalharness.StructuredToolError
			if errors.As(err, &toolErr) && toolErr.Result["reason_code"] == "topic_unavailable" {
				continue
			}
			require.NoError(t, err)
			if slices.ContainsFunc(members.Results, func(item recall.RecallResultItem) bool { return item.EvidenceID == corrected }) {
				topicTime = time.Since(correctedAt)
				correctionBatch = batch + 1
			}
		}
	}
	drain()
	drainTime := time.Since(start)
	require.Equal(t, initialBatches+pacingBatches, batchCount())
	require.Positive(t, topicTime, "current topic follow-up must include the corrected Evidence")
	require.NoError(t, group.Wait())
	require.Len(t, rememberTimes, pacingBatches*pacingSamplesPerBatch)
	require.Len(t, recallTimes, len(rememberTimes))
	slices.Sort(rememberTimes)
	slices.Sort(recallTimes)
	p95 := (len(rememberTimes)*95 - 1) / 100
	return ontologyPacingMeasurement{
		Mode: mode, TickMS: tick.Milliseconds(), Batches: pacingBatches, SamplesPerTool: len(rememberTimes),
		RememberP95MS: float64(rememberTimes[p95].Microseconds()) / 1000, RecallP95MS: float64(recallTimes[p95].Microseconds()) / 1000,
		CorrectionBatch: correctionBatch, CorrectionTopicMS: topicTime.Milliseconds(), BacklogDrainMS: drainTime.Milliseconds(),
	}
}

func rememberPacingEvidence(ctx context.Context, client *evalharness.HTTPClient, space, text, key, revision, previous string) (string, error) {
	evidence := map[string]any{"content": text, "source_type": "document", "source": "ontology-pacing", "force_insert": true}
	if key != "" {
		evidence["source_key"], evidence["source_revision"] = key, revision
	}
	if previous != "" {
		evidence["previous_source_revision"] = previous
	}
	var result struct {
		ProcessingState string `json:"processing_state"`
		Evidence        []struct {
			ID string `json:"evidence_id"`
		} `json:"evidence"`
	}
	err := client.CallTool(ctx, "remember", map[string]any{
		"target_space": space, "idempotency_key": uuid.NewString(), "evidence": []any{evidence},
	}, &result)
	if err != nil {
		return "", err
	}
	if result.ProcessingState != "completed" || len(result.Evidence) != 1 || result.Evidence[0].ID == "" {
		return "", fmt.Errorf("pacing fixture did not store its Evidence")
	}
	return result.Evidence[0].ID, nil
}

func waitForPacingTime(ctx context.Context, date time.Time) error {
	timer := time.NewTimer(time.Until(date))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
