package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// TestTeamServiceDelete_CallsStatePurger proves that a successful profile
// delete invokes PurgeTeamState on the injected state purger (AC-03, AC-E2).
func TestTeamServiceDelete_CallsStatePurger(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := new(MockTeamRepository)
	audit := new(MockAuditService)
	purger := new(MockTeamStatePurger)

	id := uuid.New()
	repo.On("GetByID", ctx, id).Return(&domain.Team{ID: id, Name: "p"}, nil)
	repo.On("SoftDelete", ctx, id).Return(nil)
	purger.On("PurgeTeamState", ctx, id.String()).Return(nil)
	audit.On("Append", mock.Anything, mock.MatchedBy(func(entry AuditLogEntry) bool {
		return entry.Operation == "DELETE" &&
			entry.EntityType == "profile" &&
			entry.EntityID == id.String() &&
			entry.ProfileID != nil &&
			*entry.ProfileID == id.String()
	})).Return(nil)

	svc := NewTeamService(repo, audit, purger)
	err := svc.Delete(ctx, id, nil, "system", "127.0.0.1", "corr-1")
	require.NoError(t, err)

	purger.AssertCalled(t, "PurgeTeamState", ctx, id.String())
	repo.AssertExpectations(t)
	audit.AssertExpectations(t)
}

// TestTeamServiceDelete_NilPurgerIsSafe proves that a nil statePurger does not
// panic and the delete still succeeds (AC-E2: no-op cleanup repos are valid in no-Redis mode).
func TestTeamServiceDelete_NilPurgerIsSafe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := new(MockTeamRepository)
	audit := new(MockAuditService)

	id := uuid.New()
	repo.On("GetByID", ctx, id).Return(&domain.Team{ID: id, Name: "p"}, nil)
	repo.On("SoftDelete", ctx, id).Return(nil)
	audit.On("Append", mock.Anything, mock.AnythingOfType("service.AuditLogEntry")).Return(nil)

	// purger is nil — this must not panic
	svc := NewTeamService(repo, audit, nil)
	err := svc.Delete(ctx, id, nil, "system", "127.0.0.1", "corr-2")
	require.NoError(t, err)

	repo.AssertExpectations(t)
	audit.AssertExpectations(t)
}

// MockTeamRepository is a mock implementation of repository.TeamRepository
// for unit tests that need to isolate the service layer.
type MockTeamRepository struct {
	mock.Mock
}

type MockTeamStatePurger struct {
	mock.Mock
}

func (m *MockTeamStatePurger) PurgeTeamState(ctx context.Context, teamID string) error {
	args := m.Called(ctx, teamID)
	return args.Error(0)
}

func (m *MockTeamRepository) Create(ctx context.Context, profile *domain.Team) error {
	args := m.Called(ctx, profile)
	return args.Error(0)
}

func (m *MockTeamRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Team), args.Error(1)
}

func (m *MockTeamRepository) List(ctx context.Context, limit, offset int) ([]*domain.Team, error) {
	args := m.Called(ctx, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.Team), args.Error(1)
}

func (m *MockTeamRepository) Count(ctx context.Context) (int64, error) {
	args := m.Called(ctx)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockTeamRepository) Update(ctx context.Context, profile *domain.Team) error {
	args := m.Called(ctx, profile)
	return args.Error(0)
}

func (m *MockTeamRepository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockTeamRepository) HardDelete(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockTeamRepository) CountActiveKeys(ctx context.Context, profileID uuid.UUID) (int64, error) {
	args := m.Called(ctx, profileID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockTeamRepository) NameExists(ctx context.Context, name string) (bool, error) {
	args := m.Called(ctx, name)
	return args.Get(0).(bool), args.Error(1)
}
