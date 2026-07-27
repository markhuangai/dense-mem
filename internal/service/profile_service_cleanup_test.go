package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// TestProfileServiceDelete_CallsStatePurger proves that a successful profile
// delete invokes PurgeProfileState on the injected state purger (AC-03, AC-E2).
func TestProfileServiceDelete_CallsStatePurger(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := new(MockProfileRepository)
	audit := new(MockAuditService)
	purger := new(MockProfileStatePurger)

	id := uuid.New()
	repo.On("GetByID", ctx, id).Return(&domain.Profile{ID: id, Name: "p"}, nil)
	repo.On("SoftDelete", ctx, id).Return(nil)
	purger.On("PurgeProfileState", ctx, id.String()).Return(nil)
	audit.On("Append", mock.Anything, mock.MatchedBy(func(entry AuditLogEntry) bool {
		return entry.Operation == "DELETE" &&
			entry.EntityType == "profile" &&
			entry.EntityID == id.String() &&
			entry.ProfileID != nil &&
			*entry.ProfileID == id.String()
	})).Return(nil)

	svc := NewProfileService(repo, audit, purger)
	err := svc.Delete(ctx, id, nil, "system", "127.0.0.1", "corr-1")
	require.NoError(t, err)

	purger.AssertCalled(t, "PurgeProfileState", ctx, id.String())
	repo.AssertExpectations(t)
	audit.AssertExpectations(t)
}

// TestProfileServiceDelete_NilPurgerIsSafe proves that a nil statePurger does not
// panic and the delete still succeeds (AC-E2: no-op cleanup repos are valid in no-Redis mode).
func TestProfileServiceDelete_NilPurgerIsSafe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := new(MockProfileRepository)
	audit := new(MockAuditService)

	id := uuid.New()
	repo.On("GetByID", ctx, id).Return(&domain.Profile{ID: id, Name: "p"}, nil)
	repo.On("SoftDelete", ctx, id).Return(nil)
	audit.On("Append", mock.Anything, mock.AnythingOfType("service.AuditLogEntry")).Return(nil)

	// purger is nil — this must not panic
	svc := NewProfileService(repo, audit, nil)
	err := svc.Delete(ctx, id, nil, "system", "127.0.0.1", "corr-2")
	require.NoError(t, err)

	repo.AssertExpectations(t)
	audit.AssertExpectations(t)
}

// TestProfileServiceDelete_DoesNotPurgePostgresData proves team tombstone
// deletion does not invoke the durable-data purge hook.
func TestProfileServiceDelete_DoesNotPurgePostgresData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := new(MockProfileRepository)
	audit := new(MockAuditService)
	dataPurger := new(MockProfileDataPurger)

	id := uuid.New()
	repo.On("GetByID", ctx, id).Return(&domain.Profile{ID: id, Name: "p"}, nil)
	repo.On("SoftDelete", ctx, id).Return(nil)
	audit.On("Append", mock.Anything, mock.AnythingOfType("service.AuditLogEntry")).Return(nil)

	svc := NewProfileServiceWithDataPurger(repo, audit, nil, dataPurger)
	err := svc.Delete(ctx, id, nil, "system", "127.0.0.1", "corr-3")
	require.NoError(t, err)

	dataPurger.AssertNotCalled(t, "PurgeProfileData", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
	audit.AssertExpectations(t)
}

func TestProfileServiceDelete_DoesNotPurgeDataWhenSoftDeleteFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := new(MockProfileRepository)
	audit := new(MockAuditService)
	dataPurger := new(MockProfileDataPurger)

	id := uuid.New()
	repo.On("GetByID", ctx, id).Return(&domain.Profile{ID: id, Name: "p"}, nil)
	repo.On("SoftDelete", ctx, id).Return(errors.New("blocked by database"))

	svc := NewProfileServiceWithDataPurger(repo, audit, nil, dataPurger)
	err := svc.Delete(ctx, id, nil, "system", "127.0.0.1", "corr-4")
	require.ErrorContains(t, err, "failed to delete profile")

	dataPurger.AssertNotCalled(t, "PurgeProfileData", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
	audit.AssertExpectations(t)
}

// MockProfileRepository is a mock implementation of repository.ProfileRepository
// for unit tests that need to isolate the service layer.
type MockProfileRepository struct {
	mock.Mock
}

func (m *MockProfileRepository) Create(ctx context.Context, profile *domain.Profile) error {
	args := m.Called(ctx, profile)
	return args.Error(0)
}

func (m *MockProfileRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Profile), args.Error(1)
}

func (m *MockProfileRepository) List(ctx context.Context, limit, offset int) ([]*domain.Profile, error) {
	args := m.Called(ctx, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.Profile), args.Error(1)
}

func (m *MockProfileRepository) Count(ctx context.Context) (int64, error) {
	args := m.Called(ctx)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockProfileRepository) Update(ctx context.Context, profile *domain.Profile) error {
	args := m.Called(ctx, profile)
	return args.Error(0)
}

func (m *MockProfileRepository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockProfileRepository) HardDelete(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockProfileRepository) CountActiveKeys(ctx context.Context, profileID uuid.UUID) (int64, error) {
	args := m.Called(ctx, profileID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockProfileRepository) NameExists(ctx context.Context, name string) (bool, error) {
	args := m.Called(ctx, name)
	return args.Get(0).(bool), args.Error(1)
}

type MockProfileDataPurger struct {
	mock.Mock
}

func (m *MockProfileDataPurger) PurgeProfileData(ctx context.Context, profileID string) error {
	args := m.Called(ctx, profileID)
	return args.Error(0)
}
