package registry

import (
	"github.com/markhuangai/dense-mem/internal/dream"
	"github.com/markhuangai/dense-mem/internal/lifecycle"
	"github.com/markhuangai/dense-mem/internal/memorypack"
	"github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	traceapp "github.com/markhuangai/dense-mem/internal/trace"
)

// CoreDependencies are shared immutable inputs used by multiple registry
// capabilities. They carry providers and policy owners; they do not contain
// capability-specific invokers.
type CoreDependencies struct {
	Metrics              recallcontract.FeedbackMetrics
	RecallFeedbackConfig RecallFeedbackConfigProvider
	RecallFeedbackEvents RecallFeedbackEventRecorder
	EvaluationAudit      EvaluationAuditAppender
}

type RememberBindings struct {
	Service rememberapp.Service
}

type LifecycleBindings struct {
	Service lifecycle.LifecycleService
}

type RecallBindings struct {
	Service recall.RecallService
	Dreams  DreamingConfigProvider
}

type TraceBindings struct {
	Service traceapp.Service
}

type DreamBindings struct {
	Service dream.Service
}

type MemoryPackBindings struct {
	Service memorypack.MemoryPackService
}
