package postgres

import (
	"errors"
	"strconv"

	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) organizationInputsCurrent(tx *gorm.DB, fence scope, receipt topic.OrganizationReceipt) (bool, error) {
	rejection, err := s.organizationInputRejection(tx, fence, receipt)
	return rejection == nil && err == nil, err
}

func (s *Store) organizationInputRejection(tx *gorm.DB, fence scope, receipt topic.OrganizationReceipt) (*topic.FreshnessRejection, error) {
	handles := make([]topic.SourceHandle, 0, len(receipt.Sources))
	for _, dependency := range receipt.Sources {
		handles = append(handles, dependency.SourceHandle)
	}
	sources, err := organizationSources(tx, fence, handles)
	if err != nil {
		return nil, err
	}
	for i, dependency := range receipt.Sources {
		fingerprint, err := topic.SourceFingerprint(sources[i])
		if err != nil {
			return nil, err
		}
		if fingerprint != dependency.Fingerprint {
			handle := dependency.SourceHandle
			return &topic.FreshnessRejection{Check: "source_fingerprint", Source: &handle,
				Expected: dependency.Fingerprint, Observed: fingerprint}, nil
		}
	}
	ids := []string{}
	for _, dependency := range receipt.Dependencies {
		ids = append(ids, dependency.ID)
	}
	catalog, err := loadHeads(tx, fence, ids, nil)
	if errors.Is(err, topic.ErrContextBound) {
		return &topic.FreshnessRejection{Check: "dependency_catalog_bound", Expected: "within_dependency_context_bound",
			Observed: "context_bound_exceeded", Dependencies: receipt.Dependencies}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []topic.Record
	for _, dependency := range receipt.Dependencies {
		record, ok := catalog[dependency.ID]
		if !ok {
			return &topic.FreshnessRejection{Check: "dependency_missing", RecordID: dependency.ID,
				Expected: strconv.FormatInt(dependency.Version, 10), Observed: "missing"}, nil
		}
		if record.Version != dependency.Version {
			return &topic.FreshnessRejection{Check: "dependency_version", RecordID: dependency.ID,
				Expected: strconv.FormatInt(dependency.Version, 10), Observed: strconv.FormatInt(record.Version, 10)}, nil
		}
		if record.Retired {
			return &topic.FreshnessRejection{Check: "dependency_retired", RecordID: dependency.ID,
				Expected: "active", Observed: "retired"}, nil
		}
		records = append(records, record)
	}
	for start := 0; start < len(records); start += topic.MaxOrganizationSources {
		end := min(start+topic.MaxOrganizationSources, len(records))
		views, err := s.organizationViews(tx, fence, records[start:end])
		if errors.Is(err, topic.ErrContextBound) {
			return &topic.FreshnessRejection{Check: "dependency_context_bound", Expected: "within_dependency_context_bound",
				Observed: "context_bound_exceeded", Dependencies: receipt.Dependencies[start:end]}, nil
		}
		if err != nil {
			return nil, err
		}
		for _, view := range views {
			if !view.Current {
				return &topic.FreshnessRejection{Check: "dependency_not_current", RecordID: view.ID,
					Expected: "current", Observed: view.StaleReason}, nil
			}
		}
	}
	contextData, err := s.assessmentContext(tx, fence, handles, receipt.EquivalenceOnly)
	if errors.Is(err, topic.ErrContextBound) {
		return &topic.FreshnessRejection{Check: "context_bound", Expected: "within_organization_context_bound",
			Observed: "context_bound_exceeded"}, nil
	}
	if err != nil {
		return nil, err
	}
	hash, err := topic.OrganizationContextHash(contextData)
	if err != nil {
		return nil, err
	}
	if hash != receipt.ContextHash {
		observed := topic.SnapshotOrganizationContext(contextData)
		return &topic.FreshnessRejection{Check: "context_hash", Expected: receipt.ContextHash, Observed: hash,
			Dependencies: receipt.Dependencies, ExpectedContext: receipt.ContextState, ObservedContext: &observed}, nil
	}
	return nil, nil
}
