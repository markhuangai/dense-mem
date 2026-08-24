package evalharness

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

const (
	v2DerivationReportSchema = "dense-mem.eval.v2_derivation.v1"
	derivedSuiteFileName     = "suite.jsonl"
)

type relationshipLedgerRow struct {
	SourceDocID         string `json:"source_doc_id"`
	Support             string `json:"support"`
	SupportOccurrence   *int   `json:"support_occurrence,omitempty"`
	Subject             string `json:"subject"`
	SubjectOccurrence   *int   `json:"subject_occurrence,omitempty"`
	SubjectKind         string `json:"subject_kind,omitempty"`
	Predicate           string `json:"predicate"`
	PredicateOccurrence *int   `json:"predicate_occurrence,omitempty"`
	Object              string `json:"object"`
	ObjectOccurrence    *int   `json:"object_occurrence,omitempty"`
	ObjectKind          string `json:"object_kind,omitempty"`
	Polarity            string `json:"polarity,omitempty"`
}

// DeriveV2Seed copies a V1 seed's evaluation inputs unchanged and adds one
// flat, span-grounded relationship submission to every retained corpus item.
func DeriveV2Seed(opts DeriveV2SeedOptions) (V2DerivationReport, error) {
	sourceManifestPath := strings.TrimSpace(opts.SourceManifestPath)
	sourceSuitePath := strings.TrimSpace(opts.SourceSuitePath)
	ledgerPath := strings.TrimSpace(opts.RelationshipLedgerPath)
	cohortLockPath := strings.TrimSpace(opts.CohortLockPath)
	outputDir := strings.TrimSpace(opts.OutputDir)
	seedID := strings.TrimSpace(opts.SeedID)
	if sourceManifestPath == "" || sourceSuitePath == "" || ledgerPath == "" || outputDir == "" || seedID == "" {
		return V2DerivationReport{}, fmt.Errorf("source manifest, source suite, relationship ledger, output directory, and seed ID are required")
	}
	if _, err := os.Lstat(outputDir); err == nil {
		return V2DerivationReport{}, fmt.Errorf("derived seed output already exists: %s", outputDir)
	} else if !os.IsNotExist(err) {
		return V2DerivationReport{}, err
	}

	sourceManifest, err := LoadSeedManifest(sourceManifestPath)
	if err != nil {
		return V2DerivationReport{}, err
	}
	if sourceManifest.SchemaVersion != SeedSchemaVersion {
		return V2DerivationReport{}, fmt.Errorf("source seed schema_version %q is not %q", sourceManifest.SchemaVersion, SeedSchemaVersion)
	}
	sourceHash, err := SeedHash(sourceManifestPath, sourceManifest)
	if err != nil {
		return V2DerivationReport{}, fmt.Errorf("hash source seed: %w", err)
	}
	sourceCorpus, err := LoadCorpus(sourceManifestPath, sourceManifest)
	if err != nil {
		return V2DerivationReport{}, err
	}
	ledger, excludedLedgerRows, err := loadRelationshipLedger(ledgerPath, sourceManifest, sourceHash, sourceCorpus, cohortLockPath)
	if err != nil {
		return V2DerivationReport{}, err
	}

	targetCorpus := make([]CorpusItem, len(sourceCorpus))
	relationshipCount := 0
	fallbackSourceDocIDs := make([]string, 0)
	derivationErrors := make([]string, 0, 10)
	invalidRelationshipCount := 0
	for index, item := range sourceCorpus {
		relationship, usedFallback, err := flatRelationshipFromLedgerOrFallback(item.Content, ledger[item.SourceDocID])
		if err != nil {
			invalidRelationshipCount++
			if len(derivationErrors) < 10 {
				derivationErrors = append(derivationErrors, fmt.Sprintf("row %d (%s): %v", index+1, item.SourceDocID, err))
			}
			continue
		}
		targetCorpus[index] = item
		targetCorpus[index].Relationships = []any{relationship}
		relationshipCount++
		if usedFallback {
			fallbackSourceDocIDs = append(fallbackSourceDocIDs, item.SourceDocID)
		}
	}
	if invalidRelationshipCount > 0 {
		return V2DerivationReport{}, fmt.Errorf("relationship ledger has %d unresolved retained V1 rows: %s", invalidRelationshipCount, strings.Join(derivationErrors, "; "))
	}
	evidenceIdentityHash, err := corpusEvidenceIdentityHash(sourceCorpus)
	if err != nil {
		return V2DerivationReport{}, fmt.Errorf("hash source evidence identity: %w", err)
	}
	ledgerHash, err := sha256File(ledgerPath)
	if err != nil {
		return V2DerivationReport{}, fmt.Errorf("hash relationship ledger: %w", err)
	}

	outputParent := filepath.Dir(outputDir)
	if err := os.MkdirAll(outputParent, 0o755); err != nil {
		return V2DerivationReport{}, err
	}
	stage, err := os.MkdirTemp(outputParent, "."+filepath.Base(outputDir)+".stage-")
	if err != nil {
		return V2DerivationReport{}, err
	}
	defer os.RemoveAll(stage)

	if err := copySeedArtifacts(sourceManifestPath, sourceManifest, stage); err != nil {
		return V2DerivationReport{}, err
	}
	corpusPath, err := derivedSeedPath(stage, sourceManifest.CorpusFile)
	if err != nil {
		return V2DerivationReport{}, err
	}
	if err := writeJSONL(corpusPath, targetCorpus); err != nil {
		return V2DerivationReport{}, fmt.Errorf("write derived corpus: %w", err)
	}
	if err := copyFileExact(sourceSuitePath, filepath.Join(stage, derivedSuiteFileName)); err != nil {
		return V2DerivationReport{}, fmt.Errorf("copy source suite: %w", err)
	}

	targetManifest := *sourceManifest
	targetManifest.SchemaVersion = SeedSchemaVersionV2
	targetManifest.SeedID = seedID
	targetManifest.Description = "Issue #149 V2 derivation: retained V1 evidence with flat relationship submissions."
	targetManifest.GeneratedAt = ""
	targetManifest.ValidationReportFile = "validation_report.json"
	targetManifest.ParentSeedID = sourceManifest.SeedID
	targetManifest.ParentSeedHash = sourceHash
	targetManifest.EvidenceIdentityHash = evidenceIdentityHash
	targetManifest.RelationshipLedgerHash = ledgerHash
	targetManifest.RelationshipCount = relationshipCount
	manifestPath := filepath.Join(stage, "seed_manifest.json")
	if err := writeJSONFile(manifestPath, targetManifest); err != nil {
		return V2DerivationReport{}, fmt.Errorf("write derived manifest: %w", err)
	}
	targetHash, err := SeedHash(manifestPath, &targetManifest)
	if err != nil {
		return V2DerivationReport{}, fmt.Errorf("hash derived seed: %w", err)
	}
	if err := writeJSONFile(filepath.Join(stage, targetManifest.ValidationReportFile), seedValidationReport{
		SchemaVersion: "dense-mem.eval.validation.v1",
		SeedID:        targetManifest.SeedID,
		Status:        "passed",
		SeedHash:      targetHash,
	}); err != nil {
		return V2DerivationReport{}, fmt.Errorf("write derived validation report: %w", err)
	}

	report, err := ValidateV2Derivation(sourceManifestPath, sourceSuitePath, manifestPath, filepath.Join(stage, derivedSuiteFileName))
	if err != nil {
		return V2DerivationReport{}, err
	}
	report.ExcludedLedgerRows = excludedLedgerRows
	report.FallbackRelationshipCount = len(fallbackSourceDocIDs)
	report.FallbackSourceDocIDs = append([]string(nil), fallbackSourceDocIDs...)
	if err := writeJSONFile(filepath.Join(stage, "v2_derivation_report.json"), report); err != nil {
		return V2DerivationReport{}, fmt.Errorf("write V2 derivation report: %w", err)
	}
	if _, err := os.Lstat(outputDir); err == nil {
		return V2DerivationReport{}, fmt.Errorf("derived seed output already exists: %s", outputDir)
	} else if !os.IsNotExist(err) {
		return V2DerivationReport{}, err
	}
	if err := os.Rename(stage, outputDir); err != nil {
		return V2DerivationReport{}, err
	}
	return report, nil
}

// ValidateV2Derivation proves a V2 seed retained its V1 evidence and sidecar
// inputs, then validates every relationship with the current public contract.
func ValidateV2Derivation(sourceManifestPath, sourceSuitePath, derivedManifestPath, derivedSuitePath string) (V2DerivationReport, error) {
	sourceManifest, err := LoadSeedManifest(sourceManifestPath)
	if err != nil {
		return V2DerivationReport{}, err
	}
	if sourceManifest.SchemaVersion != SeedSchemaVersion {
		return V2DerivationReport{}, fmt.Errorf("source seed schema_version %q is not %q", sourceManifest.SchemaVersion, SeedSchemaVersion)
	}
	derivedManifest, err := LoadSeedManifest(derivedManifestPath)
	if err != nil {
		return V2DerivationReport{}, err
	}
	if derivedManifest.SchemaVersion != SeedSchemaVersionV2 {
		return V2DerivationReport{}, fmt.Errorf("derived seed schema_version %q is not %q", derivedManifest.SchemaVersion, SeedSchemaVersionV2)
	}
	sourceHash, err := SeedHash(sourceManifestPath, sourceManifest)
	if err != nil {
		return V2DerivationReport{}, fmt.Errorf("hash source seed: %w", err)
	}
	derivedHash, err := SeedHash(derivedManifestPath, derivedManifest)
	if err != nil {
		return V2DerivationReport{}, fmt.Errorf("hash derived seed: %w", err)
	}
	if derivedManifest.ParentSeedID != sourceManifest.SeedID {
		return V2DerivationReport{}, fmt.Errorf("derived parent_seed_id %q does not match source seed_id %q", derivedManifest.ParentSeedID, sourceManifest.SeedID)
	}
	if derivedManifest.ParentSeedHash != sourceHash {
		return V2DerivationReport{}, fmt.Errorf("derived parent_seed_hash %q does not match source seed hash %q", derivedManifest.ParentSeedHash, sourceHash)
	}
	sourceCorpus, err := LoadCorpus(sourceManifestPath, sourceManifest)
	if err != nil {
		return V2DerivationReport{}, err
	}
	derivedCorpus, err := LoadCorpus(derivedManifestPath, derivedManifest)
	if err != nil {
		return V2DerivationReport{}, err
	}
	if len(sourceCorpus) != len(derivedCorpus) {
		return V2DerivationReport{}, fmt.Errorf("derived corpus has %d rows; source has %d", len(derivedCorpus), len(sourceCorpus))
	}
	sourceIdentityHash, err := corpusEvidenceIdentityHash(sourceCorpus)
	if err != nil {
		return V2DerivationReport{}, err
	}
	derivedIdentityHash, err := corpusEvidenceIdentityHash(derivedCorpus)
	if err != nil {
		return V2DerivationReport{}, err
	}
	if sourceIdentityHash != derivedIdentityHash {
		return V2DerivationReport{}, fmt.Errorf("derived evidence identity hash %q does not match source %q", derivedIdentityHash, sourceIdentityHash)
	}
	if derivedManifest.EvidenceIdentityHash != sourceIdentityHash {
		return V2DerivationReport{}, fmt.Errorf("derived evidence_identity_hash %q does not match source %q", derivedManifest.EvidenceIdentityHash, sourceIdentityHash)
	}
	for index := range sourceCorpus {
		if sourceCorpus[index].SourceDocID != derivedCorpus[index].SourceDocID {
			return V2DerivationReport{}, fmt.Errorf("derived corpus row %d source_doc_id %q does not match source %q", index+1, derivedCorpus[index].SourceDocID, sourceCorpus[index].SourceDocID)
		}
	}
	if err := validateSameManifestCounts(sourceManifest, derivedManifest); err != nil {
		return V2DerivationReport{}, err
	}

	artifactHashes, err := validateCopiedSeedArtifacts(sourceManifestPath, sourceManifest, derivedManifestPath, derivedManifest)
	if err != nil {
		return V2DerivationReport{}, err
	}
	suiteHash, err := validateCopiedFile(sourceSuitePath, derivedSuitePath, "suite.jsonl")
	if err != nil {
		return V2DerivationReport{}, err
	}
	if err := validateSeedValidationReport(derivedManifestPath, derivedManifest, derivedHash); err != nil {
		return V2DerivationReport{}, err
	}
	if err := validateV2CorpusRelationships(derivedManifest, derivedCorpus); err != nil {
		return V2DerivationReport{}, err
	}
	relationshipCount := 0
	for _, item := range derivedCorpus {
		relationshipCount += len(item.Relationships)
	}
	if derivedManifest.RelationshipCount != relationshipCount {
		return V2DerivationReport{}, fmt.Errorf("derived relationship_count %d does not match corpus relationship count %d", derivedManifest.RelationshipCount, relationshipCount)
	}
	return V2DerivationReport{
		SchemaVersion:          v2DerivationReportSchema,
		Status:                 "passed",
		ParentSeedID:           sourceManifest.SeedID,
		ParentSeedHash:         sourceHash,
		DerivedSeedID:          derivedManifest.SeedID,
		DerivedSeedHash:        derivedHash,
		EvidenceIdentityHash:   sourceIdentityHash,
		CorpusCount:            len(derivedCorpus),
		RelationshipCount:      relationshipCount,
		ContractValidatedCount: len(derivedCorpus),
		CopiedArtifactHashes:   artifactHashes,
		SuiteHash:              suiteHash,
	}, nil
}

func validateV2CorpusRelationships(manifest *SeedManifest, corpus []CorpusItem) error {
	if manifest == nil || manifest.SchemaVersion != SeedSchemaVersionV2 {
		return nil
	}
	remember, ok := contractTool(registry.ToolRemember)
	if !ok {
		return fmt.Errorf("remember contract tool is unavailable")
	}
	for index, item := range corpus {
		if len(item.Relationships) == 0 {
			return fmt.Errorf("V2 corpus row %d (%s) has no relationships", index+1, item.SourceDocID)
		}
		input := map[string]any{
			"idempotency_key": "validate:" + item.SourceDocID,
			"evidence":        []any{map[string]any{"content": item.Content}},
			"relationships":   item.Relationships,
		}
		if err := registry.ValidateContractInput(remember, input, []string{"write"}); err != nil {
			return fmt.Errorf("V2 corpus row %d (%s) relationship contract: %w", index+1, item.SourceDocID, err)
		}
	}
	return nil
}

func contractTool(name string) (registry.Tool, bool) {
	for _, tool := range registry.ContractTools() {
		if tool.Name == name {
			return tool, true
		}
	}
	return registry.Tool{}, false
}

func loadRelationshipLedger(path string, sourceManifest *SeedManifest, sourceHash string, corpus []CorpusItem, cohortLockPath string) (map[string]relationshipLedgerRow, int, error) {
	expected := make(map[string]struct{}, len(corpus))
	for _, item := range corpus {
		expected[item.SourceDocID] = struct{}{}
	}
	allowedExtra, err := allowedExtraLedgerRows(sourceManifest, sourceHash, cohortLockPath)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	ledger := make(map[string]relationshipLedgerRow, len(corpus))
	extras := map[string]struct{}{}
	seenSourceDocIDs := map[string]struct{}{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		row, err := decodeRelationshipLedgerRow([]byte(line))
		if err != nil {
			return nil, 0, fmt.Errorf("relationship ledger %s:%d: %w", path, lineNo, err)
		}
		if err := validateRelationshipLedgerRow(row); err != nil {
			return nil, 0, fmt.Errorf("relationship ledger %s:%d: %w", path, lineNo, err)
		}
		if _, exists := seenSourceDocIDs[row.SourceDocID]; exists {
			return nil, 0, fmt.Errorf("relationship ledger has duplicate source_doc_id %q", row.SourceDocID)
		}
		seenSourceDocIDs[row.SourceDocID] = struct{}{}
		if _, exists := expected[row.SourceDocID]; !exists {
			if _, allowed := allowedExtra[row.SourceDocID]; !allowed {
				return nil, 0, fmt.Errorf("relationship ledger source_doc_id %q is outside the retained V1 cohort", row.SourceDocID)
			}
			extras[row.SourceDocID] = struct{}{}
			continue
		}
		ledger[row.SourceDocID] = row
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("read relationship ledger: %w", err)
	}
	if len(ledger) != len(corpus) {
		missing := make([]string, 0, len(corpus)-len(ledger))
		for sourceDocID := range expected {
			if _, exists := ledger[sourceDocID]; !exists {
				missing = append(missing, sourceDocID)
			}
		}
		sort.Strings(missing)
		return nil, 0, fmt.Errorf("relationship ledger is missing %d retained V1 source_doc_id values; first %q", len(missing), missing[0])
	}
	return ledger, len(extras), nil
}

func decodeRelationshipLedgerRow(raw []byte) (relationshipLedgerRow, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return relationshipLedgerRow{}, err
	}
	allowed := map[string]struct{}{
		"source_doc_id": {}, "support": {}, "support_occurrence": {}, "subject": {}, "subject_occurrence": {},
		"subject_kind": {}, "predicate": {}, "predicate_occurrence": {}, "object": {}, "object_occurrence": {},
		"object_kind": {}, "polarity": {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return relationshipLedgerRow{}, fmt.Errorf("unsupported field %q", field)
		}
	}
	var row relationshipLedgerRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return relationshipLedgerRow{}, err
	}
	return row, nil
}

func validateRelationshipLedgerRow(row relationshipLedgerRow) error {
	if strings.TrimSpace(row.SourceDocID) == "" {
		return fmt.Errorf("source_doc_id is required")
	}
	for name, value := range map[string]string{
		"support": row.Support, "subject": row.Subject, "predicate": row.Predicate, "object": row.Object,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	for name, occurrence := range map[string]*int{
		"support_occurrence": row.SupportOccurrence, "subject_occurrence": row.SubjectOccurrence,
		"predicate_occurrence": row.PredicateOccurrence, "object_occurrence": row.ObjectOccurrence,
	} {
		if occurrence != nil && *occurrence < 0 {
			return fmt.Errorf("%s must be non-negative", name)
		}
	}
	for name, kind := range map[string]string{"subject_kind": row.SubjectKind, "object_kind": row.ObjectKind} {
		if kind != "" && !allowedEntityKind(kind) {
			return fmt.Errorf("%s %q is unsupported", name, kind)
		}
	}
	if row.Polarity != "" && row.Polarity != "+" && row.Polarity != "-" {
		return fmt.Errorf("polarity %q is unsupported", row.Polarity)
	}
	return nil
}

func allowedEntityKind(kind string) bool {
	for _, candidate := range domain.EntityKinds() {
		if kind == candidate {
			return true
		}
	}
	return false
}

func allowedExtraLedgerRows(sourceManifest *SeedManifest, sourceHash, cohortLockPath string) (map[string]struct{}, error) {
	if strings.TrimSpace(cohortLockPath) == "" {
		return map[string]struct{}{}, nil
	}
	lock, _, err := loadCohortFilterLock(cohortLockPath)
	if err != nil {
		return nil, fmt.Errorf("read cohort lock: %w", err)
	}
	if sourceManifest == nil || lock.SeedID != sourceManifest.SeedID || lock.FilteredSeedHash != sourceHash {
		return nil, fmt.Errorf("cohort lock does not bind the source seed")
	}
	allowed, err := cohortIDSet(lock.RemovedSourceDocIDs, "removed_source_doc_ids")
	if err != nil {
		return nil, err
	}
	return allowed, nil
}

func flatRelationshipFromLedger(content string, row relationshipLedgerRow) (map[string]any, error) {
	runes := []rune(content)
	supportStart, supportEnd, err := resolveLedgerSurface(runes, row.Support, row.SupportOccurrence, 0, len(runes), "support")
	if err != nil {
		return nil, err
	}
	_, _, err = resolveLedgerSurface(runes, row.Subject, row.SubjectOccurrence, supportStart, supportEnd, "subject")
	if err != nil {
		return nil, err
	}
	_, _, err = resolveLedgerSurface(runes, row.Predicate, row.PredicateOccurrence, supportStart, supportEnd, "predicate")
	if err != nil {
		return nil, err
	}
	_, _, err = resolveLedgerSurface(runes, row.Object, row.ObjectOccurrence, supportStart, supportEnd, "object")
	if err != nil {
		return nil, err
	}
	predicateKey, err := predicateProposalKey(row.Predicate)
	if err != nil {
		return nil, err
	}
	subjectKind := row.SubjectKind
	if subjectKind == "" {
		subjectKind = "other"
	}
	objectKind := row.ObjectKind
	if objectKind == "" {
		objectKind = "other"
	}
	polarity := row.Polarity
	if polarity == "" {
		polarity = "+"
	}
	return map[string]any{
		"ref": "relationship_1",
		"subject": map[string]any{
			"name": row.Subject, "entity_kind": subjectKind,
		},
		"predicate": map[string]any{
			"proposed_key": predicateKey,
		},
		"object": map[string]any{"entity": map[string]any{
			"name": row.Object, "entity_kind": objectKind,
		}},
		"polarity":         polarity,
		"evidence_indices": []any{0},
	}, nil
}

func flatRelationshipFromLedgerOrFallback(content string, row relationshipLedgerRow) (map[string]any, bool, error) {
	relationship, err := flatRelationshipFromLedger(content, row)
	if err == nil {
		return relationship, false, nil
	}
	fallback, fallbackErr := flatRelationshipFallback(content, row)
	if fallbackErr != nil {
		return nil, false, fmt.Errorf("ledger selection is not span-grounded (%v); fallback failed: %w", err, fallbackErr)
	}
	return fallback, true, nil
}

func flatRelationshipFallback(content string, row relationshipLedgerRow) (map[string]any, error) {
	runes := []rune(content)
	predicateStart, predicateEnd, err := fallbackPredicateSpan(runes, row.Predicate, row.PredicateOccurrence)
	if err != nil {
		return nil, err
	}
	supportStart, supportEnd := sentenceSupportSpan(runes, predicateStart, predicateEnd)
	subjectStart, subjectEnd, ok := nearestFallbackEntity(runes, supportStart, predicateStart, true)
	if !ok {
		return nil, fmt.Errorf("no substantive subject token before predicate")
	}
	objectStart, objectEnd, ok := nearestFallbackEntity(runes, predicateEnd, supportEnd, false)
	if !ok {
		return nil, fmt.Errorf("no substantive object token after predicate")
	}
	predicateSurface := string(runes[predicateStart:predicateEnd])
	predicateKey, err := predicateProposalKey(predicateSurface)
	if err != nil {
		return nil, err
	}
	polarity := row.Polarity
	if polarity == "" {
		polarity = "+"
	}
	return map[string]any{
		"ref": "relationship_1",
		"subject": map[string]any{
			"name": string(runes[subjectStart:subjectEnd]), "entity_kind": "other",
		},
		"predicate": map[string]any{
			"proposed_key": predicateKey,
		},
		"object": map[string]any{"entity": map[string]any{
			"name": string(runes[objectStart:objectEnd]), "entity_kind": "other",
		}},
		"polarity":         polarity,
		"evidence_indices": []any{0},
	}, nil
}

func fallbackPredicateSpan(content []rune, surface string, preferredOccurrence *int) (int, int, error) {
	needle := []rune(surface)
	if len(needle) == 0 {
		return 0, 0, fmt.Errorf("predicate surface is empty")
	}
	positions := matchingRunePositions(content, needle, 0, len(content))
	if len(positions) == 0 {
		return 0, 0, fmt.Errorf("predicate surface does not occur in evidence")
	}
	if preferredOccurrence != nil && *preferredOccurrence < len(positions) {
		start := positions[*preferredOccurrence]
		return start, start + len(needle), nil
	}
	for _, start := range positions {
		supportStart, supportEnd := sentenceSupportSpan(content, start, start+len(needle))
		if _, _, subjectOK := nearestFallbackEntity(content, supportStart, start, true); !subjectOK {
			continue
		}
		if _, _, objectOK := nearestFallbackEntity(content, start+len(needle), supportEnd, false); objectOK {
			return start, start + len(needle), nil
		}
	}
	return 0, 0, fmt.Errorf("predicate surface has no sentence with substantive endpoints")
}

func sentenceSupportSpan(content []rune, predicateStart, predicateEnd int) (int, int) {
	start := predicateStart
	for start > 0 && !sentenceDelimiter(content[start-1]) {
		start--
	}
	end := predicateEnd
	for end < len(content) && !sentenceDelimiter(content[end]) {
		end++
	}
	if end < len(content) && content[end] != '\n' {
		end++
	}
	for start < end && unicode.IsSpace(content[start]) {
		start++
	}
	for end > start && unicode.IsSpace(content[end-1]) {
		end--
	}
	return start, end
}

func sentenceDelimiter(value rune) bool {
	return value == '.' || value == '!' || value == '?' || value == '\n'
}

func nearestFallbackEntity(content []rune, start, end int, before bool) (int, int, bool) {
	tokens := fallbackTokens(content, start, end)
	if before {
		for index := len(tokens) - 1; index >= 0; index-- {
			if !fallbackEntityStopword(string(content[tokens[index][0]:tokens[index][1]])) {
				return tokens[index][0], tokens[index][1], true
			}
		}
		return 0, 0, false
	}
	for _, token := range tokens {
		if !fallbackEntityStopword(string(content[token[0]:token[1]])) {
			return token[0], token[1], true
		}
	}
	return 0, 0, false
}

func fallbackTokens(content []rune, start, end int) [][2]int {
	tokens := make([][2]int, 0, 8)
	for start < end {
		for start < end && !unicode.IsLetter(content[start]) && !unicode.IsNumber(content[start]) {
			start++
		}
		tokenStart := start
		for start < end && (unicode.IsLetter(content[start]) || unicode.IsNumber(content[start])) {
			start++
		}
		if tokenStart < start {
			tokens = append(tokens, [2]int{tokenStart, start})
		}
	}
	return tokens
}

func fallbackEntityStopword(value string) bool {
	_, ok := map[string]struct{}{
		"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "been": {}, "being": {}, "by": {},
		"can": {}, "could": {}, "did": {}, "do": {}, "does": {}, "for": {}, "from": {}, "had": {}, "has": {}, "have": {},
		"he": {}, "i": {}, "in": {}, "into": {}, "is": {}, "it": {}, "may": {}, "might": {}, "no": {}, "not": {},
		"of": {}, "on": {}, "or": {}, "she": {}, "should": {}, "that": {}, "the": {}, "these": {}, "they": {}, "this": {},
		"those": {}, "to": {}, "was": {}, "we": {}, "were": {}, "will": {}, "with": {}, "would": {}, "you": {},
	}[strings.ToLower(value)]
	return ok
}

func resolveLedgerSurface(content []rune, surface string, occurrence *int, scopeStart, scopeEnd int, name string) (int, int, error) {
	needle := []rune(surface)
	if len(needle) == 0 {
		return 0, 0, fmt.Errorf("%s surface is empty", name)
	}
	if scopeStart < 0 || scopeEnd > len(content) || scopeStart >= scopeEnd {
		return 0, 0, fmt.Errorf("%s declared scope is invalid", name)
	}
	positions := matchingRunePositions(content, needle, scopeStart, scopeEnd)
	if len(positions) == 0 {
		return 0, 0, fmt.Errorf("%s surface does not occur in its declared support", name)
	}
	selected := 0
	if occurrence == nil {
		if len(positions) != 1 {
			return 0, 0, fmt.Errorf("%s surface occurs %d times; declare an explicit occurrence", name, len(positions))
		}
	} else {
		if *occurrence >= len(positions) {
			return 0, 0, fmt.Errorf("%s_occurrence %d is outside %d matching surfaces", name, *occurrence, len(positions))
		}
		selected = *occurrence
	}
	start := positions[selected]
	return start, start + len(needle), nil
}

func matchingRunePositions(content, needle []rune, start, end int) []int {
	positions := make([]int, 0, 1)
	for index := start; index+len(needle) <= end; {
		if sameRunes(content[index:index+len(needle)], needle) {
			positions = append(positions, index)
			index += len(needle)
			continue
		}
		index++
	}
	return positions
}

func sameRunes(left, right []rune) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func predicateProposalKey(surface string) (string, error) {
	var builder strings.Builder
	separator := true
	for _, value := range strings.ToLower(surface) {
		if unicode.IsLetter(value) || unicode.IsNumber(value) {
			builder.WriteRune(value)
			separator = false
			continue
		}
		if !separator {
			builder.WriteByte('_')
			separator = true
		}
	}
	key := strings.Trim(builder.String(), "_")
	if key == "" {
		return "related_to", nil
	}
	if len([]rune(key)) > 128 {
		return "", fmt.Errorf("predicate proposed_key exceeds 128 characters")
	}
	return key, nil
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
		manifest.TransformsFile, manifest.DreamsFile, manifest.LicensesFile,
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
