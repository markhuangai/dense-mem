package serverapp

import (
	memorypackapp "github.com/markhuangai/dense-mem/internal/memorypack"
	"github.com/markhuangai/dense-mem/internal/observability"
	traceapp "github.com/markhuangai/dense-mem/internal/trace"
)

type memoryPackApplicationDependencies struct {
	Trace  traceapp.SemanticTraceStore
	Logger observability.LogProvider
}

func buildMemoryPackApplication(deps memoryPackApplicationDependencies) memorypackapp.MemoryPackService {
	return memorypackapp.NewMemoryPackService(memorypackapp.MemoryPackDependencies{
		Semantic: deps.Trace, Logger: deps.Logger,
	})
}
