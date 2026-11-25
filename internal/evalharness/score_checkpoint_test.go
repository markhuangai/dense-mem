package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/stretchr/testify/require"
)

func TestScoreCheckpointRecoversAfterWorkerKill(t *testing.T) {
	manifest := Manifest{RunID: "42-hotpotqa-off", WorkflowRunID: "42", Dataset: "hotpotqa", Condition: "off", Model: "fixture", ContextWindowTokens: 300_000}
	question, observation, bySource, mapping, canonical := scoringFixture()
	if root := os.Getenv("DENSE_MEM_SCORE_CHECKPOINT_TEST_ROOT"); root != "" {
		manifest.OutputDir = filepath.Join(root, "public")
		report := NewReport(manifest, len(bySource), 1)
		report.Identity.CanonicalHash = strings.Repeat("a", 64)
		report.ImportedRows = len(bySource)
		checkpoint, err := newScoreCheckpoint(manifest.OutputDir, report)
		require.NoError(t, err)
		corpus := make([]CorpusItem, 0, len(bySource))
		for _, item := range bySource {
			corpus = append(corpus, item)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": observation.Initial.Result}}))
		}))
		defer server.Close()
		capture := newRunnerCaptureFixture(io.Discard, manifest.RunID)
		client := &HTTPClient{BaseURL: server.URL, Client: server.Client(), APIKey: "synthetic", Capture: capture}
		var calls atomic.Int32
		provider := scriptedAnswer(func(ctx context.Context, _ modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
			if calls.Add(1) == 2 {
				require.NoError(t, os.WriteFile(filepath.Join(root, "multi-turn-started"), nil, 0o600))
				<-ctx.Done()
				return modelprovider.StructuredResult{}, ctx.Err()
			}
			answer := hostAction{Action: "answer", Answer: question.Answers[0], Citations: []Citation{}}
			data, err := json.Marshal(answer)
			return modelprovider.StructuredResult{Content: string(data)}, err
		})
		_, err = runCondition(t.Context(), manifest, corpus, []Question{question}, mapping, canonical, client, provider, checkpoint)
		t.Fatalf("worker exited before the expected kill: %v", err)
	}

	root := t.TempDir()
	manifest.OutputDir = filepath.Join(root, "public")
	require.NoError(t, os.Mkdir(manifest.OutputDir, 0o700))
	var diagnostics bytes.Buffer
	child := exec.Command(os.Args[0], "-test.run=^TestScoreCheckpointRecoversAfterWorkerKill$")
	child.Env = append(os.Environ(), "DENSE_MEM_SCORE_CHECKPOINT_TEST_ROOT="+root)
	child.Stderr, child.Stdout = &diagnostics, &diagnostics
	require.NoError(t, child.Start())
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(root, "multi-turn-started"))
		return err == nil
	}, 15*time.Second, 10*time.Millisecond)
	require.NoError(t, child.Process.Kill())
	waitErr := child.Wait()
	waited = true
	require.ErrorContains(t, waitErr, "killed", diagnostics.String())

	checkpoint := checkpointDirectory(manifest.OutputDir)
	require.NoFileExists(t, filepath.Join(checkpoint, scoreFilename(0, "multi_turn")))
	require.NoFileExists(t, filepath.Join(manifest.OutputDir, "scores.json"))
	record := filepath.Join(checkpoint, scoreFilename(0, "single"))
	original, err := os.ReadFile(record)
	require.NoError(t, err)
	var invalid checkpointScore
	require.NoError(t, json.Unmarshal(original, &invalid))
	invalid.Seed = strings.Repeat("b", 64)
	require.NoError(t, writeScoreFile(record, invalid))
	_, _, err = RecoverScores(manifest, len(bySource), []Question{question})
	require.ErrorContains(t, err, "different run")
	require.NoError(t, json.Unmarshal(original, &invalid))
	invalid.Score.F1 = 0.25
	require.NoError(t, writeScoreFile(record, invalid))
	_, _, err = RecoverScores(manifest, len(bySource), []Question{question})
	require.ErrorContains(t, err, "corrupt")
	require.NoError(t, os.WriteFile(record, []byte("{"), 0o600))
	_, _, err = RecoverScores(manifest, len(bySource), []Question{question})
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(manifest.OutputDir, "scores.json"))
	require.NoError(t, os.WriteFile(record, original, 0o600))

	var verified Report
	header := filepath.Join(checkpoint, scoreCheckpointHeader)
	require.NoError(t, readJSON(header, &verified))
	digest, err := FileHash(header)
	require.NoError(t, err)
	proof := &scoreCheckpoint{directory: checkpoint, hash: digest}
	require.NoError(t, proof.saveFailure(CaptureFailure{Phase: "log capture", Kind: "runtime_log", Reason: "enqueue_interrupted"}))
	require.NoError(t, os.WriteFile(filepath.Join(checkpoint, ".scores-unpublished"), []byte("{"), 0o600))
	report, recovered, err := RecoverScores(manifest, len(bySource), []Question{question})
	require.NoError(t, err)
	require.True(t, recovered)
	require.False(t, report.Complete)
	require.False(t, report.CaptureComplete)
	require.Equal(t, "enqueue_interrupted", report.CaptureFailure.Reason)
	require.Equal(t, 1, report.CompletedAnswers)
	require.Equal(t, 2, report.ExpectedAnswers)
	require.Equal(t, 1, report.Matrix[1].Missing)
	require.Equal(t, 1.0, report.Matrix[0].Mean.EM)
	require.NoError(t, os.WriteFile(record, []byte("{"), 0o600))
	_, recovered, err = RecoverScores(manifest, len(bySource), []Question{question})
	require.NoError(t, err)
	require.False(t, recovered, "published report takes precedence over scratch records")
}
