package evalharness

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPairMonotonicIntervalsRejectUnverifiableTiming(t *testing.T) {
	start := &PairClock{BootID: "boot", Nanoseconds: 1_000_000_000}
	end := &PairClock{BootID: "boot", Nanoseconds: 3_500_000_000}
	seconds, err := pairClockElapsed(start, end)
	require.NoError(t, err)
	require.Equal(t, 2.5, *seconds)
	seconds, err = pairClockElapsed(nil, nil)
	require.NoError(t, err)
	require.Nil(t, seconds)
	for _, pair := range [][2]*PairClock{{nil, end}, {start, nil}, {end, start}, {start, {BootID: "other", Nanoseconds: 4_000_000_000}}, {start, {Nanoseconds: 4_000_000_000}}} {
		_, err := pairClockElapsed(pair[0], pair[1])
		require.Error(t, err)
	}
	first, err := pairClockNow()
	require.NoError(t, err)
	second, err := pairClockNow()
	require.NoError(t, err)
	_, err = pairClockElapsed(first, second)
	require.NoError(t, err)
}

func TestPairReceiptUsesMonotonicTimingAcrossUTCStep(t *testing.T) {
	c := pairedFixture(t)
	dir := writePairCompletedStage(t, c, "candidate", "validate")
	var receipt PairStageReceipt
	require.NoError(t, readPairJSON(filepath.Join(dir, "receipt.json"), &receipt))
	receipt.FinishedAt = receipt.Intent.StartedAt.Add(-time.Hour)
	require.NoError(t, writePairJSON(filepath.Join(dir, "receipt.json"), receipt))
	require.NoError(t, VerifyPairStage(c, dir))
	receipt.Clock.BootID = "another-boot"
	require.NoError(t, writePairJSON(filepath.Join(dir, "receipt.json"), receipt))
	require.ErrorContains(t, VerifyPairStage(c, dir), "monotonic")
}

func TestPairReportHistoricalTimingStaysUnavailable(t *testing.T) {
	c := pairedFixture(t)
	for _, side := range []string{"baseline", "candidate"} {
		for _, stage := range []string{"import", "capture", "score"} {
			dir := writePairCompletedStage(t, c, side, stage)
			var receipt PairStageReceipt
			require.NoError(t, readPairJSON(filepath.Join(dir, "receipt.json"), &receipt))
			receipt.Intent.Clock, receipt.Clock = nil, nil
			require.NoError(t, writePairJSON(filepath.Join(dir, "intent.json"), receipt.Intent))
			var err error
			receipt.Files, err = pairArtifactHashes(dir)
			require.NoError(t, err)
			require.NoError(t, writePairJSON(filepath.Join(dir, "receipt.json"), receipt))
		}
	}
	report, err := WritePairReport(c)
	require.NoError(t, err)
	require.Nil(t, report.ElapsedSeconds)
	for _, stage := range report.Stages {
		require.Nil(t, stage.ElapsedSeconds)
	}
}

func TestPairReportRetainedStageTimingsSurviveReboot(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		name := "verifiable"
		if corrupt {
			name = "corrupt_stage_interval"
		}
		t.Run(name, func(t *testing.T) {
			c := pairedFixture(t)
			var last string
			for _, side := range []string{"baseline", "candidate"} {
				for _, stage := range []string{"import", "capture", "score"} {
					last = writePairCompletedStage(t, c, side, stage)
					var receipt PairStageReceipt
					require.NoError(t, readPairJSON(filepath.Join(last, "receipt.json"), &receipt))
					receipt.Intent.Clock = &PairClock{BootID: "retained-boot", Nanoseconds: 1_000_000_000}
					receipt.Clock = &PairClock{BootID: "retained-boot", Nanoseconds: 3_500_000_000}
					require.NoError(t, writePairJSON(filepath.Join(last, "intent.json"), receipt.Intent))
					var err error
					receipt.Files, err = pairArtifactHashes(last)
					require.NoError(t, err)
					require.NoError(t, writePairJSON(filepath.Join(last, "receipt.json"), receipt))
				}
			}
			if corrupt {
				var receipt PairStageReceipt
				require.NoError(t, readPairJSON(filepath.Join(last, "receipt.json"), &receipt))
				receipt.Clock.BootID = "unrelated-boot"
				require.NoError(t, writePairJSON(filepath.Join(last, "receipt.json"), receipt))
			}
			report, err := WritePairReport(c)
			if corrupt {
				require.ErrorContains(t, err, "monotonic")
				require.NoFileExists(t, filepath.Join(c.OutDir, "paired_report.json"))
				return
			}
			require.NoError(t, err)
			require.Nil(t, report.ElapsedSeconds)
			require.Len(t, report.Stages, 6)
			for _, stage := range report.Stages {
				require.NotNil(t, stage.ElapsedSeconds)
				require.Equal(t, 2.5, *stage.ElapsedSeconds)
			}
		})
	}
}
