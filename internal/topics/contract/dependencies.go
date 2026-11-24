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
			reason := "dependency_cycle"
			if record.Definition != nil {
				reason = "hierarchy_cycle"
			}
			return &PublicationRejection{Cause: fmt.Errorf("%w: dependency cycle", ErrInvalid), Reason: reason, RecordID: record.ID}
		}
		if state[record.ID] == 2 {
			return nil
		}
		if depth > MaxParentDepth || len(state) >= MaxDependencyRecords {
			reason := "dependency_bound"
			if depth > MaxParentDepth && record.Definition != nil {
				reason = "hierarchy_depth"
			}
			return &PublicationRejection{Cause: fmt.Errorf("%w: dependency traversal bound", ErrContextBound), Reason: reason, RecordID: record.ID}
		}
		state[record.ID] = 1
		if !record.Retired {
			for _, ref := range dependenciesFor(record, catalog) {
				dependency, exists := catalog[ref.ID]
				if !exists || dependency.Retired {
					if record.Definition != nil && record.Definition.ParentID == ref.ID {
						return &PublicationRejection{Cause: ErrDependencyStale, Reason: "parent_unavailable", RecordID: record.ID, ParentID: ref.ID}
					}
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
	return checkDependencies(records, catalog, func(record Record) (string, error) {
		return RecordFingerprint(record, snapshots, catalog)
	})
}

func checkDependencies(records []Record, catalog map[string]Record, fingerprintRecord func(Record) (string, error)) error {
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
		fingerprint, err := fingerprintRecord(record)
		if err != nil {
			return err
		}
		if fingerprint != record.Fingerprint {
			return ErrSourceStale
		}
	}
	return nil
}
