package evalharness

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const MaxCorpusContentCodepoints = 999

func LoadSeedManifest(path string) (*SeedManifest, error) {
	var manifest SeedManifest
	if err := readJSONFile(path, &manifest); err != nil {
		return nil, err
	}
	if strings.TrimSpace(manifest.SchemaVersion) == "" {
		return nil, errors.New("seed manifest missing schema_version")
	}
	if manifest.SchemaVersion != SeedSchemaVersion {
		return nil, fmt.Errorf("unsupported seed schema_version %q", manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.SeedID) == "" {
		return nil, errors.New("seed manifest missing seed_id")
	}
	if strings.TrimSpace(manifest.CorpusFile) == "" || strings.TrimSpace(manifest.CasesFile) == "" || strings.TrimSpace(manifest.QrelsFile) == "" {
		return nil, errors.New("seed manifest must set corpus_file, cases_file, and qrels_file")
	}
	return &manifest, nil
}

func LoadCorpus(manifestPath string, manifest *SeedManifest) ([]CorpusItem, error) {
	items := []CorpusItem{}
	if err := scanCorpusFile(resolveSeedPath(manifestPath, manifest.CorpusFile), func(item CorpusItem) error {
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for i, item := range items {
		if strings.TrimSpace(item.SourceDocID) == "" {
			return nil, fmt.Errorf("corpus row %d missing source_doc_id", i+1)
		}
		if strings.TrimSpace(item.Content) == "" {
			return nil, fmt.Errorf("corpus row %d missing content", i+1)
		}
		if contentLen := utf8.RuneCountInString(item.Content); contentLen > MaxCorpusContentCodepoints {
			return nil, fmt.Errorf("corpus row %d content has %d code points; max is %d", i+1, contentLen, MaxCorpusContentCodepoints)
		}
		if _, ok := seen[item.SourceDocID]; ok {
			return nil, fmt.Errorf("duplicate corpus source_doc_id %q", item.SourceDocID)
		}
		seen[item.SourceDocID] = struct{}{}
	}
	return items, nil
}

func LoadCases(manifestPath string, manifest *SeedManifest) ([]Case, error) {
	var cases []Case
	if err := readJSONL(resolveSeedPath(manifestPath, manifest.CasesFile), &cases); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for i, c := range cases {
		if strings.TrimSpace(c.CaseID) == "" {
			return nil, fmt.Errorf("case row %d missing case_id", i+1)
		}
		if strings.TrimSpace(c.Query) == "" {
			return nil, fmt.Errorf("case row %d missing query", i+1)
		}
		if _, ok := seen[c.CaseID]; ok {
			return nil, fmt.Errorf("duplicate case_id %q", c.CaseID)
		}
		seen[c.CaseID] = struct{}{}
	}
	return cases, nil
}

func LoadQrels(manifestPath string, manifest *SeedManifest) ([]QRel, error) {
	var qrels []QRel
	if err := readJSONL(resolveSeedPath(manifestPath, manifest.QrelsFile), &qrels); err != nil {
		return nil, err
	}
	for i, qrel := range qrels {
		if strings.TrimSpace(qrel.CaseID) == "" {
			return nil, fmt.Errorf("qrels row %d missing case_id", i+1)
		}
		if len(qrel.RequiredRefs) == 0 {
			return nil, fmt.Errorf("qrels row %d has no required_refs", i+1)
		}
	}
	return qrels, nil
}

func LoadExpectedDreams(manifestPath string, manifest *SeedManifest) ([]ExpectedDream, error) {
	if manifest == nil || strings.TrimSpace(manifest.DreamsFile) == "" {
		return nil, nil
	}
	var dreams []ExpectedDream
	if err := readJSONL(resolveSeedPath(manifestPath, manifest.DreamsFile), &dreams); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for i, dream := range dreams {
		if strings.TrimSpace(dream.SourceDocID) == "" {
			return nil, fmt.Errorf("expected dream row %d missing source_doc_id", i+1)
		}
		if len(dream.SourceRefs) < 2 {
			return nil, fmt.Errorf("expected dream row %d must contain at least two source_refs", i+1)
		}
		if _, ok := seen[dream.SourceDocID]; ok {
			return nil, fmt.Errorf("duplicate expected dream source_doc_id %q", dream.SourceDocID)
		}
		seen[dream.SourceDocID] = struct{}{}
	}
	return dreams, nil
}

func LoadSuite(path string) ([]SuiteCase, error) {
	var suite []SuiteCase
	if err := readJSONL(path, &suite); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for i, c := range suite {
		if strings.TrimSpace(c.CaseID) == "" {
			return nil, fmt.Errorf("suite row %d missing case_id", i+1)
		}
		if _, ok := seen[c.CaseID]; ok {
			return nil, fmt.Errorf("duplicate suite case_id %q", c.CaseID)
		}
		seen[c.CaseID] = struct{}{}
	}
	return suite, nil
}

func LoadRecallTraces(path string) ([]RecallTrace, error) {
	var traces []RecallTrace
	if err := readJSONL(path, &traces); err != nil {
		return nil, err
	}
	return traces, nil
}

func SeedHash(manifestPath string, manifest *SeedManifest) (string, error) {
	hash := sha256.New()
	files := []string{manifestPath, resolveSeedPath(manifestPath, manifest.CorpusFile), resolveSeedPath(manifestPath, manifest.CasesFile), resolveSeedPath(manifestPath, manifest.QrelsFile)}
	for _, optional := range []string{manifest.AnswersFile, manifest.HardNegativesFile, manifest.TransformsFile, manifest.DreamsFile, manifest.LicensesFile} {
		if strings.TrimSpace(optional) != "" {
			files = append(files, resolveSeedPath(manifestPath, optional))
		}
	}
	for _, path := range files {
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

func FileHash(path string) (string, error) {
	hash := sha256.New()
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func IndexCases(cases []Case) map[string]Case {
	out := make(map[string]Case, len(cases))
	for _, c := range cases {
		out[c.CaseID] = c
	}
	return out
}

func IndexQrels(qrels []QRel) map[string]QRel {
	out := make(map[string]QRel, len(qrels))
	for _, qrel := range qrels {
		out[qrel.CaseID] = qrel
	}
	return out
}

func resolveSeedPath(manifestPath, rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(filepath.Dir(manifestPath), rel)
}

func readJSONFile(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func readJSONL[T any](path string, out *[]T) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var row T
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		*out = append(*out, row)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
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

func writeJSONL[T any](path string, rows []T) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			return err
		}
	}
	return nil
}
