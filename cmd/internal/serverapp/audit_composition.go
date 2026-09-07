package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/service"
	"gorm.io/gorm"
)

func buildAuditApplication(db *gorm.DB) *service.AuditServiceImpl {
	return service.NewAuditService(db)
}
