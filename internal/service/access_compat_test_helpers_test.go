package service

import (
	"context"
	"sync"

	"github.com/stretchr/testify/mock"

	"github.com/markhuangai/dense-mem/internal/observability"
)

// MockAuditService keeps the root service package tests independent from the
// access package's moved test fixtures.
type MockAuditService struct{ mock.Mock }

func (m *MockAuditService) Append(ctx context.Context, entry AuditLogEntry) error {
	return m.Called(ctx, entry).Error(0)
}
func (m *MockAuditService) List(ctx context.Context, teamID string, limit, offset int) ([]AuditLogEntry, int, error) {
	a := m.Called(ctx, teamID, limit, offset)
	var entries []AuditLogEntry
	if a.Get(0) != nil {
		entries = a.Get(0).([]AuditLogEntry)
	}
	return entries, a.Int(1), a.Error(2)
}
func (m *MockAuditService) TeamCreated(ctx context.Context, teamID string, after map[string]interface{}, key *string, role, ip, correlation string) error {
	return m.Called(ctx, teamID, after, key, role, ip, correlation).Error(0)
}
func (m *MockAuditService) TeamUpdated(ctx context.Context, teamID string, before, after map[string]interface{}, key *string, role, ip, correlation string) error {
	return m.Called(ctx, teamID, before, after, key, role, ip, correlation).Error(0)
}
func (m *MockAuditService) TeamDeleteBlocked(ctx context.Context, teamID string, before map[string]interface{}, key *string, role, ip, correlation, reason string) error {
	return m.Called(ctx, teamID, before, key, role, ip, correlation, reason).Error(0)
}
func (m *MockAuditService) TeamDeleted(ctx context.Context, teamID string, before map[string]interface{}, key *string, role, ip, correlation string) error {
	return m.Called(ctx, teamID, before, key, role, ip, correlation).Error(0)
}
func (m *MockAuditService) CredentialCreated(ctx context.Context, teamID *string, credentialID string, after map[string]interface{}, key *string, role, ip, correlation string) error {
	return m.Called(ctx, teamID, credentialID, after, key, role, ip, correlation).Error(0)
}
func (m *MockAuditService) CredentialRevoked(ctx context.Context, teamID *string, credentialID string, before map[string]interface{}, key *string, role, ip, correlation string) error {
	return m.Called(ctx, teamID, credentialID, before, key, role, ip, correlation).Error(0)
}
func (m *MockAuditService) AuthFailure(ctx context.Context, teamID *string, entityType, entityID string, metadata map[string]interface{}, ip, correlation string) error {
	return m.Called(ctx, teamID, entityType, entityID, metadata, ip, correlation).Error(0)
}
func (m *MockAuditService) CrossTeamDenied(ctx context.Context, actorTeamID, targetTeamID, operation string, metadata map[string]interface{}, ip, correlation string) error {
	return m.Called(ctx, actorTeamID, targetTeamID, operation, metadata, ip, correlation).Error(0)
}
func (m *MockAuditService) RateLimited(ctx context.Context, teamID *string, operation string, metadata map[string]interface{}, ip, correlation string) error {
	return m.Called(ctx, teamID, operation, metadata, ip, correlation).Error(0)
}
func (m *MockAuditService) SystemQuery(ctx context.Context, queryType string, metadata map[string]interface{}, key *string, role, ip, correlation string) error {
	return m.Called(ctx, queryType, metadata, key, role, ip, correlation).Error(0)
}
func (m *MockAuditService) InvariantViolation(ctx context.Context, entityType, entityID, violation string, metadata map[string]interface{}, ip, correlation string) error {
	return m.Called(ctx, entityType, entityID, violation, metadata, ip, correlation).Error(0)
}

type activityLogger struct {
	mu       sync.Mutex
	warnings []string
}

func (*activityLogger) Info(string, ...observability.LogAttr)         {}
func (*activityLogger) Error(string, error, ...observability.LogAttr) {}
func (l *activityLogger) Warn(message string, _ ...observability.LogAttr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnings = append(l.warnings, message)
}
func (*activityLogger) Debug(string, ...observability.LogAttr)                    {}
func (l *activityLogger) With(...observability.LogAttr) observability.LogProvider { return l }
