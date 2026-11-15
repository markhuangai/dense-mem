package evalretire

import (
	"context"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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

func TestEvalRuntimeResourceLocks(t *testing.T) {
	for _, variant := range []string{"existing", "missing", "missing_no_containers", "missing_invalid_container", "symlink", "symlink_parent", "not_directory", "permission", "contended"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			monitor := filepath.Join(root, "z-monitor")
			lockDir, err := runtimeLockDirectory()
			require.NoError(t, err)
			receipt := runtimeReceipt{MonitorDirs: []string{monitor, monitor}}
			for range 2 {
				id := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
				receipt.Containers = append(receipt.Containers, runtimeContainer{ID: id})
				t.Cleanup(func() {
					err := os.Remove(filepath.Join(lockDir, id+".lock"))
					if !os.IsNotExist(err) {
						require.NoError(t, err)
					}
				})
			}
			receipt.Containers = append(receipt.Containers, receipt.Containers[0])
			var expected error
			reason, releasedPath := "", ""
			switch variant {
			case "missing":
			case "missing_no_containers":
				receipt.Containers, expected = nil, os.ErrNotExist
			case "missing_invalid_container":
				receipt.Containers[0].ID = "invalid"
				reason = "invalid recorded container identity"
			case "symlink", "symlink_parent":
				require.NoError(t, os.Symlink(filepath.Join(root, "absent-target"), monitor))
				if variant == "symlink_parent" {
					receipt.MonitorDirs = []string{filepath.Join(monitor, "absent-child")}
				}
				reason = "symlink"
			case "not_directory":
				require.NoError(t, os.WriteFile(monitor, []byte("preserve"), 0o600))
				expected = syscall.ENOTDIR
			case "permission":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses directory search permissions")
				}
				require.NoError(t, os.Mkdir(monitor, 0o000))
				t.Cleanup(func() { require.NoError(t, os.Chmod(monitor, 0o700)) })
				receipt.MonitorDirs = []string{filepath.Join(monitor, "absent-child")}
				expected = os.ErrPermission
			default:
				require.NoError(t, os.Mkdir(monitor, 0o700))
				if variant == "contended" {
					first := filepath.Join(root, "a-monitor")
					require.NoError(t, os.Mkdir(first, 0o700))
					receipt.MonitorDirs = append(receipt.MonitorDirs, first)
					releasedPath = filepath.Join(first, "paired.lock")
					held, err := runtimeLock(filepath.Join(monitor, "paired.lock"))
					require.NoError(t, err)
					t.Cleanup(func() { require.NoError(t, held.Close()) })
					expected = syscall.EWOULDBLOCK
				}
			}
			locks, err := lockRuntimeResources(receipt)
			t.Cleanup(func() {
				for _, lock := range locks {
					require.NoError(t, lock.Close())
				}
			})
			if expected != nil || reason != "" {
				require.Nil(t, locks)
				if expected != nil {
					require.ErrorIs(t, err, expected)
				} else {
					require.ErrorContains(t, err, reason)
				}
				if releasedPath != "" {
					lock, err := runtimeLock(releasedPath)
					require.NoError(t, err, "partial acquisition must release earlier monitor locks")
					require.NoError(t, lock.Close())
				}
				return
			}
			require.NoError(t, err)
			wantLocks := 3
			if variant == "missing" {
				wantLocks = 2
				require.NoDirExists(t, monitor)
			}
			require.Len(t, locks, wantLocks, "duplicate paths must not acquire the same lock twice")
			for _, lock := range locks {
				_, err := runtimeLock(lock.Name())
				require.ErrorIs(t, err, syscall.EWOULDBLOCK)
			}
		})
	}
}

func TestEvalRuntimeArchive(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "runtime", "postgres")
	require.NoError(t, os.MkdirAll(data, 0o700))
	input := filepath.Join(root, "input.json")
	require.NoError(t, os.WriteFile(input, []byte("private audit input\n"), 0o600))
	hash, err := runtimeFileHash(input)
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
	require.NoError(t, readRuntimeJSON(planPath, &plan))
	source := filepath.Join(root, "evidence.json")
	require.NoError(t, os.WriteFile(source, []byte("retained evidence\n"), 0o600))
	hash, err := runtimeFileHash(source)
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
				require.NoError(t, writeRuntimeJSON(manifest, archive))
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

func evalRuntimeIncompletePlan(t *testing.T, root string) string {
	t.Helper()
	plan := runtimeRetentionPlan{SchemaVersion: runtimeSchema, ArchiveDir: filepath.Join(root, "archive"), Outcome: "incomplete"}
	for _, kind := range runtimeEvidenceKinds {
		plan.Evidence = append(plan.Evidence, runtimeEvidence{Kind: kind, Unavailable: "startup ended before the evaluation began"})
	}
	path := filepath.Join(root, "retention.json")
	require.NoError(t, writeRuntimeJSON(path, plan))
	return path
}

func TestMaintenanceRejectsStartupAndMissingOwnership(t *testing.T) {
	for _, action := range []string{"start", "resume", "retire", "verify"} {
		require.Error(t, RunRuntimeCommand(context.Background(), RuntimeCommand{Action: action, Directory: filepath.Join(t.TempDir(), "runtime")}, io.Discard))
	}
}
