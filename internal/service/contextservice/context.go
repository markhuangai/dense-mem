// Package contextservice preserves the legacy trace application API while the
// implementation is owned by the trace capability.
package contextservice

import traceapp "github.com/markhuangai/dense-mem/internal/trace"

type Service = traceapp.Service
type TraceRequest = traceapp.TraceRequest
type TraceResult = traceapp.TraceResult
type SemanticTrace = traceapp.SemanticTrace
type SemanticTraceStore = traceapp.SemanticTraceStore

var (
	ErrTraceAuthContext            = traceapp.ErrTraceAuthContext
	ErrTraceRelationshipNotFound   = traceapp.ErrTraceRelationshipNotFound
	ErrTraceRepositoryTeamMismatch = traceapp.ErrTraceRepositoryTeamMismatch
)

func NewSemantic(store SemanticTraceStore) Service {
	return traceapp.NewSemantic(store)
}
