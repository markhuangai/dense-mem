package serverapp

import (
	memorypackapp "github.com/markhuangai/dense-mem/internal/memorypack"
	traceapp "github.com/markhuangai/dense-mem/internal/trace"
)

type memoryPackApplicationDependencies struct {
	Trace traceapp.SemanticTraceStore
}

func buildMemoryPackApplication(deps memoryPackApplicationDependencies) memorypackapp.MemoryPackService {
	return memorypackapp.NewMemoryPackService(memorypackapp.MemoryPackDependencies{
		Semantic: deps.Trace,
	})
}
