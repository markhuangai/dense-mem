package evalharness

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	remember "github.com/markhuangai/dense-mem/internal/remember/service"
)

const v3RepairLockSchema = "dense-mem.eval.encoding_repairs.v1"

type DeriveV3SeedOptions struct {
	Cohort                 V2CohortValidationOptions
	RelationshipLedgerPath string
	RepairLockPath         string
	OutputDir              string
}

type v3TextReplacement struct {
	Before string `json:"before"`
	After  string `json:"after"`
	Count  int    `json:"count"`
}

type v3ContentRepair struct {
	SourceDocID       string              `json:"source_doc_id"`
	BeforeContentHash string              `json:"before_content_sha256"`
	AfterContentHash  string              `json:"after_content_sha256"`
	Replacements      []v3TextReplacement `json:"replacements"`
}

type v3RepairLock struct {
	SchemaVersion          string            `json:"schema_version"`
	SeedID                 string            `json:"seed_id"`
	FilteredSeedHash       string            `json:"filtered_seed_hash"`
	RelationshipLedgerHash string            `json:"relationship_ledger_sha256"`
	Repairs                []v3ContentRepair `json:"repairs"`
}

type V3DerivationReport struct {
	SchemaVersion           string             `json:"schema_version"`
	ParentSeedHash          string             `json:"parent_seed_hash"`
	FilteredSeedHash        string             `json:"filtered_seed_hash"`
	CohortLockHash          string             `json:"cohort_lock_sha256"`
	RepairLockHash          string             `json:"repair_lock_sha256"`
	RelationshipLedgerHash  string             `json:"source_relationship_ledger_sha256"`
	LegacyModalityFields    int                `json:"legacy_modality_fields_removed"`
	RepairedDocuments       int                `json:"repaired_documents"`
	RepairedSequences       int                `json:"repaired_sequences"`
	AdmissionValidatedCount int                `json:"admission_validated_count"`
	Repairs                 []v3ContentRepair  `json:"repairs"`
	Derivation              V2DerivationReport `json:"derivation"`
}

// DeriveV3Seed makes explicit locked encoding repairs before reusing the
// evidence-preserving V2 derivation; historical cohort validation stays exact.
func DeriveV3Seed(opts DeriveV3SeedOptions) (V3DerivationReport, error) {
	var report V3DerivationReport
	if opts.OutputDir == "" || opts.RelationshipLedgerPath == "" || opts.RepairLockPath == "" {
		return report, fmt.Errorf("output directory, relationship ledger, and repair lock are required")
	}
	if _, err := os.Lstat(opts.OutputDir); !os.IsNotExist(err) {
		return report, fmt.Errorf("derived seed output must not exist: %s", opts.OutputDir)
	}
	cohort, cohortHash, err := loadCohortFilterLock(opts.Cohort.CohortLockPath)
	if err != nil {
		return report, err
	}
	parent, err := LoadSeedManifest(opts.Cohort.ParentManifestPath)
	if err != nil {
		return report, err
	}
	source, err := LoadSeedManifest(opts.Cohort.FilteredManifestPath)
	if err != nil {
		return report, err
	}
	if parent.SchemaVersion != SeedSchemaVersion || source.SchemaVersion != SeedSchemaVersion {
		return report, fmt.Errorf("parent and filtered seeds must use %s", SeedSchemaVersion)
	}
	parentHash, err := SeedHash(opts.Cohort.ParentManifestPath, parent)
	if err != nil {
		return report, err
	}
	sourceHash, err := SeedHash(opts.Cohort.FilteredManifestPath, source)
	if err != nil {
		return report, err
	}
	if err = validateCohortFilterLockBinding(cohort, parent, source, parentHash, sourceHash); err != nil {
		return report, err
	}
	if err = validateV1CohortFiles(opts.Cohort, parent, source, cohort); err != nil {
		return report, err
	}
	var lock v3RepairLock
	if err = readJSONFile(opts.RepairLockPath, &lock); err != nil {
		return report, err
	}
	if lock.SchemaVersion != v3RepairLockSchema || lock.SeedID != "public_6axis_1k_v3" || lock.FilteredSeedHash != sourceHash {
		return report, fmt.Errorf("repair lock schema, seed ID, or filtered seed hash mismatch")
	}
	ledgerHash, err := sha256File(opts.RelationshipLedgerPath)
	if err != nil {
		return report, err
	}
	if ledgerHash != lock.RelationshipLedgerHash {
		return report, fmt.Errorf("repair lock relationship ledger hash mismatch")
	}
	corpus, err := LoadCorpus(opts.Cohort.FilteredManifestPath, source)
	if err != nil {
		return report, err
	}
	repaired, err := repairV3Corpus(corpus, lock.Repairs)
	if err != nil {
		return report, err
	}
	if err = os.MkdirAll(filepath.Dir(opts.OutputDir), 0o755); err != nil {
		return report, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(opts.OutputDir), ".v3-derivation-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(stage)
	ledgerPath := filepath.Join(stage, "ledger.jsonl")
	legacyFields, err := normalizeV3Ledger(opts.RelationshipLedgerPath, ledgerPath)
	if err != nil {
		return report, err
	}
	ledger, excluded, err := loadRelationshipLedger(ledgerPath, source, sourceHash, corpus, opts.Cohort.CohortLockPath)
	if err != nil {
		return report, err
	}
	retainedLedger := make([]relationshipLedgerRow, 0, len(corpus))
	for _, item := range corpus {
		retainedLedger = append(retainedLedger, ledger[item.SourceDocID])
	}
	if err = writeJSONL(ledgerPath, retainedLedger); err != nil {
		return report, err
	}
	sourceDir := filepath.Join(stage, "repaired_source")
	if err = os.Mkdir(sourceDir, 0o755); err != nil {
		return report, err
	}
	if err = copySeedArtifacts(opts.Cohort.FilteredManifestPath, source, sourceDir); err != nil {
		return report, err
	}
	if err = writeJSONL(filepath.Join(sourceDir, source.CorpusFile), repaired); err != nil {
		return report, err
	}
	manifestPath := filepath.Join(sourceDir, "seed_manifest.json")
	source.ValidationReportFile = "validation_report.json"
	if err = writeJSONFile(manifestPath, source); err != nil {
		return report, err
	}
	repairedHash, err := SeedHash(manifestPath, source)
	if err != nil {
		return report, err
	}
	if err = writeJSONFile(filepath.Join(sourceDir, source.ValidationReportFile), seedValidationReport{
		SchemaVersion: "dense-mem.eval.validation.v1", SeedID: source.SeedID, Status: "passed", SeedHash: repairedHash,
	}); err != nil {
		return report, err
	}
	output := filepath.Join(stage, "seed")
	derived, err := DeriveV2Seed(DeriveV2SeedOptions{
		SourceManifestPath: manifestPath, SourceSuitePath: opts.Cohort.FilteredSuitePath,
		RelationshipLedgerPath: ledgerPath, OutputDir: output, SeedID: lock.SeedID,
	})
	if err != nil {
		return report, err
	}
	derived.ExcludedLedgerRows = excluded
	if err = writeJSONFile(filepath.Join(output, "v2_derivation_report.json"), derived); err != nil {
		return report, err
	}
	derivedManifestPath := filepath.Join(output, "seed_manifest.json")
	manifest, err := LoadSeedManifest(derivedManifestPath)
	if err != nil {
		return report, err
	}
	derivedCorpus, err := LoadCorpus(derivedManifestPath, manifest)
	if err != nil {
		return report, err
	}
	failures := []string{}
	for _, item := range derivedCorpus {
		scan, scanErr := remember.ScanSubmissionWithProviderProposal([]string{item.Content}, map[string]any{"relationships": item.Relationships})
		if scanErr != nil {
			rules := []string{}
			for _, signal := range scan.Signals {
				rules = append(rules, signal.RuleID)
			}
			failures = append(failures, fmt.Sprintf("%s: %v [%s]", item.SourceDocID, scanErr, strings.Join(rules, ",")))
		}
	}
	if len(failures) > 0 {
		return report, fmt.Errorf("production admission rejected %d rows: %s", len(failures), strings.Join(failures, "; "))
	}
	repairHash, err := sha256File(opts.RepairLockPath)
	if err != nil {
		return report, err
	}
	report = V3DerivationReport{
		SchemaVersion: "dense-mem.eval.v3_derivation.v1", ParentSeedHash: parentHash, FilteredSeedHash: sourceHash,
		CohortLockHash: cohortHash, RepairLockHash: repairHash, RelationshipLedgerHash: ledgerHash,
		LegacyModalityFields: legacyFields, RepairedDocuments: len(lock.Repairs), AdmissionValidatedCount: len(derivedCorpus),
		Repairs: lock.Repairs, Derivation: derived,
	}
	for _, repair := range lock.Repairs {
		for _, replacement := range repair.Replacements {
			report.RepairedSequences += replacement.Count
		}
	}
	if err = writeJSONFile(filepath.Join(output, "v3_derivation_report.json"), report); err != nil {
		return report, err
	}
	if err = copyFileExact(opts.RepairLockPath, filepath.Join(output, "encoding_repairs.json")); err != nil {
		return report, err
	}
	if err = os.Rename(sourceDir, filepath.Join(output, "repaired_source")); err != nil {
		return report, err
	}
	if err = os.Rename(ledgerPath, filepath.Join(output, "relationship_ledger.jsonl")); err != nil {
		return report, err
	}
	if _, err = os.Lstat(opts.OutputDir); !os.IsNotExist(err) {
		return report, fmt.Errorf("derived seed output already exists: %s", opts.OutputDir)
	}
	if err = os.Rename(output, opts.OutputDir); err != nil {
		return report, err
	}
	return report, nil
}

func repairV3Corpus(corpus []CorpusItem, repairs []v3ContentRepair) ([]CorpusItem, error) {
	byID := map[string]v3ContentRepair{}
	for _, repair := range repairs {
		if _, exists := byID[repair.SourceDocID]; exists {
			return nil, fmt.Errorf("duplicate repair source %s", repair.SourceDocID)
		}
		byID[repair.SourceDocID] = repair
	}
	result := append([]CorpusItem(nil), corpus...)
	for index, item := range result {
		repair, exists := byID[item.SourceDocID]
		if !exists {
			continue
		}
		if contentSHA256(item.Content) != repair.BeforeContentHash {
			return nil, fmt.Errorf("repair source %s content hash mismatch", item.SourceDocID)
		}
		for _, replacement := range repair.Replacements {
			encoded := []byte{}
			for _, value := range replacement.Before {
				if value > 255 {
					return nil, fmt.Errorf("repair is not reversible UTF-8-as-Latin-1")
				}
				encoded = append(encoded, byte(value))
			}
			if replacement.Before == replacement.After || !utf8.Valid(encoded) || string(encoded) != replacement.After || replacement.Count < 1 {
				return nil, fmt.Errorf("repair is not reversible UTF-8-as-Latin-1")
			}
			if strings.Count(item.Content, replacement.Before) != replacement.Count {
				return nil, fmt.Errorf("repair source %s replacement count mismatch", item.SourceDocID)
			}
			item.Content = strings.ReplaceAll(item.Content, replacement.Before, replacement.After)
		}
		if item.Content == result[index].Content || contentSHA256(item.Content) != repair.AfterContentHash {
			return nil, fmt.Errorf("repair source %s resulting content hash mismatch", item.SourceDocID)
		}
		result[index] = item
		delete(byID, item.SourceDocID)
	}
	if len(byID) > 0 {
		return nil, fmt.Errorf("repair source is absent from retained corpus")
	}
	return result, nil
}

func normalizeV3Ledger(sourcePath, targetPath string) (int, error) {
	var rows []map[string]json.RawMessage
	if err := readJSONL(sourcePath, &rows); err != nil {
		return 0, err
	}
	ledger := make([]relationshipLedgerRow, 0, len(rows))
	legacy := 0
	for _, row := range rows {
		if raw, exists := row["modality"]; exists {
			var modality string
			if err := json.Unmarshal(raw, &modality); err != nil {
				return 0, err
			}
			switch modality {
			case "statement", "question", "proposal", "speculation", "quoted":
			default:
				return 0, fmt.Errorf("unsupported legacy ledger modality %q", modality)
			}
			delete(row, "modality")
			legacy++
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return 0, err
		}
		decoded, err := decodeRelationshipLedgerRow(encoded)
		if err != nil {
			return 0, err
		}
		ledger = append(ledger, decoded)
	}
	return legacy, writeJSONL(targetPath, ledger)
}

func contentSHA256(content string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))
}
