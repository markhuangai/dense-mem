package evalharness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPairReportReusesPreservedBaselineAndAccountsAcrossWindows(t *testing.T) {
	c := pairedFixture(t)
	writePairCompletedStage(t, c, "baseline", "import")
	original := filepath.Join(writePairCompletedStage(t, c, "baseline", "capture"), "native")
	before, err := pairArtifactHashes(original)
	require.NoError(t, err)
	c.Baseline.CaptureDir = original
	c.Baseline.CaptureSHA256, err = PairFileHash(filepath.Join(original, "pair_capture.json"))
	require.NoError(t, err)
	c.OutDir = filepath.Join(t.TempDir(), "resumed-pair")
	writePairCompletedStage(t, c, "candidate", "import")
	process, err := PairProcessIdentity(os.Getpid())
	require.NoError(t, err)
	stage, err := BeginPairStage(c, "candidate", "capture", process)
	require.NoError(t, err)
	o := pairFixtureObservation(t, c, c.Candidate)
	now := time.Now().UTC()
	for index, window := range []string{"older-window", o.WindowID} {
		a := PairProviderAttempt{TeamID: o.Scope.TeamID, SpaceID: o.Scope.SpaceID, Generation: o.Scope.Generation,
			WindowID: window, OperationKey: window, AssessmentID: window, Number: 1,
			AdmittedAt: now.Add(-2 * time.Second), ReconciledAt: now.Add(-time.Second), Terminal: true, Outcome: "organized"}
		if index == 1 {
			a.UsageAvailable, a.InputTokens, a.OutputTokens, a.TotalTokens = true, 3, 1, 4
		}
		o.ProviderAttempts = append(o.ProviderAttempts, a)
	}
	o.Usage = PairUsage{Attempts: 1, Input: 3, Output: 1, Total: 4}
	o.HistoricalUsage = PairUsage{Attempts: 2, Unavailable: 1, Input: 3, Output: 1, Total: 4}
	writePairNativeCapture(t, c, filepath.Join(stage.Directory, "native"))
	require.NoError(t, writePairJSON(filepath.Join(stage.Directory, "before.json"), o))
	require.NoError(t, writePairJSON(filepath.Join(stage.Directory, "observation.json"), o))
	require.NoError(t, writePairJSON(filepath.Join(stage.Directory, "native-exit.json"), map[string]any{"exit_code": 0, "finished_at": time.Now().UTC()}))
	require.NoError(t, FinishPairStage(c, stage.Directory))
	for _, side := range []string{"baseline", "candidate"} {
		writePairCompletedStage(t, c, side, "score")
	}
	output, err := runFixturePairCommand(t, c, PairCommand{Action: "report"})
	require.NoError(t, err)
	var report PairReport
	require.NoError(t, json.Unmarshal(output, &report))
	require.True(t, report.Sides["baseline"].ReusedCapture)
	require.Equal(t, 1, report.Sides["candidate"].Attempts.UnknownUsage)
	require.Equal(t, 1, report.Sides["candidate"].Attempts.KnownUsage)
	require.Len(t, report.Sides["candidate"].Windows, 2)
	require.Equal(t, 2, report.Sides["candidate"].Attempts.MaximumOverlap)
	after, err := pairArtifactHashes(original)
	require.NoError(t, err)
	require.Equal(t, before, after)
	closed, err := os.CreateTemp(t.TempDir(), "output")
	require.NoError(t, err)
	require.NoError(t, closed.Close())
	err = RunPairCommand(PairCommand{ConfigPath: filepath.Join(filepath.Dir(c.OutDir), "pair-command.json"), Action: "report"}, closed)
	require.ErrorIs(t, err, os.ErrClosed)
}

func TestPairReportCannotPublishWithMissingCaptureOrScore(t *testing.T) {
	for _, missing := range []string{"capture", "score", "proof"} {
		t.Run(missing, func(t *testing.T) {
			c := pairedFixture(t)
			if missing == "capture" {
				require.NoError(t, os.MkdirAll(filepath.Join(c.OutDir, "baseline", "capture"), 0o700))
			} else {
				writePairCompletedStage(t, c, "baseline", "import")
				capture := writePairCompletedStage(t, c, "baseline", "capture")
				if missing == "proof" {
					c.Baseline.CaptureDir = filepath.Join(capture, "native")
					c.Baseline.CaptureSHA256 = pairBytesHash([]byte("different capture"))
					c.OutDir = filepath.Join(t.TempDir(), "comparison")
				}
			}
			output, err := runFixturePairCommand(t, c, PairCommand{Action: "report"})
			require.Error(t, err)
			require.Empty(t, output)
			require.NoFileExists(t, filepath.Join(c.OutDir, "paired_report.json"))
		})
	}
}
