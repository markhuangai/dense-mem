package postgres

import (
	"context"

	"gorm.io/gorm"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
)

type TraceRelationshipInput = tracecontract.Input
type RelationshipTraceResult = tracecontract.RelationshipTraceResult
type RelationshipTraceRecord = tracecontract.RelationshipTraceRecord
type RelationshipObservationRecord = tracecontract.RelationshipObservationRecord
type RelationshipVerificationEvent = tracecontract.RelationshipVerificationEvent
type RelationshipEvidenceSupportRecord = tracecontract.RelationshipEvidenceSupportRecord
type RelationshipSupportDecisionEvent = tracecontract.RelationshipSupportDecisionEvent
type TraceEvidenceFragment = tracecontract.TraceEvidenceFragment
type TraceEvidenceLifecycleEvent = tracecontract.TraceEvidenceLifecycleEvent
type RelationshipTransitionEvent = tracecontract.RelationshipTransitionEvent
type RelationshipCrossReferenceRecord = tracecontract.RelationshipCrossReferenceRecord
type EntityCorrectionEventRecord = tracecontract.EntityCorrectionEventRecord
type TraceSearchDocument = tracecontract.TraceSearchDocument
type RelationshipConflictCaseRecord = tracecontract.RelationshipConflictCaseRecord
type SemanticGraphNode = graphcontract.Node
type SemanticGraphEdge = graphcontract.Edge

// GraphLoader reads the graph snapshot on the trace transaction after the
// trace adapter has derived the relationship's memory space.
type GraphLoader func(context.Context, *gorm.DB, graphcontract.Query, string) (*graphcontract.Snapshot, error)

// ConflictLoader reads conflict records on the trace transaction and in the
// relationship's derived memory space.
type ConflictLoader func(context.Context, *gorm.DB, string, string, string) ([]tracecontract.RelationshipConflictCaseRecord, error)

// Store owns PostgreSQL trace hydration. Graph traversal and conflict reads
// remain injected adapters so their existing implementations and transaction
// boundaries are reused without importing the legacy repository.
type Store struct {
	db        *gorm.DB
	rls       storagepostgres.RLSHelper
	graph     GraphLoader
	conflicts ConflictLoader
}
