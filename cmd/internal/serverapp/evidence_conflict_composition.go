package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/evidenceconflict"
)

func buildEvidenceConflictApplication(store repository.EvidenceConflictRepository) *evidenceconflict.Service {
	return evidenceconflict.New(store)
}
