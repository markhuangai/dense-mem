package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

type memoryPackApplicationDependencies struct {
	Semantic skillpackservice.MemoryPackSemanticReader
}

func buildMemoryPackApplication(deps memoryPackApplicationDependencies) skillpackservice.MemoryPackService {
	return skillpackservice.NewMemoryPackService(skillpackservice.MemoryPackDependencies{
		Semantic: deps.Semantic,
	})
}
