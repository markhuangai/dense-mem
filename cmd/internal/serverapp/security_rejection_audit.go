package serverapp

import (
	"context"

	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	"github.com/markhuangai/dense-mem/internal/service"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

// securityRejectionAuditAppender is retained as a composition seam for the
// existing server tests. Event construction belongs to the audit capability.
type securityRejectionAuditAppender interface {
	Append(context.Context, service.AuditLogEntry) error
}

func newRememberSecurityRejectionAuditAdapter(audit securityRejectionAuditAppender) rememberapp.SecurityRejectionAuditor {
	return auditapp.NewRememberSecurityRejectionAuditor(audit)
}
