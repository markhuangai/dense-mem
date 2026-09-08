package serverapp

import (
	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"gorm.io/gorm"
)

func buildAuditApplication(db *gorm.DB) *auditapp.Service {
	return auditapp.New(auditpostgres.NewStore(db))
}
