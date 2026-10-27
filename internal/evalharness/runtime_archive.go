package evalharness

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

var runtimeEvidenceKinds = []string{"frozen_inputs", "provenance", "captures", "failures", "accounting", "private_audit"}

type runtimeArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type runtimeEvidence struct {
	Kind        string            `json:"kind"`
	Files       []runtimeArtifact `json:"files,omitempty"`
	Unavailable string            `json:"unavailable,omitempty"`
}

type runtimeRetentionPlan struct {
	SchemaVersion              string            `json:"schema_version"`
	ArchiveDir                 string            `json:"archive_dir"`
	Outcome                    string            `json:"outcome"`
	Evidence                   []runtimeEvidence `json:"evidence"`
	ProtectedRuntimes          []string          `json:"protected_runtimes"`
	PendingRuntimeDependencies []string          `json:"pending_runtime_dependencies"`
}

type runtimeArchivedFile struct {
	SourcePath  string `json:"source_path"`
	ArchivePath string `json:"archive_path"`
	SHA256      string `json:"sha256"`
}

type runtimeArchive struct {
	Plan  runtimeRetentionPlan  `json:"plan"`
	Files []runtimeArchivedFile `json:"files"`
}

func runtimeRetentionPlanPath(path string, receipt runtimeReceipt) (string, error) {
	clean, err := runtimeCleanPath(path)
	if err != nil {
		return "", err
	}
	for _, bind := range receipt.Binds {
		if runtimeOverlap(clean, bind.Identity.Path) {
			return "", fmt.Errorf("retention plan overlaps disposable runtime storage")
		}
	}
	return clean, nil
}

func validateRuntimeRetention(plan *runtimeRetentionPlan, receipt runtimeReceipt) error {
	if plan.SchemaVersion != runtimeSchema || (plan.Outcome != "complete" && plan.Outcome != "incomplete") {
		return fmt.Errorf("retention requires the runtime schema and an explicit complete/incomplete outcome")
	}
	if len(plan.PendingRuntimeDependencies) != 0 {
		return fmt.Errorf("unresolved report/audit runtime dependencies block retirement")
	}
	archive, err := runtimeCleanPath(plan.ArchiveDir)
	if err != nil {
		return err
	}
	plan.ArchiveDir = archive
	if runtimeOverlap(archive, receipt.Directory) {
		return fmt.Errorf("archive and runtime directory must not overlap")
	}
	seen := map[string]bool{}
	for i := range plan.Evidence {
		evidence := &plan.Evidence[i]
		if !slices.Contains(runtimeEvidenceKinds, evidence.Kind) || seen[evidence.Kind] {
			return fmt.Errorf("retention evidence kind is unknown or duplicated")
		}
		seen[evidence.Kind] = true
		if (len(evidence.Files) == 0) == (evidence.Unavailable == "") {
			return fmt.Errorf("%s requires files or an explicit unavailability reason", evidence.Kind)
		}
		if plan.Outcome == "complete" && evidence.Kind == "captures" && len(evidence.Files) == 0 {
			return fmt.Errorf("a complete evaluation requires retained captures")
		}
		for j := range evidence.Files {
			file := &evidence.Files[j]
			path, err := runtimeCleanPath(file.Path)
			if err != nil {
				return err
			}
			file.Path = path
			if !pairSHA.MatchString(file.SHA256) {
				return fmt.Errorf("retained artifact requires its exact SHA-256")
			}
			if runtimeOverlap(path, archive) || path == filepath.Join(receipt.Directory, runtimeReceiptName) {
				return fmt.Errorf("retained artifact overlaps the archive or mutable lifecycle receipt")
			}
			for _, bind := range receipt.Binds {
				if runtimeOverlap(path, bind.Identity.Path) {
					return fmt.Errorf("retained artifact and disposable storage overlap")
				}
			}
			if err := runtimeVerifyFile(path, file.SHA256); err != nil {
				return err
			}
		}
	}
	if len(seen) != len(runtimeEvidenceKinds) {
		return fmt.Errorf("retention must account for frozen_inputs, provenance, captures, failures, accounting and private_audit")
	}
	for i, dir := range plan.ProtectedRuntimes {
		path, err := runtimeCleanPath(dir)
		if err != nil {
			return err
		}
		if runtimeOverlap(path, receipt.Directory) || runtimeOverlap(path, archive) {
			return fmt.Errorf("protected runtime, archive and retired runtime must not overlap")
		}
		plan.ProtectedRuntimes[i] = path
	}
	return nil
}

func runtimeVerifyFile(path, expected string) error {
	if _, err := runtimeCleanPath(path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("retained artifact must be a regular file")
	}
	hash, err := PairFileHash(path)
	if err != nil {
		return err
	}
	if hash != expected {
		return fmt.Errorf("retained artifact hash mismatch: %s", path)
	}
	return nil
}

func archiveRuntimeEvidence(plan runtimeRetentionPlan) (runtimeArchive, error) {
	archive := runtimeArchive{Plan: plan}
	if _, err := runtimeCleanPath(plan.ArchiveDir); err != nil {
		return archive, err
	}
	if err := os.MkdirAll(plan.ArchiveDir, 0o700); err != nil {
		return archive, err
	}
	info, err := os.Stat(plan.ArchiveDir)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return archive, fmt.Errorf("archive directory must be private (0700)")
	}
	for _, evidence := range plan.Evidence {
		for _, file := range evidence.Files {
			destination := filepath.Join(plan.ArchiveDir, file.SHA256[len("sha256:"):])
			if err := runtimeCopyArtifact(file, destination); err != nil {
				return archive, err
			}
			archive.Files = append(archive.Files, runtimeArchivedFile{SourcePath: file.Path, ArchivePath: destination, SHA256: file.SHA256})
		}
	}
	manifest := filepath.Join(plan.ArchiveDir, "retention.json")
	if _, err := os.Lstat(manifest); err == nil {
		var existing runtimeArchive
		if err := readPairJSON(manifest, &existing); err != nil {
			return archive, err
		}
		before, err := canonicalJSONHash(existing)
		if err != nil {
			return archive, err
		}
		after, err := canonicalJSONHash(archive)
		if err != nil || before != after {
			return archive, fmt.Errorf("archive already belongs to different retention evidence")
		}
	} else if !os.IsNotExist(err) {
		return archive, err
	} else if err := writePairJSON(manifest, archive); err != nil {
		return archive, err
	}
	return archive, verifyRuntimeArchive(archive)
}

func runtimeCopyArtifact(file runtimeArtifact, destination string) error {
	if err := runtimeVerifyFile(file.Path, file.SHA256); err != nil {
		return err
	}
	if _, err := runtimeCleanPath(destination); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return runtimeVerifyFile(destination, file.SHA256)
	} else if !os.IsNotExist(err) {
		return err
	}
	source, err := os.Open(file.Path)
	if err != nil {
		return err
	}
	defer source.Close()
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".archive-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := io.Copy(temporary, source); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := runtimeVerifyFile(temporary.Name(), file.SHA256); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), destination)
}

func verifyRuntimeArchive(archive runtimeArchive) error {
	if _, err := runtimeCleanPath(archive.Plan.ArchiveDir); err != nil {
		return err
	}
	info, err := os.Stat(archive.Plan.ArchiveDir)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("archive directory is missing or not private")
	}
	manifest := filepath.Join(archive.Plan.ArchiveDir, "retention.json")
	if _, err := runtimeCleanPath(manifest); err != nil {
		return err
	}
	var saved runtimeArchive
	if err := readPairJSON(manifest, &saved); err != nil {
		return err
	}
	actual, err := canonicalJSONHash(saved)
	if err != nil {
		return err
	}
	expected, err := canonicalJSONHash(archive)
	if err != nil || actual != expected {
		return fmt.Errorf("retention manifest hash mismatch")
	}
	for _, file := range archive.Files {
		if filepath.Dir(file.ArchivePath) != archive.Plan.ArchiveDir {
			return fmt.Errorf("archived artifact escaped the retention directory")
		}
		if err := runtimeVerifyFile(file.ArchivePath, file.SHA256); err != nil {
			return err
		}
		info, err := os.Stat(file.ArchivePath)
		if err != nil || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("archived artifact is not private")
		}
		if err := runtimeVerifyFile(file.SourcePath, file.SHA256); err != nil {
			return err
		}
	}
	return nil
}
