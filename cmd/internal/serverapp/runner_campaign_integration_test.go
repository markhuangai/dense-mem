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
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type runnerAnswerTransport func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error)

func (f runnerAnswerTransport) Complete(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return f(ctx, request)
}

func TestRunnerCampaignConditionsAndCapture(t *testing.T) {
	for _, scenario := range []string{"verified", "active ontology at restore", "ontology changed during scoring", "terminal capture failed"} {
		t.Run(scenario, func(t *testing.T) { testRunnerOffCampaign(t, scenario) })
	}
}

func testRunnerOffCampaign(t *testing.T, scenario string) {
	f := newRunnerServiceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	setOntology := func(value string) error {
		return f.native.Control(ctx, http.MethodPatch, "/config/topic-maintenance", map[string]any{"items": []map[string]string{{"key": domain.AppConfigTopicMaintenanceEnabled, "value": value}}}, nil)
	}
	require.NoError(t, setOntology("false"))
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
	var initialPackets [2]evalharness.MemoryCall
	transport := runnerAnswerTransport(func(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		position := calls.Add(1) - 1
		if position >= int32(len(initialPackets)) {
			return modelprovider.StructuredResult{}, errors.New("unexpected extra answer call")
		}
		if position == 0 && scenario == "ontology changed during scoring" {
			if err := setOntology("true"); err != nil {
				return modelprovider.StructuredResult{}, err
			}
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
	manifest.SeedDir, manifest.BaseRelease = t.TempDir(), "v2.8.1-rc.5"
	seed, err := evalharness.Prepare(ctx, manifest, corpus, f.native.Client, f.native)
	require.NoError(t, err)
	runnerSealSyntheticSeed(t, manifest.SeedDir, seed)
	manifest.ImportWorkers, manifest.Condition = 0, "off"
	if scenario == "active ontology at restore" {
		require.NoError(t, setOntology("true"))
	}
	const requestCredential = "runner-request-credential-canary"
	if scenario == "terminal capture failed" {
		ctx = observability.WithAuthenticationSecrets(ctx, requestCredential)
		f.native.RuntimeLogs = func(context.Context) error {
			return errors.New("runtime collector unavailable: " + f.native.ControlToken + " " + requestCredential)
		}
	}
	providerCalls := f.provider.calls
	report, err := evalharness.Run(ctx, manifest, corpus, questions, f.native.Client, transport, f.native)
	require.Equal(t, providerCalls, f.provider.calls, "scoring never imports the corpus again")
	require.Len(t, f.snapshot(t).ProviderAttempts, 0)
	require.Equal(t, 2, report.ExpectedAnswers)
	require.FileExists(t, filepath.Join(manifest.OutputDir, "scores.json"))
	require.False(t, strings.Contains(logs.String(), f.native.ControlToken), "console contains the configured control credential")
	require.False(t, strings.Contains(logs.String(), requestCredential), "console contains the presented request credential")
	switch scenario {
	case "active ontology at restore":
		require.ErrorContains(t, err, "unexpected native Topic maintenance state")
		require.Zero(t, calls.Load())
		require.Zero(t, report.CompletedAnswers)
		require.False(t, report.Complete)
		require.Contains(t, logs.String(), `"kind":"seed_verification_failed"`)
		return
	case "ontology changed during scoring":
		require.ErrorContains(t, err, "unexpected native Topic maintenance state")
		require.False(t, report.ModelVerified)
		require.False(t, report.Complete)
	case "terminal capture failed":
		require.ErrorContains(t, err, "protected runtime capture")
		require.True(t, report.ModelVerified)
		require.False(t, report.CaptureComplete)
		require.False(t, report.Complete)
		require.Equal(t, "runtime_log_collection_failed", report.CaptureFailure.Reason)
	default:
		require.NoError(t, err)
		require.True(t, report.Complete)
		require.True(t, report.ModelVerified)
		require.True(t, report.CaptureComplete)
		require.Len(t, report.NativeMeasurements, 2)
		for _, observation := range report.NativeMeasurements {
			require.Equal(t, seed.CanonicalHash, observation.CanonicalHash)
		}
		require.Contains(t, report.Timings, "scoring_through_flush")
	}
	require.Equal(t, 2, report.CompletedAnswers)
	require.Equal(t, int32(2), calls.Load())
	require.Equal(t, initialPackets[0], initialPackets[1])
	for _, score := range report.Scores {
		require.Equal(t, 1.0, score.EM)
		require.Equal(t, 1, score.ValidCitations)
	}
	require.Equal(t, "off", report.Matrix[0].Condition)
	require.Equal(t, "off", report.Matrix[1].Condition)
	kinds := map[string]int{}
	var outcome struct {
		CompletedAnswers int                         `json:"completed_answers"`
		Error            string                      `json:"error"`
		CaptureFailure   *evalharness.CaptureFailure `json:"capture_failure"`
	}
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var event evalharness.Event
		require.NoError(t, json.Unmarshal(line, &event))
		kinds[event.Kind]++
		if bytes.Contains(line, []byte(`"msg":"eval_dataset_outcome"`)) {
			require.NoError(t, json.Unmarshal(line, &outcome))
		}
	}
	if scenario == "terminal capture failed" {
		require.Equal(t, report.CompletedAnswers, outcome.CompletedAnswers)
		require.Equal(t, report.CaptureFailure, outcome.CaptureFailure)
		require.True(t, strings.Contains(outcome.Error, "runtime collector unavailable"), "summary omits the collector failure detail")
		require.Equal(t, 2, strings.Count(outcome.Error, observability.CredentialProtectionRedacted))
	}
	require.Equal(t, 1, kinds["initial_recall"])
	require.Equal(t, 1, kinds["seed_verified"])
	require.Equal(t, 1, kinds["finished"])
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
	_, err = f.maintenance.Command(ctx, domain.TopicMaintenanceCommand{Action: "resume", OperationKey: uuid.NewString()}, "", "")
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

	// An unchanged assignment can finish a batch without writing an assignment
	// outcome, even when the Evidence equivalence assessment used the provider.
	batchID, assessmentID := uuid.NewString(), uuid.NewString()
	key := "equivalence:" + batchID + ":" + mapping.Partitions["one"][0].EvidenceID
	require.NoError(t, rls.WithSystemTx(ctx, f.admin, func(tx *gorm.DB) error {
		var runID, windowID string
		if err := tx.Raw(`SELECT run_id::text,window_id::text FROM topic_maintenance_runs
			WHERE kind='scheduled' ORDER BY created_at DESC LIMIT 1`).Row().Scan(&runID, &windowID); err != nil {
			return err
		}
		scope := f.native.Scope
		if err := tx.Exec(`INSERT INTO topic_maintenance_batches(batch_id,run_id,window_id,team_id,shared_space_id,
			space_generation,lease_token,lease_until,sources,revisions,status,created_at,completed_at)
			VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?::uuid,?,gen_random_uuid(),now(),'[]','{}','completed',now(),now())`,
			batchID, runID, windowID, scope.TeamID, scope.SpaceID, scope.Generation).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO topic_maintenance_attempts(batch_id,assessment_id,attempt,window_id,
			reserved_input,reserved_output,estimated_input_tokens,max_output_tokens,admitted_at)
			VALUES(?::uuid,?::uuid,1,?::uuid,0,0,100,100,now())`, batchID, assessmentID, windowID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE topic_maintenance_attempts
			SET reported_input=50,reported_output=20,reported_total=70,reconciled_at=now()
			WHERE batch_id=?::uuid AND assessment_id=?::uuid AND attempt=1`, batchID, assessmentID).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO evidence_equivalence_outcomes(team_id,shared_space_id,space_generation,operation_key,assessment_id,
			input_hash,batch_hash,body) VALUES(?::uuid,?::uuid,?,?,?::uuid,'sha256:'||repeat('0',64),'sha256:'||repeat('1',64),'{}')`,
			scope.TeamID, scope.SpaceID, scope.Generation, key, assessmentID).Error
	}))
	withEquivalence := f.snapshot(t)
	foundEquivalence := false
	for _, attempt := range withEquivalence.ProviderAttempts {
		if attempt.AssessmentID == assessmentID {
			foundEquivalence = true
			require.Equal(t, key, attempt.OperationKey)
			require.True(t, attempt.Terminal)
			require.EqualValues(t, 70, attempt.Total)
		}
	}
	require.True(t, foundEquivalence)
	require.NoError(t, evalharness.NativeReady(withEquivalence, true, true, f.manifest, f.native.Scope.TeamID))
}
