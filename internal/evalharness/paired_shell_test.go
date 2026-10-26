package evalharness

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestPairShellMaintenanceKeysRespectNativeContract(t *testing.T) {
	script, err := filepath.Abs("../../tests/eval/scripts/paired_eval.sh")
	require.NoError(t, err)
	keys := map[string]bool{}
	for _, tc := range []struct{ name, runID, side, stage, attempt, action string }{
		{"short", "pair-fixture", "candidate", "capacity", "attempt-0001", "pause"},
		{"boundary", strings.Repeat("r", 91), "baseline", "capacity", "attempt-0001", "pause"},
		{"overflow", strings.Repeat("r", 91), "candidate", "capacity", "attempt-0001", "pause"},
		{"maximum", strings.Repeat("r", 96), "candidate", "capacity", "attempt-0001", "pause"},
		{"side", strings.Repeat("r", 96), "baseline", "capacity", "attempt-0001", "pause"},
		{"stage", strings.Repeat("r", 96), "candidate", "drain", "attempt-0001", "pause"},
		{"attempt", strings.Repeat("r", 96), "candidate", "capacity", "attempt-0002", "pause"},
		{"action", strings.Repeat("r", 96), "candidate", "capacity", "attempt-0001", "resume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := pairedFixture(t)
			c.RunID = tc.runID
			config := filepath.Join(t.TempDir(), "pair.json")
			require.NoError(t, writePairJSON(config, c))
			_, err := LoadPairConfig(config)
			require.NoError(t, err)
			dir := filepath.Join(t.TempDir(), tc.attempt)
			require.NoError(t, os.Mkdir(dir, 0o700))
			command := exec.Command("bash", "-c", `set -Eeuo pipefail
source "$1"
PAIR_CONFIG="$2"; pair_side="$3"; pair_stage="$4"; pair_stage_dir="$5"
pair_control() { printf '%s\n' "$3"; }
pair_command "$6"
cat "$pair_stage_dir/$6.json"`, "paired-key-test", script, config, tc.side, tc.stage, dir, tc.action)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "%s", output)
			var body struct {
				OperationKey string `json:"operation_key"`
			}
			require.NoError(t, json.Unmarshal(output, &body))
			_, err = ontology.PrepareMaintenanceCommand(domain.OntologyMaintenanceCommand{OperationKey: body.OperationKey, Action: tc.action})
			require.NoError(t, err, "key has %d bytes", len(body.OperationKey))
			original := strings.Join([]string{tc.runID, tc.side, tc.stage, tc.attempt, tc.action}, ":")
			if len(original) <= 128 {
				require.Equal(t, original, body.OperationKey, "preserve existing replay identities")
			}
			require.False(t, keys[body.OperationKey], "distinct commands must not share a key")
			keys[body.OperationKey] = true
			replay := exec.Command(command.Path, command.Args[1:]...)
			replayed, err := replay.CombinedOutput()
			require.NoError(t, err, "%s", replayed)
			require.Equal(t, output, replayed)
		})
	}
}

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
	t.Run("successful_native_completion", func(t *testing.T) {
		c := pairedFixture(t)
		configPath := filepath.Join(t.TempDir(), "pair.json")
		require.NoError(t, writePairJSON(configPath, c))
		process, err := PairProcessIdentity(os.Getpid())
		require.NoError(t, err)
		stage, err := BeginPairStage(c, "candidate", "validate", process)
		require.NoError(t, err)
		var intent PairStageIntent
		require.NoError(t, readPairJSON(filepath.Join(stage.Directory, "intent.json"), &intent))
		t.Setenv("PAIR_NATIVE_SUCCESS_STAGE", stage.Directory)
		output, err := run(configPath, `set -Eeuo pipefail
source "$PAIR_SCRIPT"
pair_side=candidate; pair_stage=validate; pair_stage_dir="$PAIR_NATIVE_SUCCESS_STAGE"; pair_interrupted=false
pair_native "$PAIR_RUNNER" --mode validate --seed "$(jq -er '.seed_manifest' "$PAIR_CONFIG")" --suite "$(jq -er '.suite' "$PAIR_CONFIG")" --out "$pair_stage_dir/native"`)
		require.NoError(t, err, "%s", output)
		var completion struct {
			Code       int       `json:"exit_code"`
			FinishedAt time.Time `json:"finished_at"`
		}
		require.NoError(t, readPairJSON(filepath.Join(stage.Directory, "native-exit.json"), &completion))
		require.Zero(t, completion.Code)
		require.False(t, completion.FinishedAt.Before(intent.StartedAt), "completion %s precedes start %s", completion.FinishedAt, intent.StartedAt)
		require.NotZero(t, completion.FinishedAt.Nanosecond(), "native completion lost subsecond precision")
		require.False(t, completion.FinishedAt.After(time.Now()))
		require.FileExists(t, filepath.Join(stage.Directory, "native-process.json"))
		require.NoError(t, FinishPairStage(c, stage.Directory))
		require.NoError(t, VerifyPairStage(c, stage.Directory))
	})
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
