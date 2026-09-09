package settings

import (
	"context"

	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

type appConfigAuditStub struct {
	entries []accessservice.AuditLogEntry
}

func (a *appConfigAuditStub) Append(_ context.Context, entry accessservice.AuditLogEntry) error {
	a.entries = append(a.entries, entry)
	return nil
}

func (a *appConfigAuditStub) List(context.Context, string, int, int) ([]accessservice.AuditLogEntry, int, error) {
	return nil, 0, nil
}

func (a *appConfigAuditStub) TeamCreated(context.Context, string, map[string]interface{}, *string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) TeamUpdated(context.Context, string, map[string]interface{}, map[string]interface{}, *string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) TeamDeleteBlocked(context.Context, string, map[string]interface{}, *string, string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) TeamDeleted(context.Context, string, map[string]interface{}, *string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) CredentialCreated(context.Context, *string, string, map[string]interface{}, *string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) CredentialRevoked(context.Context, *string, string, map[string]interface{}, *string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) AuthFailure(context.Context, *string, string, string, map[string]interface{}, string, string) error {
	return nil
}

func (a *appConfigAuditStub) CrossTeamDenied(context.Context, string, string, string, map[string]interface{}, string, string) error {
	return nil
}

func (a *appConfigAuditStub) RateLimited(context.Context, *string, string, map[string]interface{}, string, string) error {
	return nil
}

func (a *appConfigAuditStub) SystemQuery(context.Context, string, map[string]interface{}, *string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) InvariantViolation(context.Context, string, string, string, map[string]interface{}, string, string) error {
	return nil
}
