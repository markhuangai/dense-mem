package evalharness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"strings"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

const (
	v2DerivationReportSchema = "dense-mem.eval.v2_derivation.v1"
	derivedSuiteFileName     = "suite.jsonl"
)

// ValidateV2Derivation proves a V2 seed retained its V1 evidence and sidecar
// inputs, then validates every relationship with the current public contract.

func contractTool(name string) (registry.Tool, bool) {
	for _, tool := range registry.ContractTools() {
		if tool.Name == name {
			return tool, true
		}
	}
	return registry.Tool{}, false
}

func corpusEvidenceIdentityHash(corpus []CorpusItem) (string, error) {
	type evidenceIdentity struct {
		SourceDocID   string         `json:"source_doc_id"`
		Title         string         `json:"title,omitempty"`
		Content       string         `json:"content"`
		SourceDataset string         `json:"source_dataset,omitempty"`
		SourceType    string         `json:"source_type,omitempty"`
		Authority     string         `json:"authority,omitempty"`
		SourceQuality float64        `json:"source_quality,omitempty"`
		Labels        []string       `json:"labels,omitempty"`
		Metadata      map[string]any `json:"metadata,omitempty"`
	}
	identity := make([]evidenceIdentity, 0, len(corpus))
	for _, item := range corpus {
		identity = append(identity, evidenceIdentity{
			SourceDocID: item.SourceDocID, Title: item.Title, Content: item.Content, SourceDataset: item.SourceDataset,
			SourceType: item.SourceType, Authority: item.Authority, SourceQuality: item.SourceQuality, Labels: item.Labels, Metadata: item.Metadata,
		})
	}
	return canonicalJSONHash(identity)
}

func validateSameManifestCounts(source, derived *SeedManifest) error {
	sourceHash, err := canonicalJSONHash(source.Counts)
	if err != nil {
		return err
	}
	derivedHash, err := canonicalJSONHash(derived.Counts)
	if err != nil {
		return err
	}
	if sourceHash != derivedHash {
		return fmt.Errorf("derived manifest counts differ from source manifest counts")
	}
	return nil
}

func copySeedArtifacts(sourceManifestPath string, sourceManifest *SeedManifest, outputDir string) error {
	for _, name := range manifestArtifactFiles(sourceManifest) {
		sourcePath, err := safeSeedFilePath(sourceManifestPath, name)
		if err != nil {
			return err
		}
		targetPath, err := derivedSeedPath(outputDir, name)
		if err != nil {
			return err
		}
		if err := copyFileExact(sourcePath, targetPath); err != nil {
			return fmt.Errorf("copy seed artifact %s: %w", name, err)
		}
	}
	return nil
}

func validateCopiedSeedArtifacts(sourceManifestPath string, sourceManifest *SeedManifest, derivedManifestPath string, derivedManifest *SeedManifest) ([]string, error) {
	sourceFiles := manifestArtifactFiles(sourceManifest)
	derivedFiles := manifestArtifactFiles(derivedManifest)
	if strings.Join(sourceFiles, "\n") != strings.Join(derivedFiles, "\n") {
		return nil, fmt.Errorf("derived manifest artifact file declarations differ from source")
	}
	hashes := make([]string, 0, len(sourceFiles))
	for _, name := range sourceFiles {
		sourcePath, err := safeSeedFilePath(sourceManifestPath, name)
		if err != nil {
			return nil, err
		}
		derivedPath, err := safeSeedFilePath(derivedManifestPath, name)
		if err != nil {
			return nil, err
		}
		hash, err := validateCopiedFile(sourcePath, derivedPath, name)
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, name+":"+hash)
	}
	return hashes, nil
}

func manifestArtifactFiles(manifest *SeedManifest) []string {
	if manifest == nil {
		return nil
	}
	files := make([]string, 0, 6)
	for _, name := range []string{
		manifest.CasesFile, manifest.QrelsFile, manifest.AnswersFile, manifest.HardNegativesFile,
		manifest.TransformsFile, manifest.LicensesFile,
	} {
		if strings.TrimSpace(name) != "" {
			files = append(files, name)
		}
	}
	return files
}

func safeSeedFilePath(manifestPath, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("seed artifact file %q must be a non-empty relative path", name)
	}
	cleaned := filepath.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("seed artifact file %q escapes the seed directory", name)
	}
	return filepath.Join(filepath.Dir(manifestPath), cleaned), nil
}

func derivedSeedPath(outputDir, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("derived seed file %q must be a non-empty relative path", name)
	}
	cleaned := filepath.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("derived seed file %q escapes the output directory", name)
	}
	return filepath.Join(outputDir, cleaned), nil
}

func copyFileExact(sourcePath, targetPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file")
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		return err
	}
	return target.Close()
}

func validateCopiedFile(sourcePath, derivedPath, name string) (string, error) {
	sourceHash, err := sha256File(sourcePath)
	if err != nil {
		return "", err
	}
	derivedHash, err := sha256File(derivedPath)
	if err != nil {
		return "", err
	}
	if sourceHash != derivedHash {
		return "", fmt.Errorf("derived %s is not byte-identical to source", name)
	}
	return sourceHash, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func sameFileBytes(left, right string) (bool, error) {
	leftBytes, err := os.ReadFile(left)
	if err != nil {
		return false, err
	}
	rightBytes, err := os.ReadFile(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftBytes, rightBytes), nil
}

func validateEvidenceCorpus(manifest *SeedManifest, corpus []CorpusItem) error {
	tool, ok := contractTool(registry.ToolRemember)
	if !ok {
		return fmt.Errorf("remember contract is unavailable")
	}
	for index, item := range corpus {
		raw, err := json.Marshal(corpusRememberInput(item))
		if err != nil {
			return err
		}
		var input map[string]any
		if err := json.Unmarshal(raw, &input); err != nil {
			return err
		}
		if err := registry.ValidateContractInput(tool, input, []string{"read", "write"}); err != nil {
			return fmt.Errorf("Evidence row %d (%s): %w", index+1, item.SourceDocID, err)
		}
	}
	return nil
}
