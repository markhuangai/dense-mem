package registry

import (
	"github.com/markhuangai/dense-mem/internal/dream"
	"github.com/markhuangai/dense-mem/internal/lifecycle"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

// CoreDependencies are shared immutable inputs used by multiple registry
// capabilities. They carry providers and policy owners; they do not contain
// capability-specific invokers.
type CoreDependencies struct {
	Metrics              observability.DiscoverabilityMetrics
	RecallFeedbackConfig RecallFeedbackConfigProvider
	RecallFeedbackEvents RecallFeedbackEventRecorder
	EvaluationAudit      EvaluationAuditAppender
}

type RememberBindings struct {
	Service memoryservice.RememberService
}

type LifecycleBindings struct {
	Service lifecycle.LifecycleService
}

type RecallBindings struct {
	Service memoryservice.RecallService
	Dreams  DreamingConfigProvider
}

type TraceBindings struct {
	Service contextservice.Service
}

type DreamBindings struct {
	Service dream.Service
}

type MemoryPackBindings struct {
	Service skillpackservice.MemoryPackService
}

func (d Dependencies) withCapabilityBindings() Dependencies {
	if d.Metrics == nil {
		d.Metrics = d.Core.Metrics
	}
	if d.RecallFeedbackConfig == nil {
		d.RecallFeedbackConfig = d.Core.RecallFeedbackConfig
	}
	if d.RecallFeedbackEvents == nil {
		d.RecallFeedbackEvents = d.Core.RecallFeedbackEvents
	}
	if d.EvaluationAudit == nil {
		d.EvaluationAudit = d.Core.EvaluationAudit
	}
	if d.Remember == nil {
		d.Remember = d.RememberBindings.Service
	}
	if d.Lifecycle == nil {
		d.Lifecycle = d.LifecycleBindings.Service
	}
	if d.Recall == nil {
		d.Recall = d.RecallBindings.Service
	}
	if d.Context == nil {
		d.Context = d.TraceBindings.Service
	}
	if d.RecallDreaming == nil {
		if d.RecallBindings.Dreams != nil {
			d.RecallDreaming = d.RecallBindings.Dreams
		} else if d.Dreams != nil {
			d.RecallDreaming = d.Dreams
		} else {
			d.RecallDreaming = d.DreamBindings.Service
		}
	}
	if d.Dreams == nil {
		d.Dreams = d.DreamBindings.Service
	}
	if d.MemoryPack == nil {
		d.MemoryPack = d.MemoryPackBindings.Service
	}
	return d.withEvaluationBindings()
}
