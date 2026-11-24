package contract

import (
	"sort"
)

type OrganizationContextRevision struct {
	ID      string
	Version int64
	Current bool
}

type OrganizationContextState struct {
	Records    []OrganizationContextRevision
	Candidates []OrganizationContextRevision
}

func SnapshotOrganizationContext(context OrganizationContext) OrganizationContextState {
	refs := func(views []RecordView) []OrganizationContextRevision {
		result := make([]OrganizationContextRevision, 0, len(views))
		for _, view := range views {
			result = append(result, OrganizationContextRevision{view.ID, view.Version, view.Current})
		}
		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
		return result
	}
	return OrganizationContextState{Records: refs(context.Records), Candidates: refs(context.Candidates)}
}

type FreshnessRejection struct {
	Check           string                    `json:"check"`
	Source          *SourceHandle             `json:"source,omitempty"`
	RecordID        string                    `json:"record_id,omitempty"`
	Expected        string                    `json:"expected"`
	Observed        string                    `json:"observed"`
	Dependencies    []RevisionRef             `json:"dependencies,omitempty"`
	ExpectedContext *OrganizationContextState `json:"expected_context,omitempty"`
	ObservedContext *OrganizationContextState `json:"observed_context,omitempty"`
}

func (d *FreshnessRejection) Valid() bool {
	if d == nil {
		return true
	}
	if !boundedText(d.Expected, 128) || !boundedText(d.Observed, 128) || len(d.Dependencies) > MaxDependencyRecords {
		return false
	}
	if d.Source != nil && ValidateSourceHandle(*d.Source) != nil || d.RecordID != "" && !validID(d.RecordID) {
		return false
	}
	for _, dependency := range d.Dependencies {
		if !validID(dependency.ID) || dependency.Version < 1 {
			return false
		}
	}
	for _, state := range []*OrganizationContextState{d.ExpectedContext, d.ObservedContext} {
		if state == nil {
			continue
		}
		if len(state.Records) > MaxDependencyRecords || len(state.Candidates) > MaxVocabularyCandidates {
			return false
		}
		for _, refs := range [][]OrganizationContextRevision{state.Records, state.Candidates} {
			for _, ref := range refs {
				if !validID(ref.ID) || ref.Version < 1 {
					return false
				}
			}
		}
	}
	switch d.Check {
	case "source_fingerprint":
		return d.Source != nil && validFingerprint(d.Expected) && validFingerprint(d.Observed)
	case "dependency_missing", "dependency_version", "dependency_retired", "dependency_not_current":
		return d.RecordID != ""
	case "dependency_catalog_bound", "dependency_context_bound", "context_bound", "context_hash":
		return true
	}
	return false
}

type FreshnessError struct {
	Rejection FreshnessRejection
}

func (e *FreshnessError) Error() string { return ErrSourceStale.Error() }
func (e *FreshnessError) Unwrap() error { return ErrSourceStale }
