package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func SeedHash(manifestPath string, manifest *SeedManifest) (string, error) {
	hash := sha256.New()
	for _, path := range seedArtifactPaths(manifestPath, manifest) {
		if _, err := fmt.Fprintf(hash, "file:%s\n", filepath.Base(path)); err != nil {
			return "", err
		}
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(hash, f); err != nil {
			_ = f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		if _, err := hash.Write([]byte("\n")); err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func seedArtifactPaths(manifestPath string, manifest *SeedManifest) []string {
	return []string{manifestPath, resolveSeedPath(manifestPath, manifest.CorpusFile), resolveSeedPath(manifestPath, manifest.CasesFile), resolveSeedPath(manifestPath, manifest.QrelsFile)}
}

func canonicalJSONHash(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return "", err
	}
	payload, err = json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func IndexCases(cases []Case) map[string]Case {
	out := make(map[string]Case, len(cases))
	for _, c := range cases {
		out[c.CaseID] = c
	}
	return out
}

func resolveSeedPath(manifestPath, rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(filepath.Dir(manifestPath), rel)
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
