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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type runnerAnswerTransport func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error)

func (f runnerAnswerTransport) Complete(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return f(ctx, request)
}

func TestRunnerCampaignConditionsAndCapture(t *testing.T) {
	f := newRunnerServiceFixture(t)
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
	worker := make(chan error, 1)
	go func() {
		for ctx.Err() == nil {
			if _, err := f.maintenance.RunTurn(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, ontology.ErrMaintenancePaused) {
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
	manifest.SeedDir, manifest.BaseRelease = t.TempDir(), "v2.8.1-rc.5"
	seed, err := evalharness.Prepare(ctx, manifest, corpus, f.native.Client, f.native)
	require.NoError(t, err)
	runnerSealSyntheticSeed(t, manifest.SeedDir, seed)
	manifest.ImportWorkers = 0
	providerCalls := f.provider.calls
	report, err := evalharness.Run(ctx, manifest, corpus, questions, f.native.Client, transport, f.native)
	require.Equal(t, providerCalls, f.provider.calls, "scoring never imports the corpus again")
	require.NoError(t, err)
	require.True(t, report.Complete)
	require.Equal(t, 4, report.ExpectedAnswers)
	require.Equal(t, 4, report.CompletedAnswers)
	require.Equal(t, int32(4), calls.Load())
	require.Len(t, report.NativeMeasurements, 3)
	for _, observation := range report.NativeMeasurements {
		require.Equal(t, report.Identity.CanonicalHash, observation.CanonicalHash)
	}
	require.True(t, report.CaptureComplete)
	require.Equal(t, initialPackets[0], initialPackets[1])
	require.Equal(t, initialPackets[2], initialPackets[3])
	conditions := []string{}
	for _, axis := range report.Matrix {
		conditions = append(conditions, axis.Condition)
	}
	require.Equal(t, []string{"off", "off", "on", "on"}, conditions)
	kinds := map[string]int{}
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var event evalharness.Event
		require.NoError(t, json.Unmarshal(line, &event))
		kinds[event.Kind]++
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
