package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestDurableUsageUnknownDoesNotBecomeZeroOrAnUnfinishedAttempt(t *testing.T) {
	now := time.Now()
	attempt := NativeAttempt{OperationKey: "maintenance:" + uuid.NewString(), AssessmentID: uuid.NewString(), Number: 1, Terminal: true, ReconciledAt: &now}
	measurement, err := MeasureAttempts([]NativeAttempt{attempt})
	require.NoError(t, err)
	require.Equal(t, 1, measurement.UnknownUsage)
	require.Zero(t, measurement.Unfinished)
	_, err = MeasureAttempts([]NativeAttempt{attempt, attempt})
	require.ErrorContains(t, err, "duplicated")
	attempt.ReconciledAt = nil
	measurement, err = MeasureAttempts([]NativeAttempt{attempt})
	require.NoError(t, err)
	require.Equal(t, 1, measurement.Unfinished)
}

func TestRunnerGuardRejectsLocalExecutionAndReruns(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	require.ErrorContains(t, RequireRunner(), "local execution")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	t.Setenv("GITHUB_RUN_ID", "42")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	require.NoError(t, RequireRunner())
	t.Setenv("GITHUB_RUN_ATTEMPT", "2")
	require.ErrorContains(t, RequireRunner(), "reruns")
}

func TestNativeReadinessRejectsUndrainedWorkAndCapacityDrift(t *testing.T) {
	manifest := Manifest{Model: "synthetic", ContextWindowTokens: 500000, EmbeddingModel: "text-embedding-3-small", EmbeddingDimensions: 1536}
	now := time.Now()
	observation := NativeObservation{Ontology: ontology.MaintenanceStatus{Enabled: true, Paused: true, DiscoveryComplete: true, CoverageComplete: true, Counts: ontology.MaintenanceCounts{Eligible: 1, Organized: 1},
		Window: &ontology.MaintenanceWindow{Policy: domain.OntologyMaintenanceConfig{Model: manifest.Model, ContextWindowTokens: 500000, MaxConcurrency: 4, TeamMaxConcurrency: 4}}},
		ProviderAttempts: []NativeAttempt{{OperationKey: "operation", AssessmentID: "assessment", Number: 1, Terminal: true, ReconciledAt: &now, UsageAvailable: true, Input: 2, Output: 3, Total: 5}}}
	require.NoError(t, json.Unmarshal([]byte(`{"search":{"status":"converged","expected_documents":1,"current_documents":1,"contract":{"model":"text-embedding-3-small","dimensions":1536}}}`), &observation))
	require.NoError(t, NativeReady(observation, true, true, manifest, "team"))
	for name, change := range map[string]func(*NativeObservation){
		"foreign source":       func(o *NativeObservation) { o.ForeignSources = 1 },
		"foreign work":         func(o *NativeObservation) { o.ForeignWork = 1 },
		"search drift":         func(o *NativeObservation) { o.Search.Drifted = 1 },
		"embedding identity":   func(o *NativeObservation) { o.Search.Contract.Model = "different" },
		"embedding dimensions": func(o *NativeObservation) { o.Search.Contract.Dimensions = 1024 },
		"active batch":         func(o *NativeObservation) { o.ActiveBatches = 1 },
		"unreconciled":         func(o *NativeObservation) { o.ProviderAttempts[0].ReconciledAt = nil },
		"negative usage":       func(o *NativeObservation) { o.ProviderAttempts[0].Input = -1 },
		"ontology state":       func(o *NativeObservation) { o.Ontology.Paused = false },
		"coverage incomplete":  func(o *NativeObservation) { o.Ontology.CoverageComplete = false },
		"empty ontology":       func(o *NativeObservation) { o.Ontology.Counts = ontology.MaintenanceCounts{} },
		"model drift":          func(o *NativeObservation) { o.Ontology.Window.Policy.Model = "different" },
		"context drift":        func(o *NativeObservation) { o.Ontology.Window.Policy.ContextWindowTokens-- },
		"team override": func(o *NativeObservation) {
			o.Ontology.Window.Policy.TeamConcurrencyOverrides = []domain.OntologyTeamConcurrencyOverride{{TeamID: "team", MaxConcurrency: 1}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(observation)
			require.NoError(t, err)
			var changed NativeObservation
			require.NoError(t, json.Unmarshal(body, &changed))
			change(&changed)
			require.Error(t, NativeReady(changed, true, true, manifest, "team"))
		})
	}
	attempts := append([]NativeAttempt(nil), observation.ProviderAttempts...)
	attempts[0].Total = 8
	measurement, err := MeasureAttempts(attempts)
	require.NoError(t, err)
	require.Equal(t, 1, measurement.PartialUsage)
	require.EqualValues(t, 2, measurement.Input)
	require.EqualValues(t, 3, measurement.Output)
	require.EqualValues(t, 8, measurement.Total)
}

func TestRunnerStartupFailureStillDrainsAndCapturesCompleteDenominators(t *testing.T) {
	const key = "startup-key-canary-7413"
	var logs bytes.Buffer
	capture := captureFixture(&logs)
	capture.Protector = NewRunnerCredentialProtector(key)
	var pauses atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pauses.Add(1)
		require.Equal(t, "/control/api/ontology/pause", r.URL.Path)
		require.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := &HTTPClient{Capture: capture, Client: server.Client()}
	collected := false
	native := &Native{Scope: Scope{TeamID: "invalid"}, Client: client, ControlURL: server.URL, ControlToken: key,
		RuntimeLogs: func(ctx context.Context) error {
			require.NoError(t, ctx.Err(), "shutdown retains its own bound after work cancellation")
			collected = true
			return errors.New("capture unavailable " + key)
		}}
	manifest := Manifest{RunID: capture.RunID, ImportWorkers: 20, OutputDir: t.TempDir(), ModelDeadline: time.Now().Add(time.Minute)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := Run(ctx, manifest, []CorpusItem{{SourceDocID: "source", Content: "Public text."}}, []Question{{ID: "question", Query: "Public question?"}}, client, nil, native)
	require.ErrorContains(t, err, "invalid runner team identity")
	require.ErrorContains(t, err, "status 503")
	require.True(t, collected)
	require.EqualValues(t, 1, pauses.Load())
	require.False(t, report.Complete)
	require.False(t, report.CaptureComplete)
	require.Equal(t, 4, report.ExpectedAnswers)
	require.Zero(t, report.CompletedAnswers)
	require.Equal(t, "seed verification incomplete", report.Failure)
	body, err := os.ReadFile(filepath.Join(manifest.OutputDir, "scores.json"))
	require.NoError(t, err)
	require.NotContains(t, string(body), key)
	events := capturedEvents(t, logs.Bytes())
	require.Equal(t, "started", events[0].Kind)
	require.Equal(t, "finished", events[len(events)-1].Kind)
	for _, event := range events {
		require.NotContains(t, string(event.Payload), key)
	}
}
