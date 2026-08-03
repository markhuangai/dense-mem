package evalharness

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	cohortFilterLockSchema   = "dense-mem.eval.cohort_filter.v1"
	v2CohortValidationSchema = "dense-mem.eval.v2_cohort_validation.v1"
)

type cohortFilterLock struct {
	SchemaVersion       string         `json:"schema_version"`
	SeedID              string         `json:"seed_id"`
	ParentSeedHash      string         `json:"parent_seed_hash"`
	FilteredSeedHash    string         `json:"filtered_seed_hash"`
	RemovedSourceDocIDs []string       `json:"removed_source_doc_ids"`
	DroppedCaseIDs      []string       `json:"dropped_case_ids"`
	ExpectedCounts      map[string]int `json:"expected_counts"`
	Invariant           string         `json:"invariant"`
}

type cohortJSONLRow struct {
	Key string
	Raw []byte
}

// ValidateV2CohortDerivation proves that the filtered V1 seed contains only
// the cohort-lock's declared omissions and that V2 preserves every retained
// V1 evidence item while adding relationship properties.
func ValidateV2CohortDerivation(opts V2CohortValidationOptions) (V2CohortValidationReport, error) {
	if err := validateV2CohortValidationOptions(opts); err != nil {
		return V2CohortValidationReport{}, err
	}
	lock, lockHash, err := loadCohortFilterLock(opts.CohortLockPath)
	if err != nil {
		return V2CohortValidationReport{}, err
	}
	parentManifest, err := LoadSeedManifest(opts.ParentManifestPath)
	if err != nil {
		return V2CohortValidationReport{}, err
	}
	filteredManifest, err := LoadSeedManifest(opts.FilteredManifestPath)
	if err != nil {
		return V2CohortValidationReport{}, err
	}
	if parentManifest.SchemaVersion != SeedSchemaVersion || filteredManifest.SchemaVersion != SeedSchemaVersion {
		return V2CohortValidationReport{}, fmt.Errorf("parent and filtered cohort seeds must use %q", SeedSchemaVersion)
	}
	parentHash, err := SeedHash(opts.ParentManifestPath, parentManifest)
	if err != nil {
		return V2CohortValidationReport{}, fmt.Errorf("hash parent V1 seed: %w", err)
	}
	filteredHash, err := SeedHash(opts.FilteredManifestPath, filteredManifest)
	if err != nil {
		return V2CohortValidationReport{}, fmt.Errorf("hash filtered V1 seed: %w", err)
	}
	if err := validateCohortFilterLockBinding(lock, parentManifest, filteredManifest, parentHash, filteredHash); err != nil {
		return V2CohortValidationReport{}, err
	}
	if err := validateV1CohortFiles(opts, parentManifest, filteredManifest, lock); err != nil {
		return V2CohortValidationReport{}, err
	}

	derivation, err := ValidateV2Derivation(
		opts.FilteredManifestPath,
		opts.FilteredSuitePath,
		opts.DerivedManifestPath,
		opts.DerivedSuitePath,
	)
	if err != nil {
		return V2CohortValidationReport{}, err
	}
	return V2CohortValidationReport{
		SchemaVersion:          v2CohortValidationSchema,
		Status:                 "passed",
		CohortLockHash:         lockHash,
		ParentSeedHash:         parentHash,
		FilteredSeedHash:       filteredHash,
		DerivedSeedHash:        derivation.DerivedSeedHash,
		RemovedSourceDocIDs:    append([]string(nil), lock.RemovedSourceDocIDs...),
		DroppedCaseIDs:         append([]string(nil), lock.DroppedCaseIDs...),
		RetainedCorpusCount:    derivation.CorpusCount,
		RetainedCaseCount:      filteredManifest.Counts["cases"],
		RelationshipCount:      derivation.RelationshipCount,
		ContractValidatedCount: derivation.ContractValidatedCount,
	}, nil
}

func validateV2CohortValidationOptions(opts V2CohortValidationOptions) error {
	for name, path := range map[string]string{
		"parent V1 manifest":   opts.ParentManifestPath,
		"parent V1 suite":      opts.ParentSuitePath,
		"filtered V1 manifest": opts.FilteredManifestPath,
		"filtered V1 suite":    opts.FilteredSuitePath,
		"derived V2 manifest":  opts.DerivedManifestPath,
		"derived V2 suite":     opts.DerivedSuitePath,
		"cohort lock":          opts.CohortLockPath,
	} {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("%s path is required", name)
		}
	}
	return nil
}

func loadCohortFilterLock(path string) (cohortFilterLock, string, error) {
	var raw map[string]json.RawMessage
	if err := readJSONFile(path, &raw); err != nil {
		return cohortFilterLock{}, "", err
	}
	allowed := map[string]struct{}{
		"schema_version": {}, "seed_id": {}, "parent_seed_hash": {}, "filtered_seed_hash": {},
		"removed_source_doc_ids": {}, "dropped_case_ids": {}, "expected_counts": {}, "invariant": {},
	}
	for field := range raw {
		if _, ok := allowed[field]; !ok {
			return cohortFilterLock{}, "", fmt.Errorf("cohort lock has unsupported field %q", field)
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return cohortFilterLock{}, "", err
	}
	var lock cohortFilterLock
	if err := json.Unmarshal(encoded, &lock); err != nil {
		return cohortFilterLock{}, "", err
	}
	lock.SchemaVersion = strings.TrimSpace(lock.SchemaVersion)
	lock.SeedID = strings.TrimSpace(lock.SeedID)
	lock.ParentSeedHash = strings.TrimSpace(lock.ParentSeedHash)
	lock.FilteredSeedHash = strings.TrimSpace(lock.FilteredSeedHash)
	if lock.SchemaVersion != cohortFilterLockSchema {
		return cohortFilterLock{}, "", fmt.Errorf("cohort lock schema_version %q is not %q", lock.SchemaVersion, cohortFilterLockSchema)
	}
	if lock.SeedID == "" || lock.ParentSeedHash == "" || lock.FilteredSeedHash == "" {
		return cohortFilterLock{}, "", fmt.Errorf("cohort lock must set seed_id, parent_seed_hash, and filtered_seed_hash")
	}
	if _, err := cohortIDSet(lock.RemovedSourceDocIDs, "removed_source_doc_ids"); err != nil {
		return cohortFilterLock{}, "", err
	}
	if _, err := cohortIDSet(lock.DroppedCaseIDs, "dropped_case_ids"); err != nil {
		return cohortFilterLock{}, "", err
	}
	for _, name := range []string{"corpus", "cases", "qrels", "answers", "transforms"} {
		if lock.ExpectedCounts[name] < 0 {
			return cohortFilterLock{}, "", fmt.Errorf("cohort lock expected_counts.%s must be non-negative", name)
		}
		if _, ok := lock.ExpectedCounts[name]; !ok {
			return cohortFilterLock{}, "", fmt.Errorf("cohort lock expected_counts.%s is required", name)
		}
	}
	hash, err := sha256File(path)
	if err != nil {
		return cohortFilterLock{}, "", err
	}
	return lock, hash, nil
}

func validateCohortFilterLockBinding(
	lock cohortFilterLock,
	parent, filtered *SeedManifest,
	parentHash, filteredHash string,
) error {
	if parent.SeedID != lock.SeedID || filtered.SeedID != lock.SeedID {
		return fmt.Errorf("cohort lock seed_id %q does not match parent/filtered seed IDs %q/%q", lock.SeedID, parent.SeedID, filtered.SeedID)
	}
	if lock.ParentSeedHash != parentHash {
		return fmt.Errorf("cohort lock parent_seed_hash %q does not match parent V1 seed hash %q", lock.ParentSeedHash, parentHash)
	}
	if lock.FilteredSeedHash != filteredHash {
		return fmt.Errorf("cohort lock filtered_seed_hash %q does not match filtered V1 seed hash %q", lock.FilteredSeedHash, filteredHash)
	}
	return nil
}

func validateV1CohortFiles(
	opts V2CohortValidationOptions,
	parent, filtered *SeedManifest,
	lock cohortFilterLock,
) error {
	if err := validateCohortManifestDeclarations(parent, filtered); err != nil {
		return err
	}
	removedSourceDocIDs, err := cohortIDSet(lock.RemovedSourceDocIDs, "removed_source_doc_ids")
	if err != nil {
		return err
	}
	droppedCaseIDs, err := cohortIDSet(lock.DroppedCaseIDs, "dropped_case_ids")
	if err != nil {
		return err
	}
	if err := validateFilteredSeedJSONL(opts.ParentManifestPath, parent.CorpusFile, opts.FilteredManifestPath, filtered.CorpusFile, "source_doc_id", removedSourceDocIDs, "corpus"); err != nil {
		return err
	}
	for name, files := range map[string][2]string{
		"cases":      {parent.CasesFile, filtered.CasesFile},
		"qrels":      {parent.QrelsFile, filtered.QrelsFile},
		"answers":    {parent.AnswersFile, filtered.AnswersFile},
		"transforms": {parent.TransformsFile, filtered.TransformsFile},
	} {
		parentFile, filteredFile := files[0], files[1]
		if strings.TrimSpace(parentFile) == "" {
			if strings.TrimSpace(filteredFile) != "" {
				return fmt.Errorf("filtered manifest unexpectedly declares %s file", name)
			}
			continue
		}
		if err := validateFilteredSeedJSONL(opts.ParentManifestPath, parentFile, opts.FilteredManifestPath, filteredFile, "case_id", droppedCaseIDs, name); err != nil {
			return err
		}
	}
	if err := validateFilteredJSONL(opts.ParentSuitePath, opts.FilteredSuitePath, "case_id", droppedCaseIDs, "suite"); err != nil {
		return err
	}
	for name, files := range map[string][2]string{
		"hard_negatives": {parent.HardNegativesFile, filtered.HardNegativesFile},
		"dreams":         {parent.DreamsFile, filtered.DreamsFile},
		"licenses":       {parent.LicensesFile, filtered.LicensesFile},
	} {
		parentFile, filteredFile := files[0], files[1]
		if strings.TrimSpace(parentFile) == "" {
			continue
		}
		parentPath, err := safeSeedFilePath(opts.ParentManifestPath, parentFile)
		if err != nil {
			return err
		}
		filteredPath, err := safeSeedFilePath(opts.FilteredManifestPath, filteredFile)
		if err != nil {
			return err
		}
		if _, err := validateCopiedFile(parentPath, filteredPath, name); err != nil {
			return err
		}
	}
	return validateCohortCounts(opts, parent, filtered, lock)
}

func validateCohortManifestDeclarations(parent, filtered *SeedManifest) error {
	for name, files := range map[string][2]string{
		"corpus":         {parent.CorpusFile, filtered.CorpusFile},
		"cases":          {parent.CasesFile, filtered.CasesFile},
		"qrels":          {parent.QrelsFile, filtered.QrelsFile},
		"answers":        {parent.AnswersFile, filtered.AnswersFile},
		"hard_negatives": {parent.HardNegativesFile, filtered.HardNegativesFile},
		"transforms":     {parent.TransformsFile, filtered.TransformsFile},
		"dreams":         {parent.DreamsFile, filtered.DreamsFile},
		"licenses":       {parent.LicensesFile, filtered.LicensesFile},
	} {
		parentFile, filteredFile := files[0], files[1]
		if strings.TrimSpace(parentFile) != strings.TrimSpace(filteredFile) {
			return fmt.Errorf("filtered manifest %s file declaration differs from parent", name)
		}
	}
	return nil
}

func validateFilteredSeedJSONL(
	parentManifestPath, parentFile, filteredManifestPath, filteredFile, keyField string,
	declaredRemoved map[string]struct{},
	name string,
) error {
	parentPath, err := safeSeedFilePath(parentManifestPath, parentFile)
	if err != nil {
		return err
	}
	filteredPath, err := safeSeedFilePath(filteredManifestPath, filteredFile)
	if err != nil {
		return err
	}
	return validateFilteredJSONL(parentPath, filteredPath, keyField, declaredRemoved, name)
}

func validateFilteredJSONL(
	parentPath, filteredPath, keyField string,
	declaredRemoved map[string]struct{},
	name string,
) error {
	parentRows, err := loadCohortJSONLRows(parentPath, keyField)
	if err != nil {
		return fmt.Errorf("read parent %s: %w", name, err)
	}
	filteredRows, err := loadCohortJSONLRows(filteredPath, keyField)
	if err != nil {
		return fmt.Errorf("read filtered %s: %w", name, err)
	}
	expected := make([]cohortJSONLRow, 0, len(parentRows))
	seenDeclared := make(map[string]struct{}, len(declaredRemoved))
	for _, row := range parentRows {
		if _, remove := declaredRemoved[row.Key]; remove {
			seenDeclared[row.Key] = struct{}{}
			continue
		}
		expected = append(expected, row)
	}
	if len(seenDeclared) != len(declaredRemoved) {
		missing := make([]string, 0, len(declaredRemoved)-len(seenDeclared))
		for key := range declaredRemoved {
			if _, ok := seenDeclared[key]; !ok {
				missing = append(missing, key)
			}
		}
		sort.Strings(missing)
		return fmt.Errorf("cohort lock declares %s removal %q that is absent from parent", name, missing[0])
	}
	if len(filteredRows) != len(expected) {
		return fmt.Errorf("filtered %s has %d rows; expected %d after declared exclusions", name, len(filteredRows), len(expected))
	}
	for index := range expected {
		if filteredRows[index].Key != expected[index].Key {
			return fmt.Errorf("filtered %s row %d key %q differs from retained parent key %q", name, index+1, filteredRows[index].Key, expected[index].Key)
		}
		if !bytes.Equal(filteredRows[index].Raw, expected[index].Raw) {
			return fmt.Errorf("filtered %s row %d (%q) is not byte-identical to retained parent row", name, index+1, expected[index].Key)
		}
	}
	return nil
}

func loadCohortJSONLRows(path, keyField string) ([]cohortJSONLRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	rows := []cohortJSONLRow{}
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		rawLine := append([]byte(nil), scanner.Bytes()...)
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 || bytes.HasPrefix(line, []byte("#")) {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		rawKey, ok := fields[keyField]
		if !ok {
			return nil, fmt.Errorf("%s:%d: missing %s", path, lineNo, keyField)
		}
		var key string
		if err := json.Unmarshal(rawKey, &key); err != nil || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%s:%d: invalid %s", path, lineNo, keyField)
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%s:%d: duplicate %s %q", path, lineNo, keyField, key)
		}
		seen[key] = struct{}{}
		rows = append(rows, cohortJSONLRow{Key: key, Raw: rawLine})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func validateCohortCounts(
	opts V2CohortValidationOptions,
	parent, filtered *SeedManifest,
	lock cohortFilterLock,
) error {
	actual := map[string]int{}
	for name, file := range map[string]struct {
		manifestPath string
		file         string
		key          string
	}{
		"corpus":     {opts.FilteredManifestPath, filtered.CorpusFile, "source_doc_id"},
		"cases":      {opts.FilteredManifestPath, filtered.CasesFile, "case_id"},
		"qrels":      {opts.FilteredManifestPath, filtered.QrelsFile, "case_id"},
		"answers":    {opts.FilteredManifestPath, filtered.AnswersFile, "case_id"},
		"transforms": {opts.FilteredManifestPath, filtered.TransformsFile, "case_id"},
	} {
		if strings.TrimSpace(file.file) == "" {
			return fmt.Errorf("filtered manifest %s file is required by cohort lock", name)
		}
		path, err := safeSeedFilePath(file.manifestPath, file.file)
		if err != nil {
			return err
		}
		rows, err := loadCohortJSONLRows(path, file.key)
		if err != nil {
			return err
		}
		actual[name] = len(rows)
		if filtered.Counts[name] != len(rows) {
			return fmt.Errorf("filtered manifest count %s=%d does not match %d rows", name, filtered.Counts[name], len(rows))
		}
		if lock.ExpectedCounts[name] != len(rows) {
			return fmt.Errorf("cohort lock expected_counts.%s=%d does not match %d rows", name, lock.ExpectedCounts[name], len(rows))
		}
	}
	if parent.Counts["corpus"] <= actual["corpus"] || parent.Counts["cases"] < actual["cases"] {
		return fmt.Errorf("cohort filter did not reduce the declared parent V1 cohort")
	}
	return nil
}

func cohortIDSet(values []string, name string) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("cohort lock %s contains an empty value", name)
		}
		if _, exists := set[value]; exists {
			return nil, fmt.Errorf("cohort lock %s duplicates %q", name, value)
		}
		set[value] = struct{}{}
	}
	return set, nil
}
