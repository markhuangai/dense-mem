package repository

import "fmt"

// EvidenceDiscoveryDuplicateError identifies the proposal that made an
// otherwise complete evidence-discovery response unpersistable.
type EvidenceDiscoveryDuplicateError struct {
	Index int
	Err   error
}

func (e *EvidenceDiscoveryDuplicateError) Error() string {
	if e == nil {
		return "evidence discovery proposal is duplicated"
	}
	return fmt.Sprintf("proposals[%d]: %v", e.Index, e.Err)
}

func (e *EvidenceDiscoveryDuplicateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
