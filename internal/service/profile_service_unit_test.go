package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

type unitProfileRepo struct {
	profile       *domain.Profile
	createErr     error
	getErr        error
	listErr       error
	countErr      error
	updateErr     error
	softDeleteErr error
	nameExists    bool
	nameExistsErr error
	created       *domain.Profile
	updated       *domain.Profile
	deletedID     uuid.UUID
}

func (r *unitProfileRepo) Create(_ context.Context, profile *domain.Profile) error {
	if r.createErr != nil {
		return r.createErr
	}
	if profile.ID == uuid.Nil {
		profile.ID = uuid.New()
	}
	r.created = profile
	r.profile = profile
	return nil
}

func (r *unitProfileRepo) GetByID(context.Context, uuid.UUID) (*domain.Profile, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.profile, nil
}

func (r *unitProfileRepo) List(context.Context, int, int) ([]*domain.Profile, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return []*domain.Profile{r.profile}, nil
}

func (r *unitProfileRepo) Count(context.Context) (int64, error) {
	if r.countErr != nil {
		return 0, r.countErr
	}
	return 1, nil
}

func (r *unitProfileRepo) Update(_ context.Context, profile *domain.Profile) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	copy := *profile
	r.updated = &copy
	r.profile = &copy
	return nil
}

func (r *unitProfileRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	if r.softDeleteErr != nil {
		return r.softDeleteErr
	}
	r.deletedID = id
	return nil
}

func (r *unitProfileRepo) HardDelete(context.Context, uuid.UUID) error { return nil }

func (r *unitProfileRepo) CountActiveKeys(context.Context, uuid.UUID) (int64, error) { return 0, nil }

func (r *unitProfileRepo) NameExists(context.Context, string) (bool, error) {
	if r.nameExistsErr != nil {
		return false, r.nameExistsErr
	}
	return r.nameExists, nil
}

type unitProfileDataPurger struct {
	err       error
	profileID string
}

func (p *unitProfileDataPurger) PurgeProfileData(_ context.Context, profileID string) error {
	p.profileID = profileID
	return p.err
}

func TestProfileServiceCreateGetListCountBranches(t *testing.T) {
	ctx := context.Background()
	audit := new(MockAuditService)
	audit.On("ProfileCreated", ctx, mock.AnythingOfType("string"), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(errors.New("audit failed"))
	repo := &unitProfileRepo{}
	svc := NewProfileService(repo, audit, nil)

	profile, err := svc.Create(ctx, CreateProfileRequest{Name: "Team", Description: "desc", Metadata: map[string]any{"a": "b"}, Config: map[string]any{"enabled": true}}, nil, "system", "127.0.0.1", "corr")
	require.NoError(t, err)
	require.Equal(t, "Team", profile.Name)
	require.NotEqual(t, uuid.Nil, profile.ID)

	got, err := svc.Get(ctx, profile.ID)
	require.NoError(t, err)
	require.Equal(t, profile.ID, got.ID)
	internalGot, err := svc.GetByID(ctx, profile.ID)
	require.NoError(t, err)
	require.Equal(t, profile.ID, internalGot.ID)

	listed, err := svc.List(ctx, 20, 0)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	total, err := svc.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)

	repo.profile = nil
	_, err = svc.Get(ctx, uuid.New())
	require.Error(t, err)
	repo.getErr = errors.New("get failed")
	_, err = svc.Get(ctx, uuid.New())
	require.ErrorContains(t, err, "failed to get profile")
	audit.AssertExpectations(t)
}

func TestProfileServiceCreateUpdateErrorBranches(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()

	repo := &unitProfileRepo{createErr: &pq.Error{Code: "23505"}}
	svc := NewProfileService(repo, new(MockAuditService), nil)
	_, err := svc.Create(ctx, CreateProfileRequest{Name: "Team"}, nil, "system", "127.0.0.1", "corr")
	var apiErr *httperr.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.CONFLICT, apiErr.Code)

	repo = &unitProfileRepo{createErr: errors.New("insert failed")}
	svc = NewProfileService(repo, new(MockAuditService), nil)
	_, err = svc.Create(ctx, CreateProfileRequest{Name: "Team"}, nil, "system", "127.0.0.1", "corr")
	require.ErrorContains(t, err, "failed to create profile")

	existing := &domain.Profile{ID: id, Name: "Old", Description: "old", Metadata: map[string]any{"old": true}, Config: map[string]any{"a": "b"}}
	repo = &unitProfileRepo{profile: existing, nameExists: true}
	svc = NewProfileService(repo, new(MockAuditService), nil)
	newName := "New"
	_, err = svc.Update(ctx, id, UpdateProfileRequest{Name: &newName}, nil, "system", "127.0.0.1", "corr")
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.CONFLICT, apiErr.Code)

	repo = &unitProfileRepo{profile: existing, nameExistsErr: errors.New("lookup failed")}
	svc = NewProfileService(repo, new(MockAuditService), nil)
	_, err = svc.Update(ctx, id, UpdateProfileRequest{Name: &newName}, nil, "system", "127.0.0.1", "corr")
	require.ErrorContains(t, err, "failed to check name existence")

	repo = &unitProfileRepo{profile: existing, updateErr: &pq.Error{Code: "23505"}}
	svc = NewProfileService(repo, new(MockAuditService), nil)
	_, err = svc.Update(ctx, id, UpdateProfileRequest{Name: &newName}, nil, "system", "127.0.0.1", "corr")
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.CONFLICT, apiErr.Code)

	repo = &unitProfileRepo{profile: existing, updateErr: errors.New("update failed")}
	svc = NewProfileService(repo, new(MockAuditService), nil)
	_, err = svc.Update(ctx, id, UpdateProfileRequest{Name: &newName}, nil, "system", "127.0.0.1", "corr")
	require.ErrorContains(t, err, "failed to update profile")
}

func TestProfileServiceValidatesMemoryWriteConfidenceThreshold(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()

	for _, tc := range []struct {
		name   string
		config map[string]any
		want   string
	}{
		{
			name:   "memory write is not an object",
			config: map[string]any{"memory_write": "invalid"},
			want:   "memory_write must be an object",
		},
		{
			name:   "threshold is not numeric",
			config: map[string]any{"memory_write": map[string]any{"auto_write_confidence_threshold": "high"}},
			want:   "must be a number between 0 and 1",
		},
		{
			name:   "threshold below range",
			config: map[string]any{"memory_write": map[string]any{"auto_write_confidence_threshold": -0.01}},
			want:   "must be a number between 0 and 1",
		},
		{
			name:   "threshold above range",
			config: map[string]any{"memory_write": map[string]any{"auto_write_confidence_threshold": 1.01}},
			want:   "must be a number between 0 and 1",
		},
		{
			name:   "threshold is NaN",
			config: map[string]any{"memory_write": map[string]any{"auto_write_confidence_threshold": math.NaN()}},
			want:   "must be a number between 0 and 1",
		},
		{
			name:   "threshold is positive infinity",
			config: map[string]any{"memory_write": map[string]any{"auto_write_confidence_threshold": math.Inf(1)}},
			want:   "must be a number between 0 and 1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &unitProfileRepo{profile: &domain.Profile{ID: id, Name: "Team", Config: map[string]any{}}}
			svc := NewProfileService(repo, new(MockAuditService), nil)

			_, err := svc.Update(ctx, id, UpdateProfileRequest{Config: tc.config}, nil, "manager", "127.0.0.1", "corr")
			require.ErrorContains(t, err, tc.want)
			require.Nil(t, repo.updated)
		})
	}

	for _, threshold := range []float64{0, 1} {
		t.Run(fmt.Sprintf("accepts threshold %v", threshold), func(t *testing.T) {
			repo := &unitProfileRepo{}
			audit := new(MockAuditService)
			audit.On("ProfileCreated", ctx, mock.AnythingOfType("string"), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
			svc := NewProfileService(repo, audit, nil)
			config := map[string]any{"memory_write": map[string]any{"auto_write_confidence_threshold": threshold}}

			profile, err := svc.Create(ctx, CreateProfileRequest{Name: "Team", Config: config}, nil, "system", "127.0.0.1", "corr")
			require.NoError(t, err)
			require.Equal(t, config, profile.Config)
			audit.AssertExpectations(t)
		})
	}
}

func TestProfileServiceUpdateDeleteSuccessAndFailureBranches(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	existing := &domain.Profile{ID: id, Name: "Old", Description: "old", Metadata: map[string]any{"old": true}, Config: map[string]any{"a": "b"}}

	audit := new(MockAuditService)
	audit.On("ProfileUpdated", ctx, id.String(), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(errors.New("audit failed"))
	repo := &unitProfileRepo{profile: existing}
	svc := NewProfileService(repo, audit, nil)
	newName := "Old"
	newDesc := "new desc"
	updated, err := svc.Update(ctx, id, UpdateProfileRequest{Name: &newName, Description: &newDesc, Metadata: map[string]any{"new": true}, Config: map[string]any{"c": "d"}}, nil, "system", "127.0.0.1", "corr")
	require.NoError(t, err)
	require.Equal(t, "new desc", updated.Description)
	require.Equal(t, map[string]any{"new": true}, updated.Metadata)

	statePurger := new(MockProfileStatePurger)
	dataPurger := &unitProfileDataPurger{}
	audit = new(MockAuditService)
	audit.On("Append", ctx, mock.AnythingOfType("service.AuditLogEntry")).Return(errors.New("audit failed"))
	statePurger.On("PurgeProfileState", ctx, id.String()).Return(errors.New("redis failed"))
	repo = &unitProfileRepo{profile: existing}
	svc = NewProfileServiceWithDataPurger(repo, audit, statePurger, dataPurger)
	err = svc.Delete(ctx, id, nil, "system", "127.0.0.1", "corr")
	require.NoError(t, err)
	require.Equal(t, id, repo.deletedID)
	require.Empty(t, dataPurger.profileID)

	repo = &unitProfileRepo{profile: nil}
	svc = NewProfileService(repo, new(MockAuditService), nil)
	err = svc.Delete(ctx, id, nil, "system", "127.0.0.1", "corr")
	require.Error(t, err)

	repo = &unitProfileRepo{profile: existing, softDeleteErr: errors.New("delete failed")}
	svc = NewProfileService(repo, new(MockAuditService), nil)
	err = svc.Delete(ctx, id, nil, "system", "127.0.0.1", "corr")
	require.ErrorContains(t, err, "failed to delete profile")

	repo = &unitProfileRepo{profile: existing}
	dataPurger = &unitProfileDataPurger{err: errors.New("profile data purge failed")}
	audit = new(MockAuditService)
	audit.On("Append", ctx, mock.AnythingOfType("service.AuditLogEntry")).Return(nil)
	svc = NewProfileServiceWithDataPurger(repo, audit, nil, dataPurger)
	err = svc.Delete(ctx, id, nil, "system", "127.0.0.1", "corr")
	require.NoError(t, err)
	require.Empty(t, dataPurger.profileID)
}

func TestProfileHasActiveKeysError(t *testing.T) {
	id := uuid.New()
	err := &ProfileHasActiveKeysError{ProfileID: id}
	require.Contains(t, err.Error(), id.String())
	require.Equal(t, string(httperr.PROFILE_HAS_ACTIVE_KEYS), PROFILE_HAS_ACTIVE_KEYS)
}
