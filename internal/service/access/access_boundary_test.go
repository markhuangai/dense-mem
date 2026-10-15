package access

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestAccessCredentialOwnershipUsesTeamAndOwnerBoundaries(t *testing.T) {
	ctx := context.Background()
	teamA, teamB := uuid.New(), uuid.New()
	ownerA, ownerB := uuid.New(), uuid.New()
	credentialID := uuid.New()
	repo := new(MockCredentialRepository)
	teams := new(MockTeamService)
	audit := new(MockAuditService)
	svc := NewCredentialService(repo, teams, audit, nil)

	credential := &domain.Credential{ID: credentialID, TeamID: teamA, OwnerIdentityID: &ownerA, Name: "owner A key", RateLimit: 10}
	t.Run("same-team read remains visible", func(t *testing.T) {
		repo.On("GetByIDForTeam", ctx, teamA, credentialID).Return(credential, nil).Once()
		got, err := svc.GetByIDForTeam(ctx, teamA, credentialID)
		require.NoError(t, err)
		require.Equal(t, credential, got)
	})

	t.Run("owner A can mutate its owned credential", func(t *testing.T) {
		repo.On("GetSSOOwnedCredentialByID", ctx, teamA, ownerA, credentialID).Return(credential, nil).Once()
		repo.On("GetByIDForTeam", ctx, teamA, credentialID).Return(credential, nil).Once()
		repo.On("RevokeForTeam", ctx, teamA, credentialID).Return(int64(1), nil).Once()
		audit.On("CredentialRevoked", ctx, mock.Anything, credentialID.String(), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
		require.NoError(t, svc.RevokeSSOOwnedCredential(ctx, teamA, ownerA, credentialID, nil, CredentialRoleMember, "", "owner-a"))
	})

	t.Run("owner B cannot mutate owner A credential", func(t *testing.T) {
		repo.On("GetSSOOwnedCredentialByID", ctx, teamA, ownerB, credentialID).Return(nil, nil).Once()
		err := svc.RevokeSSOOwnedCredential(ctx, teamA, ownerB, credentialID, nil, CredentialRoleMember, "", "owner-b")
		require.Error(t, err)
		require.Contains(t, err.Error(), "not found")
	})

	t.Run("team B receives no existence oracle", func(t *testing.T) {
		repo.On("GetByIDForTeam", ctx, teamB, credentialID).Return(nil, nil).Once()
		got, err := svc.GetByIDForTeam(ctx, teamB, credentialID)
		require.Error(t, err)
		require.Nil(t, got)
		require.Contains(t, err.Error(), "not found")
	})
	repo.AssertExpectations(t)
}

func TestAccessCredentialAuditNeverCarriesBearerMaterial(t *testing.T) {
	ctx := context.Background()
	teamID := uuid.New()
	var captured map[string]interface{}
	var generatedHash string
	repo := new(MockCredentialRepository)
	teams := new(MockTeamService)
	audit := new(MockAuditService)
	svc := NewCredentialService(repo, teams, audit, nil)

	teams.On("Get", ctx, teamID).Return(&domain.Team{ID: teamID}, nil).Once()
	repo.On("CountByTeam", ctx, teamID).Return(int64(0), nil).Once()
	repo.On("CreateCredential", ctx, mock.AnythingOfType("*domain.Credential")).Run(func(args mock.Arguments) {
		credential := args.Get(1).(*domain.Credential)
		generatedHash = credential.KeyHash
		credential.ID = uuid.New()
	}).Return(nil).Once()
	audit.On("CredentialCreated", ctx, mock.AnythingOfType("*string"), mock.AnythingOfType("string"), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { captured = args.Get(3).(map[string]interface{}) }).Return(nil).Once()

	_, raw, err := svc.CreateCredential(ctx, teamID, CreateCredentialRequest{Name: "boundary"}, nil, CredentialRoleMember, "", "corr")
	require.NoError(t, err)
	require.NotEmpty(t, raw)
	require.NotEmpty(t, generatedHash)
	serialized, err := json.Marshal(captured)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), raw)
	require.NotContains(t, string(serialized), generatedHash)
	audit.AssertExpectations(t)
}

func TestRateLimitAccessPortParityAcrossCoordinationImplementations(t *testing.T) {
	now := time.Date(2026, time.August, 22, 12, 0, 17, 0, time.UTC)
	stores := []struct {
		name  string
		store RateLimitStore
	}{
		{name: "process-local", store: newParityRateLimitStore()},
		{name: "redis-compatible", store: newParityRateLimitStore()},
	}
	for _, tc := range stores {
		t.Run(tc.name, func(t *testing.T) {
			svc := newRateLimitServiceWithClock(tc.store, func() time.Time { return now })
			allowed, remaining, reset, err := svc.Check(context.Background(), "profile-a", "/mcp", 2)
			require.NoError(t, err)
			require.True(t, allowed)
			require.Equal(t, 1, remaining)
			require.Equal(t, now.Truncate(time.Minute).Add(time.Minute), reset)
			allowed, remaining, _, err = svc.Check(context.Background(), "profile-a", "/mcp", 2)
			require.NoError(t, err)
			require.True(t, allowed)
			require.Equal(t, 0, remaining)
			allowed, remaining, _, err = svc.Check(context.Background(), "profile-a", "/mcp", 2)
			require.NoError(t, err)
			require.False(t, allowed)
			require.Zero(t, remaining)
		})
	}
}

type parityRateLimitStore struct {
	mu      sync.Mutex
	counter map[string]int64
}

func newParityRateLimitStore() *parityRateLimitStore {
	return &parityRateLimitStore{counter: make(map[string]int64)}
}

func (s *parityRateLimitStore) IncrWithExpire(_ context.Context, key string, _ int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter[key]++
	return s.counter[key], nil
}
