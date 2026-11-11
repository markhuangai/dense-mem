package evalharness

import (
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

func contractTool(name string) (registry.Tool, bool) {
	for _, tool := range registry.ContractTools() {
		if tool.Name == name {
			return tool, true
		}
	}
	return registry.Tool{}, false
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
			return fmt.Errorf("evidence row %d (%s): %w", index+1, item.SourceDocID, err)
		}
	}
	return nil
}
