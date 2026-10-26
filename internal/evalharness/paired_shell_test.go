package evalharness

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPairShellDeadlineResumeAndCapturePreservation(t *testing.T) {
	t.Setenv("PAIR_API_KEY", "fixture")
	binary := filepath.Join(t.TempDir(), "eval-runner")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/eval-runner")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)
	script, err := filepath.Abs("../../tests/eval/scripts/paired_eval.sh")
	require.NoError(t, err)
	run := func(configPath, body string) ([]byte, error) {
		command := exec.Command("bash", "-c", body)
		command.Env = append(os.Environ(), "PAIR_RUNNER="+binary, "PAIR_CONFIG="+configPath, "PAIR_SCRIPT="+script)
		return command.CombinedOutput()
	}
	c := pairedFixture(t)
	c.Deadline = time.Now().Add(3 * time.Second)
	configPath := filepath.Join(t.TempDir(), "pair.json")
	require.NoError(t, writePairJSON(configPath, c))
	output, err = run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate; pair_stage_dir=""; pair_interrupted=false
trap 'pair_on_error "$?"' ERR
pair_begin drain
sleep 3
pair_wait`)
	var exited *exec.ExitError
	require.ErrorAs(t, err, &exited, "%s", output)
	require.Equal(t, 124, exited.ExitCode(), "%s", output)
	first := filepath.Join(c.OutDir, "candidate", "drain", "attempt-0001")
	require.FileExists(t, filepath.Join(first, "intent.json"))
	require.NoFileExists(t, filepath.Join(first, "failure.json"))
	c.Deadline = time.Now().Add(time.Minute)
	require.NoError(t, writePairJSON(configPath, c))
	output, err = run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate
pair_begin drain`)
	require.NoError(t, err, "%s", output)
	require.FileExists(t, filepath.Join(first, "interrupted.json"))
	require.FileExists(t, filepath.Join(c.OutDir, "candidate", "drain", "attempt-0002", "intent.json"))

	imported, err := BeginPairStage(c, "candidate", "import", PairProcess{PID: 1, BootID: "prior-monitor", StartTick: "1"})
	require.NoError(t, err)
	require.NoError(t, writePairJSON(filepath.Join(imported.Directory, "native", "run_config.json"), RunConfig{Mode: "import", SeedHash: c.SeedHash}))
	require.NoError(t, writePairJSON(filepath.Join(imported.Directory, "native", "knowledge_mapping.json"), newKnowledgeMapping()))
	c.Deadline = time.Now().Add(-time.Second)
	require.NoError(t, writePairJSON(configPath, c))
	t.Setenv("PAIR_IMPORT_STAGE", imported.Directory)
	output, err = run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_stage_dir="$PAIR_IMPORT_STAGE"; pair_interrupted=false
trap 'pair_on_error "$?"' ERR
pair_tool complete-import-mapping --pair-side candidate --pair-directory "$pair_stage_dir"`)
	require.ErrorAs(t, err, &exited, "%s", output)
	require.Equal(t, 124, exited.ExitCode(), "%s", output)
	require.NoFileExists(t, filepath.Join(imported.Directory, "failure.json"))
	c.Deadline = time.Now().Add(time.Minute)
	require.NoError(t, writePairJSON(configPath, c))
	output, err = run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate
pair_begin import`)
	require.NoError(t, err, "%s", output)
	require.FileExists(t, filepath.Join(imported.Directory, "interrupted.json"))

	beforeLaunch, err := BeginPairStage(c, "candidate", "validate", PairProcess{PID: 1, BootID: "prior-launch", StartTick: "1"})
	require.NoError(t, err)
	c.Deadline = time.Now().Add(-time.Second)
	require.NoError(t, writePairJSON(configPath, c))
	t.Setenv("PAIR_VALIDATE_STAGE", beforeLaunch.Directory)
	output, err = run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate; pair_stage=validate; pair_stage_dir="$PAIR_VALIDATE_STAGE"; pair_interrupted=false
trap 'pair_on_error "$?"' ERR
pair_native "$PAIR_RUNNER" --mode validate`)
	require.ErrorAs(t, err, &exited, "%s", output)
	require.Equal(t, 124, exited.ExitCode(), "%s", output)
	require.NoFileExists(t, filepath.Join(beforeLaunch.Directory, "failure.json"))
	require.NoFileExists(t, filepath.Join(beforeLaunch.Directory, "native-process.json"))
	require.NoFileExists(t, filepath.Join(beforeLaunch.Directory, "native.log"))
	c.Deadline = time.Now().Add(time.Minute)
	require.NoError(t, writePairJSON(configPath, c))
	output, err = run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate
pair_begin validate`)
	require.NoError(t, err, "%s", output)
	require.FileExists(t, filepath.Join(beforeLaunch.Directory, "interrupted.json"))
	for _, destination := range []string{"same", "nested", "parent", "symlink"} {
		t.Run(destination, func(t *testing.T) {
			c := pairedFixture(t)
			capture := filepath.Join(t.TempDir(), "original")
			require.NoError(t, os.Mkdir(capture, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(capture, "evidence.json"), []byte("original capture"), 0o600))
			before, err := pairArtifactHashes(capture)
			require.NoError(t, err)
			c.Baseline.CaptureDir, c.Baseline.CaptureSHA256 = capture, c.Baseline.Runner.SHA256
			switch destination {
			case "same":
				c.OutDir = capture
			case "nested":
				c.OutDir = filepath.Join(capture, "comparison")
			case "parent":
				c.OutDir = filepath.Dir(capture)
			case "symlink":
				c.OutDir = filepath.Join(t.TempDir(), "alias")
				require.NoError(t, os.Symlink(capture, c.OutDir))
			}
			path := filepath.Join(t.TempDir(), "pair.json")
			require.NoError(t, writePairJSON(path, c))
			output, err := run(path, `source "$PAIR_SCRIPT"; pair_main`)
			require.Error(t, err, "%s", output)
			require.Contains(t, string(output), "must not overlap")
			after, err := pairArtifactHashes(capture)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
