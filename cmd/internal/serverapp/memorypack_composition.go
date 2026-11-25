package serverapp

import (
	memorypackapp "github.com/markhuangai/dense-mem/internal/memorypack"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type memoryPackApplicationDependencies struct {
	Trace  memorypackapp.MemoryPackSemanticReader
	Logger observability.LogProvider
}

func buildMemoryPackApplication(deps memoryPackApplicationDependencies) memorypackapp.MemoryPackService {
	return memorypackapp.NewMemoryPackService(memorypackapp.MemoryPackDependencies{
		Semantic: deps.Trace, Logger: deps.Logger,
	})
}
