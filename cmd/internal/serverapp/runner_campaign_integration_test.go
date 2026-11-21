//go:build integration

package serverapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	ontologypg "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type runnerAnswerTransport func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error)

func (f runnerAnswerTransport) Complete(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return f(ctx, request)
}

func TestRunnerCampaignConditionsAndCapture(t *testing.T) {
	for _, scenario := range []string{"ordinary", "stale source", "rate limited", "mixed failure"} {
		t.Run(scenario, func(t *testing.T) { testRunnerCampaignConditionsAndCapture(t, scenario) })
	}
	t.Run("prepared ontology after window expiry", func(t *testing.T) { testPreparedOntology(t, "expiry") })
	t.Run("settled ambiguity blocks preparation", func(t *testing.T) { testPreparedOntology(t, "ambiguous") })
	t.Run("unclassified provider failure blocks preparation", func(t *testing.T) { testPreparedOntology(t, "provider failure") })
	t.Run("stale input is retried only once per source", func(t *testing.T) { testPreparedOntology(t, "stale twice") })
	t.Run("checkpoint resumes after repeated interrupted 429s beyond leases", func(t *testing.T) { testPreparedOntology(t, "resume") })
}

func testPreparedOntology(t *testing.T, scenario string) {
	var offset atomic.Int64
	var providerCalls atomic.Int64
	fixture := runnerPreparationFixture{now: func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }, ambiguous: scenario == "ambiguous"}
	stateDir := t.TempDir()
	if scenario == "resume" {
		var err error
		fixture.cooldown, err = organization.RestorePreparationCooldown(filepath.Join(stateDir, "until"))
		require.NoError(t, err)
	}
	var definitions []ontology.Change
	if scenario == "stale twice" {
		definitions = []ontology.Change{
			{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "postgresql", Label: "PostgreSQL"}}},
			{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "memory", Label: "Durable memory"}}},
		}
	}
	f := newRunnerServiceFixtureWithOntology(t, definitions, func(ctx context.Context, store *ontologypg.Store, actor requestctx.Actor, _ assessment.Request) (int, error) {
		calls := providerCalls.Add(1)
		if scenario == "provider failure" {
			return http.StatusUnauthorized, nil
		}
		if calls <= 2 && scenario == "resume" {
			return http.StatusTooManyRequests, nil
		}
		if scenario == "stale twice" && calls <= int64(len(definitions)) {
			actor.Role = "manager"
			record := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.PinDefinition, TargetID: definitions[calls-1].Record.ID}}
			_, err := store.PublishManager(requestctx.WithActor(ctx, actor), actor.TeamID.String(), ontology.Publication{
				OperationKey: uuid.NewString(), ExpectedRevision: calls, Reason: "Change an existing candidate context during assessment",
				Changes: []ontology.Change{{Record: record}},
			})
			return 0, err
		}
		return 0, nil
	}, fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	corpus := []evalharness.CorpusItem{{SourceDocID: "source-one", Content: "Project Alpha stores durable memory in PostgreSQL."}}
	manifest := f.manifest
	manifest.SeedDir, manifest.BaseRelease = t.TempDir(), "v2.8.1-rc.5"
	manifest.SourceRevision, manifest.RegistryHash = strings.Repeat("a", 40), strings.Repeat("c", 64)
	manifest.Image = "ghcr.io/markhuangai/dense-mem@sha256:" + strings.Repeat("b", 64)
	manifest.Dataset, manifest.ModelDeadline = "synthetic", time.Now().Add(230*time.Second)
	seed, err := evalharness.Prepare(ctx, manifest, corpus, f.native.Client, f.native)
	require.NoError(t, err)
	seed = runnerSealSyntheticSeed(t, manifest.SeedDir, seed)
	seed.Version = 2
	body, err := json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(manifest.SeedDir, "seed.json"), body, 0600))
	manifest.SeedImage = "sha256:" + strings.Repeat("d", 64)
	manifest.Mode, manifest.Operation, manifest.BakeStateDir = "fixtures", "bake-ontology", stateDir
	if scenario == "expiry" {
		off := manifest
		off.Condition, off.Operation, off.ImportWorkers = "off", "run", 0
		testFrozenConditionScoring(t, ctx, f, off, corpus, seed.CanonicalHash, 0, "")
		wrongScope := seed.Scope
		wrongScope.Generation++
		manifest.ResumeScope = &wrongScope
		require.ErrorContains(t, evalharness.BakeOntology(ctx, manifest, corpus, f.native.Client, f.native), "checkpoint is not drained in its original scope")
		require.Zero(t, providerCalls.Load())
		manifest.ResumeScope = nil
	}
	var logs bytes.Buffer
	f.native.Client.Capture = &evalharness.Capture{RunID: "synthetic-prepare", Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: evalharness.NewRunnerCredentialProtector(f.native.ControlToken)}
	done := make(chan error, 1)
	turnCtx, stop := context.WithCancel(ctx)
	go func() {
		for turnCtx.Err() == nil {
			_, err := f.maintenance.RunTurn(turnCtx)
			if scenario == "provider failure" && errors.Is(err, modelprovider.ErrVerifierProvider) {
				continue
			}
			if scenario == "stale twice" && errors.Is(err, ontology.ErrSourceStale) {
				continue
			}
			if err != nil && !errors.Is(err, ontology.ErrMaintenancePaused) && !errors.Is(err, modelprovider.ErrVerifierRateLimit) && turnCtx.Err() == nil {
				done <- err
				return
			}
			select {
			case <-turnCtx.Done():
			case <-time.After(10 * time.Millisecond):
			}
		}
		done <- nil
	}()
	var joined bool
	defer func() {
		if !joined {
			stop()
			require.NoError(t, <-done)
		}
	}()
	if scenario == "resume" {
		originalDeadline := manifest.ModelDeadline
		interruption, interrupt := context.WithCancel(ctx)
		defer interrupt()
		retryPath := filepath.Join(stateDir, "recovery.json")
		type retry struct {
			Origin string `json:"origin"`
			Run    string `json:"run"`
			Key    string `json:"key"`
		}
		receipt := make(chan retry, 1)
		go func() {
			for interruption.Err() == nil {
				var saved []retry
				body, err := os.ReadFile(retryPath)
				if err == nil && json.Unmarshal(body, &saved) == nil && len(saved) == 1 && saved[0].Run != "" {
					receipt <- saved[0]
					interrupt()
					return
				}
				select {
				case <-interruption.Done():
				case <-time.After(20 * time.Millisecond):
				}
			}
		}()
		require.Error(t, evalharness.BakeOntology(interruption, manifest, corpus, f.native.Client, f.native))
		select {
		case saved := <-receipt:
			require.NotEmpty(t, saved.Key)
			require.True(t, f.snapshot(t).Ontology.Paused)
			require.LessOrEqual(t, providerCalls.Load(), int64(2))
			// Resume from the persisted intent when the response was lost after native commit.
			body, err := os.ReadFile(retryPath)
			require.NoError(t, err)
			var intents []map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(body, &intents))
			require.Len(t, intents, 1)
			delete(intents[0], "run")
			body, err = json.Marshal(intents)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(retryPath, body, 0600))
			manifest.ResumeScope = &seed.Scope
			require.Equal(t, originalDeadline, manifest.ModelDeadline)
			require.NoError(t, evalharness.BakeOntology(ctx, manifest, corpus, f.native.Client, f.native))
			body, err = os.ReadFile(retryPath)
			require.NoError(t, err)
			var recovered []retry
			require.NoError(t, json.Unmarshal(body, &recovered))
			page, err := f.maintenance.Runs(ctx, "", ontology.MaxPageSize)
			require.NoError(t, err)
			runs := make(map[string]ontology.MaintenanceRun, len(page.Runs))
			for _, run := range page.Runs {
				runs[run.ID] = run
			}
			require.GreaterOrEqual(t, len(recovered), 2)
			require.LessOrEqual(t, len(recovered), 3)
			require.Equal(t, saved, recovered[0])
			attempts := f.snapshot(t).ProviderAttempts
			require.Len(t, attempts, 3)
			require.EqualValues(t, len(attempts), providerCalls.Load())
			slices.SortFunc(attempts, func(a, b evalharness.NativeAttempt) int { return a.AdmittedAt.Compare(b.AdmittedAt) })
			attemptRuns := make([]string, len(attempts))
			for i, attempt := range attempts {
				batchID := strings.TrimPrefix(attempt.OperationKey, "maintenance:")
				require.NoError(t, f.admin.WithContext(ctx).Raw(`SELECT run_id::text FROM ontology_maintenance_batches
					WHERE batch_id=?::uuid AND team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`,
					batchID, seed.Scope.TeamID, seed.Scope.SpaceID, seed.Scope.Generation).Row().Scan(&attemptRuns[i]))
			}
			require.Equal(t, saved.Origin, attemptRuns[0])
			completed := recovered[len(recovered)-1]
			require.Equal(t, attemptRuns[1], completed.Origin)
			require.Equal(t, attemptRuns[2], completed.Run)
			require.Equal(t, completed.Key, runs[completed.Run].OperationKey)
			require.Equal(t, "completed", runs[completed.Run].Status)
			for _, prior := range recovered[:len(recovered)-1] {
				state := runs[prior.Run]
				require.Equal(t, prior.Key, state.OperationKey)
				require.Equal(t, "incomplete", state.Status)
				require.Equal(t, saved.Origin, prior.Origin)
				if prior.Run == attemptRuns[1] {
					require.Equal(t, "provider_unavailable", state.FailureCode)
				} else {
					// Pause may interrupt admission after the retry has claimed a batch.
					require.Contains(t, []string{"maintenance_paused", "window_closed"}, state.FailureCode)
					require.NotContains(t, attemptRuns, prior.Run)
				}
			}
			require.Equal(t, []string{"provider_unavailable", "provider_unavailable", "completed"},
				[]string{attempts[0].Outcome, attempts[1].Outcome, attempts[2].Outcome})
			require.GreaterOrEqual(t, attempts[1].AdmittedAt.Sub(attempts[0].AdmittedAt), 59*time.Second)
			require.GreaterOrEqual(t, attempts[2].AdmittedAt.Sub(attempts[1].AdmittedAt), 59*time.Second)
			return
		default:
			t.Fatal("preparation did not persist the first native retry")
		}
	}
	err = evalharness.BakeOntology(ctx, manifest, corpus, f.native.Client, f.native)
	stop()
	joined = true
	require.NoError(t, <-done)
	meta := filepath.Join(manifest.BakeStateDir, "ontology-prepared.json")
	if scenario == "ambiguous" {
		require.ErrorContains(t, err, "unresolved ambiguous")
		require.NoFileExists(t, meta)
		require.Contains(t, logs.String(), "ontology_preparation_ambiguous")
		require.True(t, f.snapshot(t).Ontology.Paused)
		return
	}
	if scenario == "provider failure" {
		require.ErrorContains(t, err, "failure has no supported recovery")
		require.NoFileExists(t, meta)
		require.NoFileExists(t, filepath.Join(stateDir, "recovery.json"))
		require.Contains(t, logs.String(), "ontology_failed_sources")
		require.True(t, f.snapshot(t).Ontology.Paused)
		require.Equal(t, int64(1), providerCalls.Load())
		return
	}
	if scenario == "stale twice" {
		require.ErrorContains(t, err, "source remained stale after recovery")
		require.NoFileExists(t, meta)
		require.Contains(t, logs.String(), "context_hash")
		require.Contains(t, logs.String(), "ontology_recovery_finished")
		require.True(t, f.snapshot(t).Ontology.Paused)
		require.Equal(t, int64(2), providerCalls.Load())
		return
	}
	require.NoError(t, err)
	body, err = os.ReadFile(meta)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &seed))
	require.Positive(t, seed.Ontology.Counts.Eligible)
	require.Equal(t, seed.Ontology.Counts.Eligible, seed.Ontology.Counts.Organized)
	require.Equal(t, 8, seed.Ontology.Workers)
	seed.Version = 2
	body, err = json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(manifest.SeedDir, "seed.json"), body, 0600))
	manifest.Condition, manifest.Operation, manifest.ParentSeedImage = "on", "run", manifest.SeedImage
	manifest.SeedImage = "sha256:" + strings.Repeat("e", 64)
	offset.Store(int64(48 * time.Hour))
	_, _, _, restored, err := evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, f.native)
	require.NoError(t, err)
	require.Nil(t, restored.Ontology.Window)
	require.Equal(t, seed.CanonicalHash, restored.CanonicalHash)
	require.EqualValues(t, seed.Ontology.Usage.Attempts, len(restored.ProviderAttempts))
	manifest.ImportWorkers = 0
	for _, interruption := range []string{"", "answer", "recall", "drift", "capture", "shutdown"} {
		testFrozenConditionScoring(t, ctx, f, manifest, corpus, seed.CanonicalHash, len(restored.ProviderAttempts), interruption)
	}
	manifest.ParentSeedImage = "sha256:" + strings.Repeat("f", 64)
	_, _, _, _, err = evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, f.native)
	require.ErrorContains(t, err, "parent differs")
	manifest.ParentSeedImage = seed.Ontology.ParentImage
	seed.Ontology.Fingerprint = strings.Repeat("0", 64)
	body, err = json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(manifest.SeedDir, "seed.json"), body, 0600))
	_, _, _, _, err = evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, f.native)
	require.ErrorContains(t, err, "differ from the preparation receipt")
}

func testFrozenConditionScoring(t *testing.T, ctx context.Context, fixture *runnerServiceFixture, manifest evalharness.Manifest, corpus []evalharness.CorpusItem, canonicalHash string, ontologyAttempts int, interruption string) {
	t.Helper()
	manifest.RunID = "synthetic-" + manifest.Condition
	manifest.OutputDir = t.TempDir()
	var logs bytes.Buffer
	client := fixture.native.Client
	client.Capture = &evalharness.Capture{RunID: manifest.RunID, Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: evalharness.NewRunnerCredentialProtector(fixture.native.ControlToken)}
	if interruption == "capture" {
		fixture.native.RuntimeLogs = func(context.Context) error {
			_, err := os.ReadFile(filepath.Join(manifest.OutputDir, "terminal-server.log"))
			return err
		}
		defer func() { fixture.native.RuntimeLogs = nil }()
	}
	if interruption == "shutdown" {
		fixture.native.StopRuntime = func(_ context.Context, cutoff bool) error {
			require.False(t, cutoff)
			return context.DeadlineExceeded
		}
		fixture.native.RuntimeLogs = func(context.Context) error {
			t.Error("terminal logs require confirmed server exit")
			return nil
		}
		defer func() { fixture.native.StopRuntime, fixture.native.RuntimeLogs = nil, nil }()
	}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	if interruption == "recall" {
		underlying := client.Client.Transport
		defer func() { client.Client.Transport = underlying }()
		client.Client.Transport = seedRoundTripper(func(req *http.Request) (*http.Response, error) {
			response, err := underlying.RoundTrip(req)
			if req.URL.Path == "/mcp" {
				cancel()
			}
			return response, err
		})
	}
	var calls atomic.Int32
	transport := runnerAnswerTransport(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		call := calls.Add(1)
		switch {
		case call == 2 && interruption == "answer":
			cancel()
			return modelprovider.StructuredResult{}, context.Canceled
		case call == 1 && interruption == "drift":
			if err := fixture.native.Control(work, http.MethodPost, "/ontology/resume", map[string]string{"operation_key": uuid.NewString()}, nil); err != nil {
				return modelprovider.StructuredResult{}, err
			}
		}
		return modelprovider.StructuredResult{Content: `{"action":"answer","query":"","topic_id":"","evidence_id":"","answer":"PostgreSQL","citations":[]}`}, nil
	})
	questions := []evalharness.Question{{ID: "question-one", Query: "Where does Project Alpha store durable memory?", Answers: []string{"PostgreSQL"}, GoldSources: []string{"source-one"}}}
	report, err := evalharness.Run(work, manifest, corpus, questions, client, transport, fixture.native)
	switch interruption {
	case "answer", "recall":
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, report.Complete)
		require.False(t, report.ModelVerified)
		if interruption == "answer" {
			require.Equal(t, 1, report.CompletedAnswers)
		} else {
			require.Zero(t, report.CompletedAnswers)
			require.Zero(t, calls.Load())
		}
		require.Contains(t, logs.String(), `"kind":"campaign_failed"`)
	case "drift":
		require.ErrorContains(t, err, "restored ontology is not complete and paused")
		require.False(t, report.Complete)
		require.False(t, report.ModelVerified)
		require.Equal(t, 2, report.CompletedAnswers)
		require.NoError(t, fixture.native.Pause(ctx))
	case "capture":
		require.ErrorIs(t, err, os.ErrNotExist)
		require.Equal(t, "log capture incomplete", report.Failure)
		require.True(t, report.ModelVerified)
		require.False(t, report.CaptureComplete)
		require.False(t, report.Complete)
		require.Equal(t, 2, report.CompletedAnswers)
	case "shutdown":
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, "runtime shutdown incomplete", report.Failure)
		require.True(t, report.ModelVerified)
		require.False(t, report.CaptureComplete)
		require.False(t, report.Complete)
		require.Equal(t, 2, report.CompletedAnswers)
	default:
		require.NoError(t, err)
		require.True(t, report.Complete)
		require.True(t, report.ModelVerified)
		require.True(t, report.CaptureComplete)
		require.Equal(t, 2, report.CompletedAnswers)
		require.Len(t, report.NativeMeasurements, 2)
		require.Contains(t, report.Timings, "scoring_through_flush")
		for _, score := range report.Scores {
			require.Equal(t, float64(1), score.EM)
		}
	}
	if interruption != "recall" {
		require.Equal(t, int32(2), calls.Load())
		require.Equal(t, 1, strings.Count(logs.String(), `"kind":"initial_recall"`))
	}
	require.Equal(t, canonicalHash, report.Identity.CanonicalHash)
	require.Equal(t, 2, report.ExpectedAnswers)
	require.FileExists(t, filepath.Join(manifest.OutputDir, "scores.json"))
	require.Contains(t, logs.String(), `"kind":"finished"`)
	require.NotContains(t, logs.String(), fixture.native.ControlToken)
	require.Len(t, fixture.snapshot(t).ProviderAttempts, ontologyAttempts)
}

func testRunnerCampaignConditionsAndCapture(t *testing.T, scenario string) {
	var organizations atomic.Int32
	topicID := uuid.NewString()
	var initial []ontology.Change
	if scenario == "stale source" {
		initial = []ontology.Change{{Record: ontology.Record{ID: topicID, Kind: ontology.Topic, Definition: &ontology.Definition{Key: "postgresql", Label: "PostgreSQL"}}}}
	}
	f := newRunnerServiceFixtureWithOntology(t, initial, func(ctx context.Context, store *ontologypg.Store, actor requestctx.Actor, _ assessment.Request) (int, error) {
		number := organizations.Add(1)
		switch {
		case scenario == "stale source" && number == 1:
			actor.Role = "manager"
			// A new pin changes the assessment context without requeueing the source through vocabulary discovery.
			pin := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.PinDefinition, TargetID: topicID}}
			_, err := store.PublishManager(requestctx.WithActor(ctx, actor), actor.TeamID.String(), ontology.Publication{
				OperationKey: "runner-concurrent-pin", ExpectedRevision: 1, Reason: "Pin the candidate topic during assessment",
				Changes: []ontology.Change{{Record: pin}},
			})
			return 0, err
		case (scenario == "rate limited" || scenario == "mixed failure") && number == 1:
			return http.StatusTooManyRequests, nil
		case scenario == "mixed failure":
			return http.StatusUnauthorized, nil
		default:
			return 0, nil
		}
	})
	if scenario == "rate limited" {
		f.native.Now = func() time.Time { return time.Now().Add(5 * time.Minute) }
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, f.native.Control(ctx, http.MethodPatch, "/config/ontology-maintenance", map[string]any{"items": []map[string]string{{"key": domain.AppConfigOntologyEnabled, "value": "false"}}}, nil))
	manifest := f.manifest
	manifest.RunID, manifest.Dataset = "native-runner", "synthetic"
	manifest.SourceRevision = strings.Repeat("a", 40)
	manifest.Image = "ghcr.io/markhuangai/dense-mem@sha256:" + strings.Repeat("b", 64)
	manifest.RegistryHash = strings.Repeat("c", 64)
	manifest.ModelDeadline, manifest.OutputDir = time.Now().Add(time.Minute), t.TempDir()
	var logs bytes.Buffer
	f.native.Client.Capture = &evalharness.Capture{RunID: manifest.RunID, Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: evalharness.NewRunnerCredentialProtector(f.native.Client.APIKey, f.native.ControlToken)}
	corpus := []evalharness.CorpusItem{{SourceDocID: "source-one", Content: "Project Alpha stores durable memory in PostgreSQL."}}
	if scenario == "mixed failure" {
		corpus = append(corpus, evalharness.CorpusItem{SourceDocID: "source-other", Content: "Jupiter attracts sixty moons across astronomical orbits."})
	}
	questions := []evalharness.Question{{ID: "question-one", Query: "Where does Project Alpha store durable memory?", Answers: []string{"PostgreSQL", "private-gold-answer-canary"}, GoldSources: []string{"source-one"}}}
	var calls atomic.Int32
	var initialPackets [4]evalharness.MemoryCall
	transport := runnerAnswerTransport(func(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		position := calls.Add(1) - 1
		if position >= int32(len(initialPackets)) {
			return modelprovider.StructuredResult{}, errors.New("unexpected extra answer call")
		}
		for _, message := range request.Messages {
			if strings.Contains(message.Content, "private-gold-answer-canary") {
				return modelprovider.StructuredResult{}, errors.New("gold answer leaked into provider input")
			}
		}
		var input struct {
			Calls []evalharness.MemoryCall `json:"memory_calls"`
		}
		if err := json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &input); err != nil || len(input.Calls) != 1 {
			return modelprovider.StructuredResult{}, errors.New("answer did not receive one initial memory packet")
		}
		initialPackets[position] = input.Calls[0]
		results, ok := input.Calls[0].Result["results"].([]any)
		if !ok || len(results) == 0 {
			return modelprovider.StructuredResult{}, errors.New("initial Recall delivered no Evidence")
		}
		first, ok := results[0].(map[string]any)
		if !ok {
			return modelprovider.StructuredResult{}, errors.New("initial Recall has invalid Evidence")
		}
		id, _ := first["evidence_id"].(string)
		body, err := json.Marshal(map[string]any{"action": "answer", "query": "", "topic_id": "", "evidence_id": "", "answer": "PostgreSQL", "citations": []evalharness.Citation{{EvidenceID: id, Quote: "PostgreSQL"}}})
		return modelprovider.StructuredResult{Content: string(body)}, err
	})
	runTurn := func(ctx context.Context) (bool, error) {
		progress, err := f.maintenance.RunTurn(ctx)
		switch {
		case errors.Is(err, ontology.ErrMaintenancePaused):
			return false, nil
		case scenario == "stale source" && errors.Is(err, ontology.ErrSourceStale),
			(scenario == "rate limited" || scenario == "mixed failure") && errors.Is(err, modelprovider.ErrVerifierRateLimit),
			scenario == "mixed failure" && errors.Is(err, modelprovider.ErrVerifierProvider):
			return true, nil
		default:
			return progress, err
		}
	}
	if scenario == "rate limited" {
		underlying := f.native.Client.Client.Transport
		f.native.Client.Client.Transport = seedRoundTripper(func(req *http.Request) (*http.Response, error) {
			response, err := underlying.RoundTrip(req)
			if err != nil || req.URL.Path != "/control/api/search/convergence" {
				return response, err
			}
			// Advance real maintenance after Snapshot has read ontology status and before it returns.
			for {
				progress, err := runTurn(req.Context())
				if err != nil {
					_ = response.Body.Close()
					return nil, err
				}
				if !progress {
					return response, nil
				}
			}
		})
	} else {
		worker := make(chan error, 1)
		go func() {
			for ctx.Err() == nil {
				if _, err := runTurn(ctx); err != nil && ctx.Err() == nil {
					worker <- err
					return
				}
				select {
				case <-ctx.Done():
				case <-time.After(10 * time.Millisecond):
				}
			}
			worker <- nil
		}()
		defer func() { cancel(); require.NoError(t, <-worker) }()
	}
	manifest.SeedDir, manifest.BaseRelease = t.TempDir(), "v2.8.1-rc.5"
	seed, err := evalharness.Prepare(ctx, manifest, corpus, f.native.Client, f.native)
	require.NoError(t, err)
	runnerSealSyntheticSeed(t, manifest.SeedDir, seed)
	manifest.ImportWorkers = 0
	providerCalls := f.provider.calls
	report, err := evalharness.Run(ctx, manifest, corpus, questions, f.native.Client, transport, f.native)
	require.Equal(t, providerCalls, f.provider.calls, "scoring never imports the corpus again")
	if scenario == "mixed failure" {
		require.ErrorContains(t, err, "no supported recovery")
		require.False(t, report.Complete)
		require.Equal(t, 2, report.CompletedAnswers)
		require.GreaterOrEqual(t, organizations.Load(), int32(2))
		require.Contains(t, logs.String(), "rate_limited")
		require.NotContains(t, logs.String(), "ontology_recovery_started")
		return
	}
	require.NoError(t, err)
	require.True(t, report.Complete)
	require.True(t, report.ModelVerified)
	require.Equal(t, 4, report.ExpectedAnswers)
	require.Equal(t, 4, report.CompletedAnswers)
	require.Equal(t, int32(4), calls.Load())
	require.Len(t, report.NativeMeasurements, 3)
	for _, observation := range report.NativeMeasurements {
		require.Equal(t, report.Identity.CanonicalHash, observation.CanonicalHash)
	}
	require.True(t, report.CaptureComplete)
	if scenario == "stale source" || scenario == "rate limited" {
		require.True(t, strings.Contains(logs.String(), `"kind":"ontology_recovery_started"`), "the runner must submit the native retry")
		require.True(t, strings.Contains(logs.String(), `"kind":"ontology_recovery_finished"`), "the runner must record the terminal native retry")
		require.False(t, strings.Contains(logs.String(), f.native.ControlToken), "the recovery log must protect the control credential")
		diagnosis := map[string]string{"stale source": `"freshness_check":"context_hash"`, "rate limited": `"attempt_failure":"rate_limited"`}[scenario]
		require.True(t, strings.Contains(logs.String(), diagnosis), "the recovery log must show the durable failure reason")
		require.GreaterOrEqual(t, organizations.Load(), int32(2))
	}
	require.Equal(t, initialPackets[0], initialPackets[1])
	require.Equal(t, initialPackets[2], initialPackets[3])
	conditions := []string{}
	for _, axis := range report.Matrix {
		conditions = append(conditions, axis.Condition)
	}
	require.Equal(t, []string{"off", "off", "on", "on"}, conditions)
	kinds := map[string]int{}
	staleRetryObservation := false
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var event evalharness.Event
		require.NoError(t, json.Unmarshal(line, &event))
		kinds[event.Kind]++
		if scenario == "rate limited" && event.Kind == "native_observation" {
			var observation evalharness.NativeObservation
			require.NoError(t, json.Unmarshal(event.Payload, &observation))
			status := observation.Ontology
			if run := status.LatestRun; run != nil && run.Kind == "retry" && run.Status == "pending" &&
				status.DiscoveryComplete && status.ActiveWork == 0 && status.Counts.Pending == 0 && status.Counts.Failed == 1 {
				staleRetryObservation = true
			}
		}
	}
	if scenario == "rate limited" {
		require.True(t, staleRetryObservation, "snapshot must still show the failure when the pending retry completes")
	}
	require.Equal(t, 1, kinds["seed_verified"])
	require.Zero(t, kinds["mapping"])
	require.Zero(t, kinds["canonical"])
}

func TestRunnerCampaignNativeMappingsFreezeAndRecall(t *testing.T) {
	f := newRunnerServiceFixture(t)
	ctx := context.Background()
	client := f.native.Client
	corpus := []evalharness.CorpusItem{{SourceDocID: "one", Content: "Project Alpha stores durable memory in PostgreSQL."}, {SourceDocID: "two", Content: "Project Beta stores durable memory in PostgreSQL."}, {SourceDocID: "unjudged", Content: "PostgreSQL stores durable memory for Project Alpha."}}
	mapping, err := client.ImportCorpusWithConcurrency(ctx, corpus, 1)
	require.NoError(t, err)
	require.Equal(t, 4, f.provider.calls, "one failed attempt is retained and recovered once")
	canonical, err := f.native.VerifyMappings(ctx, corpus, mapping)
	require.NoError(t, err)
	require.Len(t, mapping.Partitions, 3)
	require.Equal(t, mapping.Partitions["one"][0].EvidenceID, mapping.Partitions["unjudged"][0].EvidenceID)
	require.NotEqual(t, mapping.Partitions["one"][0].ContentHash, mapping.Partitions["unjudged"][0].ContentHash)
	require.Len(t, canonical, 2, "semantic reuse preserves an exact original source rather than inventing a second retrieved source")
	calls := f.provider.calls
	replayed, err := client.ImportCorpusWithConcurrency(ctx, corpus, 1)
	require.NoError(t, err)
	require.Equal(t, mapping, replayed)
	require.Equal(t, calls, f.provider.calls)

	ordinary := f.manifest
	ordinary.ImportWorkers, ordinary.OutputDir = 1, t.TempDir()
	f.provider.failures = 2
	withFailure := append([]evalharness.CorpusItem{{SourceDocID: "failed", Content: "The ordinary proof records this unavailable document."}}, corpus...)
	require.NoError(t, evalharness.ObserveOrdinaryIntake(ctx, ordinary, withFailure, client, f.native))
	type ordinaryOutcome struct {
		SourceID string `json:"source_id"`
		Outcome  string `json:"outcome"`
		Attempts int    `json:"attempts"`
	}
	var report struct {
		Expected int
		Accepted int
		Outcomes []ordinaryOutcome
	}
	reportPath := filepath.Join(ordinary.OutputDir, "ordinary-intake.json")
	body, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &report))
	require.Equal(t, 4, report.Expected)
	require.Equal(t, 3, report.Accepted)
	require.Equal(t, []ordinaryOutcome{
		{SourceID: "failed", Outcome: "provider_unavailable", Attempts: 2},
		{SourceID: "one", Outcome: "accepted", Attempts: 1},
		{SourceID: "two", Outcome: "accepted", Attempts: 1},
		{SourceID: "unjudged", Outcome: "accepted", Attempts: 1},
	}, report.Outcomes)
	require.Equal(t, calls+2, f.provider.calls, "accepted rows were replayed")

	unwritable := ordinary
	unwritable.OutputDir = filepath.Join(t.TempDir(), "missing")
	require.ErrorIs(t, evalharness.ObserveOrdinaryIntake(ctx, unwritable, corpus, client, f.native), os.ErrNotExist)
	ordinary.ImportWorkers = 0
	require.ErrorContains(t, evalharness.ObserveOrdinaryIntake(ctx, ordinary, corpus, client, f.native), "import workers")
	for _, change := range []func(*evalharness.Native){func(n *evalharness.Native) { n.Scope.OwnerID = uuid.NewString() }, func(n *evalharness.Native) { n.Scope.Generation++ }} {
		wrong := *f.native
		change(&wrong)
		_, err := wrong.VerifyMappings(ctx, corpus, mapping)
		require.Error(t, err)
	}
	corrupted := mapping
	corrupted.Partitions = map[string][]evalharness.ImportedPartition{}
	for source, parts := range mapping.Partitions {
		corrupted.Partitions[source] = append([]evalharness.ImportedPartition(nil), parts...)
	}
	corrupted.Partitions["one"][0].ContentHash = "invalid"
	_, err = f.native.VerifyMappings(ctx, corpus, corrupted)
	require.ErrorContains(t, err, "partition receipt")
	before := f.snapshot(t)
	_, err = f.maintenance.Command(ctx, domain.OntologyMaintenanceCommand{Action: "resume", OperationKey: uuid.NewString()}, "", "")
	require.NoError(t, err)
	ready := false
	for range 100 {
		_, err = f.maintenance.RunTurn(ctx)
		require.NoError(t, err)
		if evalharness.NativeReady(f.snapshot(t), true, false, f.manifest, f.native.Scope.TeamID) == nil {
			ready = true
			break
		}
	}
	require.True(t, ready, "native coverage must converge")
	rls := postgres.NewRLS()
	require.NoError(t, rls.WithSystemTx(ctx, f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE search_documents SET search_state='pending',embedding=NULL WHERE team_id=?`, f.native.Scope.TeamID).Error
	}))
	require.ErrorContains(t, evalharness.NativeReady(f.snapshot(t), true, false, f.manifest, f.native.Scope.TeamID), "search")
	repaired, err := f.reconciliation.Run(ctx)
	require.NoError(t, err)
	require.Positive(t, repaired.UpdatedCount)
	require.NoError(t, f.native.Pause(ctx))
	frozen := f.snapshot(t)
	require.NoError(t, evalharness.NativeReady(frozen, true, true, f.manifest, f.native.Scope.TeamID))
	require.Equal(t, before.CanonicalHash, frozen.CanonicalHash)
	var recalled map[string]any
	require.NoError(t, client.CallTool(ctx, "recall_memory", map[string]any{"query": "What stores durable memory for Project Alpha?", "limit": 10}, &recalled))
	require.NotEmpty(t, recalled["results"])
	for _, name := range []string{"eval_list_knowledge_refs", "eval_run_recall_case"} {
		require.Error(t, client.CallTool(ctx, name, map[string]any{}, &recalled))
	}
	require.Equal(t, before.CanonicalHash, f.snapshot(t).CanonicalHash)
}
