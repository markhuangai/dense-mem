package trace

import (
	"context"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
)

type Service interface {
	Trace(context.Context, string, TraceRequest) (*TraceResult, error)
}
type TraceRequest struct {
	EvidenceID              string `json:"evidence_id"`
	IncludeEvidenceContent  *bool  `json:"include_evidence_content,omitempty"`
	IncludeTransitions      *bool  `json:"include_transitions,omitempty"`
	MaxEvents               int    `json:"max_events,omitempty"`
	MaxFragmentContentRunes int    `json:"max_fragment_content_runes,omitempty"`
}
type TraceResult struct {
	Semantic *tracecontract.EvidenceTraceResult `json:"semantic"`
}
type SemanticTrace = tracecontract.EvidenceTraceResult
