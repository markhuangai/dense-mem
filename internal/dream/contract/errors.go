package contract

import (
	"errors"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

var (
	ErrDreamCycleAlreadyClaimed     = errors.New("dream cycle already claimed")
	ErrDreamHypothesisNotFound      = errors.New("dream hypothesis not found")
	ErrDreamDiagnosticNotFound      = errors.New("dream diagnostic not found")
	ErrInvalidDreamDiagnosticCursor = errors.New("invalid dream diagnostic cursor")
	ErrDreamHypothesisIDInvalid     = errors.New("dream hypothesis ID invalid")
	ErrDreamSourceStale             = errors.New("dream source is stale")
	ErrDreamExactRelationshipExists = errors.New("dream exact relationship already exists")
	ErrDreamExactHypothesisExists   = errors.New("dream exact hypothesis already exists")
	ErrDreamCycleLeaseLost          = errors.New("dream cycle lease is no longer current")
	ErrDreamConfirmationBusy        = errors.New("dream confirmation is already in progress")
	ErrTeamInactive                 = knowledgecontract.ErrTeamInactive
)
