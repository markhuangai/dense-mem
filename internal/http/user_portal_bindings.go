package http

import (
	"github.com/markhuangai/dense-mem/internal/dream"
	"github.com/markhuangai/dense-mem/internal/service/graphview"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

// MemoryPortalBindings owns first-party graph, recall, Dream, and private
// memory readers used by the user portal.
type MemoryPortalBindings struct {
	GraphView     graphview.Service
	RecallSvc     memoryservice.RecallService
	DreamSvc      dream.Service
	PrivateMemory PrivateMemoryServiceInterface
}

func (d UserPortalDeps) withMemoryBindings() UserPortalDeps {
	if d.Memory.GraphView != nil {
		d.GraphView = d.Memory.GraphView
	}
	if d.Memory.RecallSvc != nil {
		d.RecallSvc = d.Memory.RecallSvc
	}
	if d.Memory.DreamSvc != nil {
		d.DreamSvc = d.Memory.DreamSvc
	}
	if d.Memory.PrivateMemory != nil {
		d.PrivateMemory = d.Memory.PrivateMemory
	}
	return d
}
