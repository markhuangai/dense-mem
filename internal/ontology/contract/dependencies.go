package contract

import "fmt"

const MaxDependencyRecords = 2048

var ErrContextBound = fmt.Errorf("%w: ontology context exceeds bound", ErrInvalid)
var ErrDependencyStale = fmt.Errorf("%w: ontology dependency is unavailable or stale", ErrSourceStale)

func DependencyRecords(records []Record, catalog map[string]Record) ([]Record, error) {
	state := map[string]int{}
	var result []Record
	var visit func(Record, int) error
	visit = func(record Record, depth int) error {
		if state[record.ID] == 1 {
			return fmt.Errorf("%w: dependency cycle", ErrInvalid)
		}
		if state[record.ID] == 2 {
			return nil
		}
		if depth > MaxParentDepth || len(state) >= MaxDependencyRecords {
			return fmt.Errorf("%w: dependency traversal bound", ErrContextBound)
		}
		state[record.ID] = 1
		if !record.Retired {
			for _, ref := range dependenciesFor(record, catalog) {
				dependency, exists := catalog[ref.ID]
				if !exists || dependency.Retired {
					return ErrDependencyStale
				}
				if err := visit(dependency, depth+1); err != nil {
					return err
				}
			}
		}
		state[record.ID] = 2
		result = append(result, record)
		return nil
	}
	for _, record := range records {
		if err := visit(record, 0); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func CheckDependencies(records []Record, snapshots map[string]SourceSnapshot, catalog map[string]Record) error {
	dependencies, err := DependencyRecords(records, catalog)
	if err != nil {
		return err
	}
	roots := map[string]bool{}
	for _, record := range records {
		roots[record.ID] = true
	}
	for _, record := range dependencies {
		if roots[record.ID] {
			continue
		}
		fingerprint, err := RecordFingerprint(record, snapshots, catalog)
		if err != nil {
			return err
		}
		if fingerprint != record.Fingerprint {
			return ErrSourceStale
		}
	}
	return nil
}
