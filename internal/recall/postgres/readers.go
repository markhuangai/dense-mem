package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
)

// RelationshipConflictReader loads the conflict projection associated with the
// evidence returned by one recall transaction. The transaction is supplied by
// the owning adapter so RLS and temporal fences remain unchanged.
type RelationshipConflictReader func(
	context.Context,
	*gorm.DB,
	string,
	*time.Time,
	[]recallcontract.RecallEvidenceHit,
) ([]tracecontract.RelationshipConflictCaseRecord, error)

// EvidenceConflictReader loads cited-evidence conflict projections associated
// with one recall result on the same transaction and visibility fence.
type EvidenceConflictReader func(
	context.Context,
	*gorm.DB,
	recallcontract.RecallEvidenceInput,
	[]recallcontract.RecallEvidenceHit,
) ([]recallcontract.EvidenceConflictCaseRecord, error)
