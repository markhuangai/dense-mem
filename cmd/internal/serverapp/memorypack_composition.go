package serverapp

import (
	memorypackapp "github.com/markhuangai/dense-mem/internal/memorypack"
)

type memoryPackApplicationDependencies struct {
	Semantic memorypackapp.MemoryPackSemanticReader
}

func buildMemoryPackApplication(deps memoryPackApplicationDependencies) memorypackapp.MemoryPackService {
	return memorypackapp.NewMemoryPackService(memorypackapp.MemoryPackDependencies{
		Semantic: deps.Semantic,
	})
}
