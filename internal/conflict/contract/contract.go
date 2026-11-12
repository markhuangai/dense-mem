package contract

import (
	"context"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

type (
	EvidenceConflictPositionRecord  = knowledgecontract.EvidenceConflictPositionRecord
	EvidenceConflictEventRecord     = knowledgecontract.EvidenceConflictEventRecord
	EvidenceConflictCaseRecord      = knowledgecontract.EvidenceConflictCaseRecord
	EvidenceConflictListInput       = knowledgecontract.EvidenceConflictListInput
	EvidenceConflictListResult      = knowledgecontract.EvidenceConflictListResult
	EvidenceConflictCursor          = knowledgecontract.EvidenceConflictCursor
	EvidenceConflictGetInput        = knowledgecontract.EvidenceConflictGetInput
	EvidenceConflictEventCursor     = knowledgecontract.EvidenceConflictEventCursor
	EvidenceConflictGetResult       = knowledgecontract.EvidenceConflictGetResult
	EvidenceConflictResolutionInput = knowledgecontract.EvidenceConflictResolutionInput
)

const (
	EvidenceConflictDefaultLimit      = knowledgecontract.EvidenceConflictDefaultLimit
	EvidenceConflictMaxLimit          = knowledgecontract.EvidenceConflictMaxLimit
	EvidenceConflictDefaultEventLimit = knowledgecontract.EvidenceConflictDefaultEventLimit
	EvidenceConflictMaxEventLimit     = knowledgecontract.EvidenceConflictMaxEventLimit
	EvidenceConflictMaxResults        = knowledgecontract.EvidenceConflictMaxResults
	EvidenceConflictMaxPositions      = knowledgecontract.EvidenceConflictMaxPositions
	EvidenceConflictMaxQuoteRunes     = knowledgecontract.EvidenceConflictMaxQuoteRunes
)

var (
	ErrEvidenceConflictNotFound       = knowledgecontract.ErrEvidenceConflictNotFound
	ErrEvidenceConflictVersionStale   = knowledgecontract.ErrEvidenceConflictVersionStale
	ErrEvidenceConflictNotOpen        = knowledgecontract.ErrEvidenceConflictNotOpen
	ErrEvidenceConflictInvalidCommand = knowledgecontract.ErrEvidenceConflictInvalidCommand
	ErrEvidenceConflictStaleInput     = knowledgecontract.ErrEvidenceConflictStaleInput
)

type EvidenceConflictRepository interface {
	ListEvidenceConflicts(context.Context, EvidenceConflictListInput) (*EvidenceConflictListResult, error)
	GetEvidenceConflict(context.Context, EvidenceConflictGetInput) (*EvidenceConflictGetResult, error)
	ResolveEvidenceConflict(context.Context, EvidenceConflictResolutionInput) (*EvidenceConflictCaseRecord, error)
}

func EncodeEvidenceConflictCursor(cursor EvidenceConflictCursor) (string, error) {
	return knowledgecontract.EncodeEvidenceConflictCursor(cursor)
}

func DecodeEvidenceConflictCursor(raw string) (*EvidenceConflictCursor, error) {
	return knowledgecontract.DecodeEvidenceConflictCursor(raw)
}

func EncodeEvidenceConflictEventCursor(cursor EvidenceConflictEventCursor) (string, error) {
	return knowledgecontract.EncodeEvidenceConflictEventCursor(cursor)
}

func DecodeEvidenceConflictEventCursor(raw string) (*EvidenceConflictEventCursor, error) {
	return knowledgecontract.DecodeEvidenceConflictEventCursor(raw)
}
