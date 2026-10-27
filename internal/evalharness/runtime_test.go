package evalharness

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEvalRuntimePathsAndLocks(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "data")
	require.NoError(t, os.Mkdir(child, 0o700))
	identity, err := runtimePathIdentity(child)
	require.NoError(t, err)
	require.NoError(t, verifyRuntimePath(identity))
	require.NoError(t, os.Rename(child, child+"-original"))
	require.NoError(t, os.Mkdir(child, 0o700))
	require.ErrorContains(t, verifyRuntimePath(identity), "identity changed")
	require.NoError(t, os.Symlink(child, filepath.Join(root, "alias")))
	_, err = runtimeCleanPath(filepath.Join(root, "alias", "missing"))
	require.ErrorContains(t, err, "symlink")
	_, err = runtimeCleanPath("/")
	require.Error(t, err)
	file := filepath.Join(root, "regular-file")
	require.NoError(t, os.WriteFile(file, []byte("keep"), 0o600))
	_, err = runtimePathIdentity(file)
	require.ErrorContains(t, err, "not a directory")
	_, err = runtimePathIdentity(filepath.Join(root, "missing"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = runtimePathIdentity(filepath.Join(root, "alias"))
	require.ErrorContains(t, err, "symlink")
	require.ErrorIs(t, verifyRuntimePath(runtimeFileIdentity{Path: filepath.Join(root, "missing")}), os.ErrNotExist)
	_, err = runtimeLock(filepath.Join(root, "missing", "runtime.lock"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = runtimeLock(filepath.Join(root, "alias", "runtime.lock"))
	require.ErrorContains(t, err, "symlink")
	_, err = runtimeRetentionPlanPath(filepath.Join(root, "alias", "retention.json"), runtimeReceipt{})
	require.ErrorContains(t, err, "symlink")
	lock, err := runtimeLock(filepath.Join(root, "runtime.lock"))
	require.NoError(t, err)
	defer lock.Close()
	_, err = runtimeLock(filepath.Join(root, "runtime.lock"))
	require.ErrorContains(t, err, "owned by another process")
}

func TestEvalRuntimeRequiresExplicitIntent(t *testing.T) {
	for _, action := range []string{"start", "record", "retire", "verify", "unknown"} {
		t.Run(action, func(t *testing.T) {
			err := RunRuntimeCommand(context.Background(), RuntimeCommand{Action: action}, io.Discard)
			require.Error(t, err)
		})
	}
	err := RunRuntimeCommand(context.Background(), RuntimeCommand{Action: "start", Directory: filepath.Join(t.TempDir(), "run"), Project: "fixture"}, io.Discard)
	require.ErrorContains(t, err, "--new-runtime or --supersedes")
}

func TestEvalRuntimePairGuardPreservesLegacyIdentity(t *testing.T) {
	c := pairedFixture(t)
	before, err := pairConfigHash(c)
	require.NoError(t, err)
	require.NoError(t, CheckPairRuntimeLifecycle(c, "candidate"), "unregistered legacy runtimes remain resumable")
	dir := c.Candidate.Runtime.StateDir
	require.NoError(t, os.MkdirAll(dir, 0o700))
	receipt := runtimeReceipt{SchemaVersion: runtimeSchema, ID: "retiring-fixture", Directory: dir, Project: c.Candidate.Runtime.Project, State: "retiring"}
	require.NoError(t, saveRuntimeReceipt(receipt))
	require.ErrorContains(t, CheckPairRuntimeLifecycle(c, "candidate"), "not active")
	after, err := pairConfigHash(c)
	require.NoError(t, err)
	require.Equal(t, before, after, "lifecycle state never changes the paired configuration hash")
}

func TestEvalRuntimePairGuardRejectsUnsafeState(t *testing.T) {
	for _, variant := range []string{"unknown_side", "symlink_directory", "invalid_receipt", "locked_receipt"} {
		t.Run(variant, func(t *testing.T) {
			pair := pairedFixture(t)
			dir := pair.Candidate.Runtime.StateDir
			if variant == "unknown_side" {
				require.Error(t, CheckPairRuntimeLifecycle(pair, "unknown"))
				return
			}
			if variant == "symlink_directory" {
				require.NoError(t, os.Symlink(t.TempDir(), dir))
				require.ErrorContains(t, CheckPairRuntimeLifecycle(pair, "candidate"), "symlink")
				return
			}
			require.NoError(t, os.Mkdir(dir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, runtimeReceiptName), []byte("{"), 0o600))
			reason := "invalid paired JSON"
			if variant == "locked_receipt" {
				lock, err := runtimeLock(filepath.Join(dir, "runtime.lock"))
				require.NoError(t, err)
				defer lock.Close()
				reason = "owned by another process"
			}
			require.ErrorContains(t, CheckPairRuntimeLifecycle(pair, "candidate"), reason)
		})
	}
}

func TestEvalRuntimeArchive(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "runtime", "postgres")
	require.NoError(t, os.MkdirAll(data, 0o700))
	input := filepath.Join(root, "input.json")
	require.NoError(t, os.WriteFile(input, []byte("private audit input\n"), 0o600))
	hash, err := PairFileHash(input)
	require.NoError(t, err)
	plan := runtimeRetentionPlan{SchemaVersion: runtimeSchema, ArchiveDir: filepath.Join(root, "archive"), Outcome: "incomplete"}
	for _, kind := range runtimeEvidenceKinds {
		plan.Evidence = append(plan.Evidence, runtimeEvidence{Kind: kind, Unavailable: "run ended before this stage"})
	}
	plan.Evidence[0] = runtimeEvidence{Kind: "frozen_inputs", Files: []runtimeArtifact{{Path: input, SHA256: hash}}}
	plan.Evidence[5] = runtimeEvidence{Kind: "private_audit", Files: []runtimeArtifact{{Path: input, SHA256: hash}}}
	receipt := runtimeReceipt{SchemaVersion: runtimeSchema, Directory: filepath.Dir(data), ID: "fixture"}
	bind, err := runtimePathIdentity(data)
	require.NoError(t, err)
	receipt.Binds = []runtimeBind{{Identity: bind}}
	require.NoError(t, validateRuntimeRetention(&plan, receipt))
	archive, err := archiveRuntimeEvidence(plan)
	require.NoError(t, err)
	require.NoError(t, verifyRuntimeArchive(archive))
	for _, file := range archive.Files {
		info, statErr := os.Stat(file.ArchivePath)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	_, err = archiveRuntimeEvidence(plan)
	require.NoError(t, err, "a completed copy is reusable after interruption")
	require.NoError(t, os.WriteFile(archive.Files[0].ArchivePath, []byte("changed"), 0o600))
	require.ErrorContains(t, verifyRuntimeArchive(archive), "hash")
	_, err = archiveRuntimeEvidence(plan)
	require.Error(t, err, "never overwrite corrupted retained evidence")
	plan.ArchiveDir = filepath.Join(data, "archive")
	require.ErrorContains(t, validateRuntimeRetention(&plan, receipt), "overlap")
	plan.ArchiveDir = filepath.Join(root, "archive")
	plan.PendingRuntimeDependencies = []string{"credential audit still needs PostgreSQL"}
	require.ErrorContains(t, validateRuntimeRetention(&plan, receipt), "dependencies")
	plan.PendingRuntimeDependencies = nil
	plan.Outcome = "complete"
	require.ErrorContains(t, validateRuntimeRetention(&plan, receipt), "capture")
}

func evalRuntimeRetentionFixture(t *testing.T) (runtimeRetentionPlan, runtimeReceipt) {
	t.Helper()
	root := t.TempDir()
	planPath := evalRuntimeIncompletePlan(t, root)
	var plan runtimeRetentionPlan
	require.NoError(t, readPairJSON(planPath, &plan))
	source := filepath.Join(root, "evidence.json")
	require.NoError(t, os.WriteFile(source, []byte("retained evidence\n"), 0o600))
	hash, err := PairFileHash(source)
	require.NoError(t, err)
	plan.Evidence[0] = runtimeEvidence{Kind: "frozen_inputs", Files: []runtimeArtifact{{Path: source, SHA256: hash}}}
	dir := filepath.Join(root, "runtime")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o700))
	identity, err := runtimePathIdentity(filepath.Join(dir, "data"))
	require.NoError(t, err)
	receipt := runtimeReceipt{SchemaVersion: runtimeSchema, Directory: dir, ID: uuid.NewString(), Project: "eval627-" + uuid.NewString(), State: "preparing", Binds: []runtimeBind{{Identity: identity}}}
	require.NoError(t, validateRuntimeRetention(&plan, receipt))
	return plan, receipt
}

func TestEvalRuntimeRetentionBoundaries(t *testing.T) {
	cases := []struct {
		name, message string
		change        func(*testing.T, *runtimeRetentionPlan, runtimeReceipt)
	}{
		{"schema", "runtime schema", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) { p.SchemaVersion = "unknown" }},
		{"outcome", "explicit complete/incomplete", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) { p.Outcome = "pending" }},
		{"unknown_category", "unknown or duplicated", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) { p.Evidence[1].Kind = "unknown" }},
		{"duplicate_category", "unknown or duplicated", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) { p.Evidence[1].Kind = p.Evidence[0].Kind }},
		{"missing_category", "must account for", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) { p.Evidence = p.Evidence[:5] }},
		{"unexplained_missing_evidence", "files or an explicit", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) { p.Evidence[1].Unavailable = "" }},
		{"contradictory_evidence", "files or an explicit", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) { p.Evidence[0].Unavailable = "missing" }},
		{"hash_required", "exact SHA-256", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) { p.Evidence[0].Files[0].SHA256 = "" }},
		{"source_missing", "no such file", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) {
			p.Evidence[0].Files[0].Path += ".missing"
		}},
		{"source_is_directory", "regular file", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) {
			p.Evidence[0].Files[0].Path = filepath.Join(filepath.Dir(p.ArchiveDir), "source-directory")
			require.NoError(t, os.Mkdir(p.Evidence[0].Files[0].Path, 0o700))
		}},
		{"source_is_mutable_receipt", "mutable lifecycle receipt", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) {
			p.Evidence[0].Files[0].Path = filepath.Join(r.Directory, runtimeReceiptName)
		}},
		{"source_in_archive", "overlaps the archive", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) {
			p.Evidence[0].Files[0].Path = filepath.Join(p.ArchiveDir, "input")
		}},
		{"source_in_disposable_storage", "disposable storage overlap", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) {
			p.Evidence[0].Files[0].Path = filepath.Join(r.Binds[0].Identity.Path, "input")
		}},
		{"protected_runtime_overlaps", "protected runtime", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) {
			p.ProtectedRuntimes = []string{r.Directory}
		}},
		{"protected_archive_overlaps", "protected runtime", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) {
			p.ProtectedRuntimes = []string{p.ArchiveDir}
		}},
		{"archive_symlink", "symlink", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) {
			require.NoError(t, os.Symlink(filepath.Dir(p.ArchiveDir), p.ArchiveDir))
		}},
		{"source_symlink", "symlink", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) {
			file := &p.Evidence[0].Files[0]
			require.NoError(t, os.Symlink(file.Path, file.Path+".link"))
			file.Path += ".link"
		}},
		{"protected_symlink", "symlink", func(t *testing.T, p *runtimeRetentionPlan, r runtimeReceipt) {
			link := filepath.Join(filepath.Dir(p.ArchiveDir), "protected-link")
			require.NoError(t, os.Symlink(r.Directory, link))
			p.ProtectedRuntimes = []string{link}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			plan, receipt := evalRuntimeRetentionFixture(t)
			original := plan.Evidence[0].Files[0]
			test.change(t, &plan, receipt)
			require.ErrorContains(t, validateRuntimeRetention(&plan, receipt), test.message)
			require.NoError(t, runtimeVerifyFile(original.Path, original.SHA256))
			require.NoError(t, verifyRuntimePath(receipt.Binds[0].Identity))
		})
	}
}

func TestEvalRuntimeArchiveIntegrity(t *testing.T) {
	for _, variant := range []string{"public_directory", "directory_is_file", "directory_symlink", "manifest_corrupt", "manifest_changed", "manifest_symlink", "artifact_public", "artifact_escape", "artifact_symlink", "source_changed", "source_symlink", "source_missing", "retention_changed"} {
		t.Run(variant, func(t *testing.T) {
			plan, _ := evalRuntimeRetentionFixture(t)
			if variant == "public_directory" {
				require.NoError(t, os.Mkdir(plan.ArchiveDir, 0o755))
				require.NoError(t, os.Chmod(plan.ArchiveDir, 0o755))
				_, err := archiveRuntimeEvidence(plan)
				require.ErrorContains(t, err, "private")
				require.ErrorContains(t, verifyRuntimeArchive(runtimeArchive{Plan: plan}), "private")
				return
			}
			if variant == "directory_is_file" {
				require.NoError(t, os.WriteFile(plan.ArchiveDir, []byte("keep"), 0o600))
				_, err := archiveRuntimeEvidence(plan)
				require.Error(t, err)
				data, err := os.ReadFile(plan.ArchiveDir)
				require.NoError(t, err)
				require.Equal(t, "keep", string(data))
				return
			}
			if variant == "directory_symlink" {
				require.NoError(t, os.Symlink(filepath.Dir(plan.ArchiveDir), plan.ArchiveDir))
				_, err := archiveRuntimeEvidence(plan)
				require.ErrorContains(t, err, "symlink")
				require.ErrorContains(t, verifyRuntimeArchive(runtimeArchive{Plan: plan}), "symlink")
				return
			}
			archive, err := archiveRuntimeEvidence(plan)
			require.NoError(t, err)
			manifest := filepath.Join(plan.ArchiveDir, "retention.json")
			file := archive.Files[0]
			switch variant {
			case "manifest_corrupt":
				require.NoError(t, os.WriteFile(manifest, []byte("{"), 0o600))
			case "manifest_changed":
				archive.Plan.Evidence[1].Unavailable = "different provenance"
			case "manifest_symlink":
				require.NoError(t, os.Rename(manifest, manifest+".saved"))
				require.NoError(t, os.Symlink(manifest+".saved", manifest))
			case "artifact_public":
				require.NoError(t, os.Chmod(file.ArchivePath, 0o644))
			case "artifact_escape":
				archive.Files[0].ArchivePath = file.SourcePath
				require.NoError(t, writePairJSON(manifest, archive))
			case "artifact_symlink":
				require.NoError(t, os.Rename(file.ArchivePath, file.ArchivePath+".saved"))
				require.NoError(t, os.Symlink(file.ArchivePath+".saved", file.ArchivePath))
			case "source_changed":
				require.NoError(t, os.WriteFile(file.SourcePath, []byte("changed"), 0o600))
			case "source_symlink":
				require.NoError(t, os.Rename(file.SourcePath, file.SourcePath+".saved"))
				require.NoError(t, os.Symlink(file.SourcePath+".saved", file.SourcePath))
			case "source_missing":
				require.NoError(t, os.Remove(file.SourcePath))
			case "retention_changed":
				plan.Evidence[1].Unavailable = "different provenance"
				_, err := archiveRuntimeEvidence(plan)
				require.ErrorContains(t, err, "different retention evidence")
				return
			}
			require.Error(t, verifyRuntimeArchive(archive))
			if variant == "manifest_corrupt" || variant == "source_changed" || variant == "source_symlink" || variant == "artifact_symlink" {
				_, err := archiveRuntimeEvidence(plan)
				require.Error(t, err, "resumption must not overwrite failed evidence")
			}
			if variant != "artifact_symlink" {
				require.NoError(t, runtimeVerifyFile(file.ArchivePath, file.SHA256), "retained payload remains exact after rejection")
			}
		})
	}
}

func TestEvalRuntimeCommandPreflight(t *testing.T) {
	reasons := map[string]string{
		"missing_receipt": "ownership receipt", "invalid_project": "valid Compose project", "invalid_timeout": "stop timeout",
		"directory_is_file": "not a directory", "corrupt_receipt": "invalid paired JSON", "receipt_identity": "invalid runtime receipt identity",
		"receipt_state": "invalid runtime lifecycle state", "project_mismatch": "project differs", "runtime_locked": "owned by another process",
		"project_locked": "owned by another process", "retired_record": "cannot enroll", "retired_start": "cannot resume",
		"resume_with_new": "resumes without", "replacement_overlaps": "must not overlap", "replacement_missing": "no such file",
		"replacement_symlink": "symlink", "replacement_resume_missing": "no such file", "replacement_resume_unverified": "retirement is incomplete",
		"default_project_missing_compose": "no such file",
	}
	for _, variant := range []string{"missing_receipt", "invalid_project", "invalid_timeout", "directory_is_file", "corrupt_receipt", "receipt_identity", "receipt_state", "project_mismatch", "runtime_locked", "project_locked", "retired_record", "retired_start", "resume_with_new", "replacement_overlaps", "replacement_missing", "replacement_symlink", "replacement_resume_missing", "replacement_resume_unverified", "default_project_missing_compose"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "runtime")
			receipt := runtimeReceipt{SchemaVersion: runtimeSchema, Directory: dir, ID: uuid.NewString(), Project: "eval627-" + uuid.NewString(), State: "preparing"}
			command := RuntimeCommand{Action: "record", Directory: dir, Project: receipt.Project}
			t.Cleanup(func() { removeEvalRuntimeFixture(t, root, receipt) })
			switch variant {
			case "missing_receipt":
				command.Action = "verify"
			case "invalid_project":
				command.Project = "../outside"
			case "invalid_timeout":
				command.StopTimeout = 50 * time.Millisecond
			case "directory_is_file":
				require.NoError(t, os.WriteFile(dir, []byte("keep"), 0o600))
			case "corrupt_receipt":
				require.NoError(t, os.Mkdir(dir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, runtimeReceiptName), []byte("{"), 0o600))
			case "receipt_identity":
				receipt.SchemaVersion = "unknown"
				require.NoError(t, saveRuntimeReceipt(receipt))
			case "receipt_state":
				receipt.State = "unknown"
				require.NoError(t, saveRuntimeReceipt(receipt))
			case "project_mismatch":
				require.NoError(t, saveRuntimeReceipt(receipt))
				command.Project += "-different"
			case "runtime_locked", "project_locked":
				require.NoError(t, os.Mkdir(dir, 0o700))
				path := filepath.Join(dir, "runtime.lock")
				if variant == "project_locked" {
					locks, err := runtimeLockDirectory()
					require.NoError(t, err)
					path = filepath.Join(locks, "project-"+receipt.Project+".lock")
				}
				lock, err := runtimeLock(path)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, lock.Close()) })
			case "retired_record", "retired_start":
				receipt.State = "retired"
				require.NoError(t, saveRuntimeReceipt(receipt))
				command.Action = strings.TrimPrefix(variant, "retired_")
			case "resume_with_new":
				require.NoError(t, saveRuntimeReceipt(receipt))
				command.Action, command.New = "start", true
			case "replacement_overlaps", "replacement_missing", "replacement_symlink":
				command.Action, command.Supersedes = "start", filepath.Join(root, "missing")
				if variant == "replacement_overlaps" {
					command.Supersedes = root
				}
				if variant == "replacement_symlink" {
					require.NoError(t, os.Symlink(root, command.Supersedes))
				}
			case "replacement_resume_missing", "replacement_resume_unverified":
				receipt.Supersedes = filepath.Join(root, "old")
				if variant == "replacement_resume_unverified" {
					old := receipt
					old.Directory, old.State = receipt.Supersedes, "retired"
					require.NoError(t, saveRuntimeReceipt(old))
				}
				require.NoError(t, saveRuntimeReceipt(receipt))
				command.Action = "start"
			case "default_project_missing_compose":
				t.Setenv("DENSE_MEM_EVAL_COMPOSE_PROJECT", receipt.Project)
				command.Action, command.Project, command.New = "start", "", true
				command.ComposeFile = filepath.Join(root, "missing-compose.json")
			}
			before, beforeErr := os.ReadFile(filepath.Join(dir, runtimeReceiptName))
			require.ErrorContains(t, RunRuntimeCommand(context.Background(), command, io.Discard), reasons[variant])
			after, afterErr := os.ReadFile(filepath.Join(dir, runtimeReceiptName))
			if beforeErr == nil {
				require.NoError(t, afterErr)
				require.Equal(t, before, after, "rejected preflight must preserve the receipt")
			} else {
				require.Error(t, afterErr, "rejected preflight must not acknowledge ownership")
			}
		})
	}
}
