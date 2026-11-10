package postgres

import (
	"context"

	"gorm.io/gorm"

	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
)

// EvidenceConflictReader loads cited-evidence conflict projections associated
// with one recall result on the same transaction and visibility fence.
type EvidenceConflictReader func(
	context.Context,
	*gorm.DB,
	recallcontract.RecallEvidenceInput,
	[]recallcontract.RecallEvidenceHit,
) ([]recallcontract.EvidenceConflictCaseRecord, error)
