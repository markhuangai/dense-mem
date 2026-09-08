package contract

import (
	"errors"
	"fmt"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

var (
	ErrDreamCycleAlreadyClaimed     = errors.New("dream cycle already claimed")
	ErrDreamHypothesisNotFound      = errors.New("dream hypothesis not found")
	ErrDreamHypothesisIDInvalid     = errors.New("dream hypothesis ID invalid")
	ErrDreamSourceStale             = errors.New("dream source is stale")
	ErrDreamExactRelationshipExists = errors.New("dream exact relationship already exists")
	ErrDreamExactHypothesisExists   = errors.New("dream exact hypothesis already exists")
	ErrDreamCycleLeaseLost          = errors.New("dream cycle lease is no longer current")
	ErrDreamConfirmationBusy        = errors.New("dream confirmation is already in progress")
	ErrEvidenceDiscoveryBusy        = errors.New("evidence discovery target lock is busy")
	ErrEvidenceDiscoveryAttemptNotReserved = errors.New("evidence discovery attempt is not reserved")
	ErrTeamInactive                 = knowledgecontract.ErrTeamInactive
)

// EvidenceDiscoveryDuplicateError identifies the proposal that made an
// otherwise complete provider response unpersistable.
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
