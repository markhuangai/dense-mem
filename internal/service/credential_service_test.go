package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

// MockCredentialRepository is a mock implementation of repository.CredentialRepository
type MockCredentialRepository struct {
	mock.Mock
}

func (m *MockCredentialRepository) CreateCredential(ctx context.Context, key *domain.Credential) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

func (m *MockCredentialRepository) ListByTeam(ctx context.Context, profileID uuid.UUID, limit, offset int) ([]*domain.Credential, error) {
	args := m.Called(ctx, profileID, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.Credential), args.Error(1)
}

func (m *MockCredentialRepository) GetActiveByPrefix(ctx context.Context, prefix string) (*domain.Credential, error) {
	args := m.Called(ctx, prefix)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Credential), args.Error(1)
}

func (m *MockCredentialRepository) RevokeForTeam(ctx context.Context, profileID, id uuid.UUID) (int64, error) {
	args := m.Called(ctx, profileID, id)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockCredentialRepository) RotateForTeam(ctx context.Context, profileID, id uuid.UUID, keyHash, keyPrefix, keySuffix string, expiresAt *time.Time) (int64, error) {
	args := m.Called(ctx, profileID, id, keyHash, keyPrefix, keySuffix, expiresAt)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockCredentialRepository) UpdateNameForTeam(ctx context.Context, profileID, id uuid.UUID, name string) (int64, error) {
	args := m.Called(ctx, profileID, id, name)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockCredentialRepository) UpdateRoleForTeam(ctx context.Context, profileID, id uuid.UUID, role string, scopes []string) (int64, error) {
	args := m.Called(ctx, profileID, id, role, scopes)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockCredentialRepository) UpdateScopesForTeam(ctx context.Context, profileID, id uuid.UUID, scopes []string) (int64, error) {
	args := m.Called(ctx, profileID, id, scopes)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockCredentialRepository) DeleteForTeam(ctx context.Context, profileID, id uuid.UUID) (int64, error) {
	args := m.Called(ctx, profileID, id)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockCredentialRepository) GetByIDForTeam(ctx context.Context, profileID, id uuid.UUID) (*domain.Credential, error) {
	args := m.Called(ctx, profileID, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Credential), args.Error(1)
}

func (m *MockCredentialRepository) ListSSOOwnedCredentials(ctx context.Context, profileID, identityID uuid.UUID) ([]*domain.Credential, error) {
	args := m.Called(ctx, profileID, identityID)
	var keys []*domain.Credential
	if args.Get(0) != nil {
		keys = args.Get(0).([]*domain.Credential)
	}
	return keys, args.Error(1)
}

func (m *MockCredentialRepository) GetSSOOwnedCredentialByID(ctx context.Context, profileID, identityID, credentialID uuid.UUID) (*domain.Credential, error) {
	args := m.Called(ctx, profileID, identityID, credentialID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Credential), args.Error(1)
}

func (m *MockCredentialRepository) GetSSOOwnedCredential(ctx context.Context, profileID, identityID uuid.UUID) (*domain.Credential, error) {
	args := m.Called(ctx, profileID, identityID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Credential), args.Error(1)
}

func (m *MockCredentialRepository) CountByTeam(ctx context.Context, profileID uuid.UUID) (int64, error) {
	args := m.Called(ctx, profileID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockCredentialRepository) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

// MockTeamService is a mock implementation of TeamService
type MockTeamService struct {
	mock.Mock
}

func (m *MockTeamService) Create(ctx context.Context, req CreateTeamRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Team, error) {
	args := m.Called(ctx, req, actorKeyID, actorRole, clientIP, correlationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Team), args.Error(1)
}

func (m *MockTeamService) Get(ctx context.Context, id uuid.UUID) (*domain.Team, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Team), args.Error(1)
}

func (m *MockTeamService) List(ctx context.Context, limit, offset int) ([]*domain.Team, error) {
	args := m.Called(ctx, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.Team), args.Error(1)
}

func (m *MockTeamService) Update(ctx context.Context, id uuid.UUID, req UpdateTeamRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Team, error) {
	args := m.Called(ctx, id, req, actorKeyID, actorRole, clientIP, correlationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Team), args.Error(1)
}

func (m *MockTeamService) Delete(ctx context.Context, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, id, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockTeamService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Team), args.Error(1)
}

func (m *MockTeamService) Count(ctx context.Context) (int64, error) {
	args := m.Called(ctx)
	return args.Get(0).(int64), args.Error(1)
}

// MockAuditService is a mock implementation of AuditService
type MockAuditService struct {
	mock.Mock
}

func (m *MockAuditService) Append(ctx context.Context, entry AuditLogEntry) error {
	args := m.Called(ctx, entry)
	return args.Error(0)
}

func (m *MockAuditService) List(ctx context.Context, profileID string, limit, offset int) ([]AuditLogEntry, int, error) {
	args := m.Called(ctx, profileID, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int), args.Error(2)
	}
	return args.Get(0).([]AuditLogEntry), args.Get(1).(int), args.Error(2)
}

func (m *MockAuditService) TeamCreated(ctx context.Context, profileID string, afterPayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, afterPayload, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) TeamUpdated(ctx context.Context, profileID string, beforePayload, afterPayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, beforePayload, afterPayload, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) TeamDeleteBlocked(ctx context.Context, profileID string, beforePayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string, reason string) error {
	args := m.Called(ctx, profileID, beforePayload, actorKeyID, actorRole, clientIP, correlationID, reason)
	return args.Error(0)
}

func (m *MockAuditService) TeamDeleted(ctx context.Context, profileID string, beforePayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, beforePayload, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) CredentialCreated(ctx context.Context, profileID *string, keyID string, afterPayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, keyID, afterPayload, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) CredentialRevoked(ctx context.Context, profileID *string, keyID string, beforePayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, keyID, beforePayload, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) AuthFailure(ctx context.Context, profileID *string, entityType, entityID string, metadata map[string]interface{}, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, entityType, entityID, metadata, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) CrossTeamDenied(ctx context.Context, actorProfileID, targetProfileID string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	args := m.Called(ctx, actorProfileID, targetProfileID, operation, metadata, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) RateLimited(ctx context.Context, profileID *string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, operation, metadata, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) SystemQuery(ctx context.Context, queryType string, metadata map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, queryType, metadata, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) InvariantViolation(ctx context.Context, entityType, entityID string, violation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	args := m.Called(ctx, entityType, entityID, violation, metadata, clientIP, correlationID)
	return args.Error(0)
}

// MockCredentialSessionInvalidator is a mock implementation of CredentialSessionInvalidator
type MockCredentialSessionInvalidator struct {
	mock.Mock
}

func (m *MockCredentialSessionInvalidator) InvalidateCredentialSessions(ctx context.Context, profileID, keyID string) error {
	args := m.Called(ctx, profileID, keyID)
	return args.Error(0)
}

// TestGenerateCredentialFormat verifies the raw key format
func TestGenerateCredentialFormat(t *testing.T) {
	// Generate multiple keys to ensure consistency
	for i := 0; i < 10; i++ {
		key, err := crypto.GenerateRawKey()
		require.NoError(t, err, "GenerateRawKey should not return an error")

		// Check prefix
		assert.True(t, strings.HasPrefix(key, "dm_live_"), "Key should start with 'dm_live_'")

		// Check prefix extraction (first 24 chars)
		prefix := crypto.GetKeyPrefix(key)
		assert.Equal(t, 24, len(prefix), "Prefix should be 24 characters")
		assert.Equal(t, key[:24], prefix, "Prefix should be first 24 characters of key")

		// Check that it's valid base64url after the prefix
		encodedPart := strings.TrimPrefix(key, "dm_live_")
		assert.NotEmpty(t, encodedPart, "Encoded part should not be empty")

		// Verify no padding characters (base64url uses no padding)
		assert.NotContains(t, encodedPart, "=", "Base64url should not contain padding")
	}
}

// TestVerifyCredentialCorrect verifies that a valid key passes verification
func TestVerifyCredentialCorrect(t *testing.T) {
	rawKey, err := crypto.GenerateRawKey()
	require.NoError(t, err)

	hash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)

	// Verify the correct key passes
	assert.True(t, crypto.VerifyKey(rawKey, hash), "Correct key should verify")
}

// TestVerifyCredentialWrongKey verifies that a wrong key fails verification
func TestVerifyCredentialWrongKey(t *testing.T) {
	rawKey1, err := crypto.GenerateRawKey()
	require.NoError(t, err)

	rawKey2, err := crypto.GenerateRawKey()
	require.NoError(t, err)

	hash, err := crypto.HashKey(rawKey1)
	require.NoError(t, err)

	// Verify wrong key fails
	assert.False(t, crypto.VerifyKey(rawKey2, hash), "Wrong key should not verify")
}

// TestVerifyCredentialTampered verifies that a tampered hash fails verification
func TestVerifyCredentialTampered(t *testing.T) {
	rawKey, err := crypto.GenerateRawKey()
	require.NoError(t, err)

	hash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)

	// Tamper with a mid-hash character. Avoids the last base64 char, which for
	// a 32-byte argon2 output carries 2 padding bits — changing it can decode
	// to the same bytes and spuriously verify (~6% of the time).
	lastDollar := strings.LastIndex(hash, "$")
	require.Greater(t, lastDollar, 0, "PHC hash must contain $-separated HASH segment")
	mid := lastDollar + 1 + (len(hash)-lastDollar-1)/2
	replacement := byte('A')
	if hash[mid] == 'A' {
		replacement = 'B'
	}
	tamperedHash := hash[:mid] + string(replacement) + hash[mid+1:]

	// Verify tampered hash fails
	assert.False(t, crypto.VerifyKey(rawKey, tamperedHash), "Tampered hash should not verify")
}

// TestCredentialServiceCreate verifies that raw key is returned once and hash is stored
func TestCredentialServiceCreate(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()

	mockRepo := new(MockCredentialRepository)
	mockTeamService := new(MockTeamService)
	mockAuditService := new(MockAuditService)
	mockSessionInvalidator := new(MockCredentialSessionInvalidator)

	service := NewCredentialService(mockRepo, mockTeamService, mockAuditService, mockSessionInvalidator)

	// Setup expectations
	mockTeamService.On("Get", ctx, profileID).Return(&domain.Team{ID: profileID}, nil)
	mockRepo.On("CountByTeam", ctx, profileID).Return(int64(0), nil)
	mockRepo.On("CreateCredential", ctx, mock.AnythingOfType("*domain.Credential")).Run(func(args mock.Arguments) {
		key := args.Get(1).(*domain.Credential)
		assert.Equal(t, []string{"read", "write"}, key.Scopes)
		assert.Equal(t, CredentialRoleManager, key.Role)
		key.ID = uuid.New() // Simulate DB assigning ID
	}).Return(nil)
	mockAuditService.On("CredentialCreated", ctx, mock.AnythingOfType("*string"), mock.AnythingOfType("string"), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	req := CreateCredentialRequest{
		RateLimit: 100,
	}

	key, rawKey, err := service.CreateCredential(ctx, profileID, req, nil, "system", "127.0.0.1", "test-correlation")

	require.NoError(t, err)
	assert.NotNil(t, key)
	assert.NotEmpty(t, rawKey)
	assert.True(t, strings.HasPrefix(rawKey, "dm_default-cred_"), "Raw key should include the profile slug")
	assert.Empty(t, key.KeyHash, "KeyHash should not be returned")
	assert.Equal(t, crypto.GetKeySuffix(rawKey), key.KeySuffix, "KeySuffix should be last 6 raw key characters")
	assert.Equal(t, []string{"read", "write"}, key.Scopes)
	assert.Equal(t, "default credential", key.Name)
	assert.Equal(t, "default credential", key.Name)
	assert.Equal(t, CredentialRoleManager, key.Role)

	// Verify the raw key is not logged or stored in the response
	mockRepo.AssertExpectations(t)
	mockTeamService.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}

func TestCredentialServiceCreateReadOnlyKey(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()

	mockRepo := new(MockCredentialRepository)
	mockTeamService := new(MockTeamService)
	mockAuditService := new(MockAuditService)
	mockSessionInvalidator := new(MockCredentialSessionInvalidator)

	service := NewCredentialService(mockRepo, mockTeamService, mockAuditService, mockSessionInvalidator)

	mockTeamService.On("Get", ctx, profileID).Return(&domain.Team{ID: profileID}, nil)
	mockRepo.On("CountByTeam", ctx, profileID).Return(int64(1), nil)
	mockRepo.On("CreateCredential", ctx, mock.AnythingOfType("*domain.Credential")).Run(func(args mock.Arguments) {
		key := args.Get(1).(*domain.Credential)
		assert.Equal(t, []string{"read"}, key.Scopes)
		assert.Equal(t, CredentialRoleMember, key.Role)
		key.ID = uuid.New()
	}).Return(nil)
	mockAuditService.On("CredentialCreated", ctx, mock.AnythingOfType("*string"), mock.AnythingOfType("string"), mock.MatchedBy(func(payload map[string]interface{}) bool {
		scopes, ok := payload["scopes"].([]string)
		role, roleOK := payload["role"].(string)
		return ok && assert.ObjectsAreEqual([]string{"read"}, scopes) && roleOK && role == CredentialRoleMember
	}), mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	key, rawKey, err := service.CreateCredential(ctx, profileID, CreateCredentialRequest{
		Name:      "automation",
		Scopes:    []string{"read"},
		RateLimit: 100,
	}, nil, "system", "127.0.0.1", "test-correlation")

	require.NoError(t, err)
	require.NotNil(t, key)
	assert.NotEmpty(t, rawKey)
	assert.Equal(t, []string{"read"}, key.Scopes)
	assert.Equal(t, CredentialRoleMember, key.Role)
	assert.Empty(t, key.KeyHash)
	mockRepo.AssertExpectations(t)
	mockTeamService.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}

func TestNormalizeCredentialScopesRejectsUnsupportedSets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scopes []string
	}{
		{name: "write without read", scopes: []string{"write"}},
		{name: "feedback read without read", scopes: []string{"feedback:read"}},
		{name: "unknown scope", scopes: []string{"read", "admin"}},
		{name: "blank scope", scopes: []string{"read", " "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := NormalizeCredentialScopes(tc.scopes)
			require.Nil(t, normalized)
			require.Error(t, err)
			var apiErr *httperr.APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, httperr.VALIDATION_ERROR, apiErr.Code)
		})
	}
}

// TestCredentialServiceListNeverReturnsHash verifies that list never returns key_hash.
func TestCredentialServiceListNeverReturnsHash(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()

	mockRepo := new(MockCredentialRepository)
	mockTeamService := new(MockTeamService)
	mockAuditService := new(MockAuditService)
	mockSessionInvalidator := new(MockCredentialSessionInvalidator)

	service := NewCredentialService(mockRepo, mockTeamService, mockAuditService, mockSessionInvalidator)

	// Setup expectations for list
	keyID := uuid.New()
	now := time.Now()
	keys := []*domain.Credential{
		{
			ID:        keyID,
			TeamID:    profileID,
			Name:      "test-key",
			Scopes:    []string{"read"},
			CreatedAt: now,
		},
	}
	mockRepo.On("ListByTeam", ctx, profileID, 20, 0).Return(keys, nil)

	// Test list
	result, err := service.ListByTeam(ctx, profileID, 20, 0)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Empty(t, result[0].KeyHash, "List should not return key_hash")

	mockRepo.AssertExpectations(t)
}

func TestCredentialServiceUpdateNameForTeam(t *testing.T) {
	ctx := context.Background()
	keyID := uuid.New()
	profileID := uuid.New()

	mockRepo := new(MockCredentialRepository)
	mockTeamService := new(MockTeamService)
	mockAuditService := new(MockAuditService)
	mockSessionInvalidator := new(MockCredentialSessionInvalidator)

	service := NewCredentialService(mockRepo, mockTeamService, mockAuditService, mockSessionInvalidator)

	key := &domain.Credential{
		ID:        keyID,
		TeamID:    profileID,
		Name:      "default credential",
		KeyHash:   "secret-hash",
		CreatedAt: time.Now(),
	}

	mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil)
	mockRepo.On("UpdateNameForTeam", ctx, profileID, keyID, "research profile").Return(int64(1), nil)
	mockAuditService.On("Append", ctx, mock.MatchedBy(func(entry AuditLogEntry) bool {
		return entry.Operation == "UPDATE" &&
			entry.EntityType == "api_key" &&
			entry.EntityID == keyID.String() &&
			entry.ProfileID != nil &&
			*entry.ProfileID == profileID.String()
	})).Return(nil)

	updated, err := service.UpdateNameForTeam(ctx, profileID, keyID, " research profile ", nil, "system", "127.0.0.1", "test-correlation")

	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, "research profile", updated.Name)
	assert.Equal(t, "research profile", updated.Name)
	assert.Empty(t, updated.KeyHash, "KeyHash should not be returned")
	mockRepo.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}

func TestCredentialServiceUpdateNameForTeamDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "pgx", err: &pgconn.PgError{Code: "23505"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			keyID := uuid.New()
			profileID := uuid.New()

			mockRepo := new(MockCredentialRepository)
			mockTeamService := new(MockTeamService)
			mockAuditService := new(MockAuditService)
			mockSessionInvalidator := new(MockCredentialSessionInvalidator)

			service := NewCredentialService(mockRepo, mockTeamService, mockAuditService, mockSessionInvalidator)

			key := &domain.Credential{
				ID:        keyID,
				TeamID:    profileID,
				Name:      "default credential",
				CreatedAt: time.Now(),
			}

			mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil)
			mockRepo.On("UpdateNameForTeam", ctx, profileID, keyID, "existing profile").Return(int64(0), tc.err)

			updated, err := service.UpdateNameForTeam(ctx, profileID, keyID, "existing profile", nil, "system", "127.0.0.1", "test-correlation")

			require.Nil(t, updated)
			require.Error(t, err)
			var apiErr *httperr.APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, httperr.CONFLICT, apiErr.Code)
			assert.Contains(t, apiErr.Message, "credential with name 'existing profile'")
			mockRepo.AssertExpectations(t)
		})
	}
}

// TestCredentialServiceDeleteForTeam_CallsSessionInvalidator proves that DeleteForTeam
// hard-deletes the key and invalidates sessions.
func TestCredentialServiceDeleteForTeam_CallsSessionInvalidator(t *testing.T) {
	ctx := context.Background()
	keyID := uuid.New()
	profileID := uuid.New()

	mockRepo := new(MockCredentialRepository)
	mockTeamService := new(MockTeamService)
	mockAuditService := new(MockAuditService)
	mockSessionInvalidator := new(MockCredentialSessionInvalidator)

	service := NewCredentialService(mockRepo, mockTeamService, mockAuditService, mockSessionInvalidator)

	now := time.Now()
	key := &domain.Credential{
		ID:        keyID,
		TeamID:    profileID,
		RateLimit: 100,
		CreatedAt: now,
	}

	mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil)
	mockRepo.On("DeleteForTeam", ctx, profileID, keyID).Return(int64(1), nil)
	mockSessionInvalidator.On("InvalidateCredentialSessions", ctx, profileID.String(), keyID.String()).Return(nil)
	mockAuditService.On("Append", ctx, mock.MatchedBy(func(entry AuditLogEntry) bool {
		return entry.Operation == "DELETE" &&
			entry.EntityType == "api_key" &&
			entry.EntityID == keyID.String() &&
			entry.ProfileID != nil &&
			*entry.ProfileID == profileID.String()
	})).Return(nil)

	err := service.DeleteForTeam(ctx, profileID, keyID, nil, "system", "127.0.0.1", "test-correlation")

	require.NoError(t, err)
	mockSessionInvalidator.AssertCalled(t, "InvalidateCredentialSessions", ctx, profileID.String(), keyID.String())
	mockRepo.AssertExpectations(t)
	mockSessionInvalidator.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}

// TestCredentialServiceRevokeForTeam_CallsSessionInvalidator proves that RevokeForTeam
// invokes InvalidateCredentialSessions on the injected session invalidator (AC-03).
func TestCredentialServiceRevokeForTeam_CallsSessionInvalidator(t *testing.T) {
	ctx := context.Background()
	keyID := uuid.New()
	profileID := uuid.New()

	mockRepo := new(MockCredentialRepository)
	mockTeamService := new(MockTeamService)
	mockAuditService := new(MockAuditService)
	mockSessionInvalidator := new(MockCredentialSessionInvalidator)

	service := NewCredentialService(mockRepo, mockTeamService, mockAuditService, mockSessionInvalidator)

	now := time.Now()
	key := &domain.Credential{
		ID:        keyID,
		TeamID:    profileID,
		Name:      "test-key",
		Scopes:    []string{"read"},
		CreatedAt: now,
		RevokedAt: nil,
	}

	// GetByIDForTeam is called to verify ownership
	mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil)
	// RevokeForTeam returns 1 row affected
	mockRepo.On("RevokeForTeam", ctx, profileID, keyID).Return(int64(1), nil)
	// InvalidateCredentialSessions must be called
	mockSessionInvalidator.On("InvalidateCredentialSessions", ctx, profileID.String(), keyID.String()).Return(nil)
	mockAuditService.On("CredentialRevoked", ctx, mock.AnythingOfType("*string"), keyID.String(), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	err := service.RevokeForTeam(ctx, profileID, keyID, nil, "system", "127.0.0.1", "test-correlation")

	require.NoError(t, err)
	mockSessionInvalidator.AssertCalled(t, "InvalidateCredentialSessions", ctx, profileID.String(), keyID.String())
	mockRepo.AssertExpectations(t)
	mockSessionInvalidator.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}

// TestCredentialServiceRevokeForTeam_NilInvalidatorIsSafe proves that a nil session invalidator
// does not panic and the revoke still succeeds (AC-E2).
func TestCredentialServiceRevokeForTeam_NilInvalidatorIsSafe(t *testing.T) {
	ctx := context.Background()
	keyID := uuid.New()
	profileID := uuid.New()

	mockRepo := new(MockCredentialRepository)
	mockTeamService := new(MockTeamService)
	mockAuditService := new(MockAuditService)

	// sessionInvalidator is nil — this must not panic
	service := NewCredentialService(mockRepo, mockTeamService, mockAuditService, nil)

	now := time.Now()
	key := &domain.Credential{
		ID:        keyID,
		TeamID:    profileID,
		Name:      "test-key",
		Scopes:    []string{"read"},
		CreatedAt: now,
		RevokedAt: nil,
	}

	mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil)
	mockRepo.On("RevokeForTeam", ctx, profileID, keyID).Return(int64(1), nil)
	mockAuditService.On("CredentialRevoked", ctx, mock.AnythingOfType("*string"), keyID.String(), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	err := service.RevokeForTeam(ctx, profileID, keyID, nil, "system", "127.0.0.1", "test-correlation")

	require.NoError(t, err)
	mockRepo.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}

func TestCredentialServiceScopeNameAndConstructorHelpers(t *testing.T) {
	scopes := StandardCredentialScopes()
	scopes[0] = "mutated"
	assert.Equal(t, []string{"read", "write"}, StandardCredentialScopes())

	normalized, err := NormalizeCredentialScopes([]string{" WRITE ", "Read"})
	require.NoError(t, err)
	assert.Equal(t, []string{"read", "write"}, normalized)

	normalized, err = NormalizeCredentialScopes(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"read", "write"}, normalized)

	name, err := normalizeCredentialName(" research ")
	require.NoError(t, err)
	assert.Equal(t, "research", name)

	_, err = normalizeCredentialName(" ")
	require.ErrorContains(t, err, "credential name is required")
	_, err = normalizeCredentialName(strings.Repeat("x", 101))
	require.ErrorContains(t, err, "credential name must be at most 100 characters")
	assert.Nil(t, credentialNameConflict(errors.New("not unique"), "name"))
	assert.Equal(t, "idx_credentials_owner_team_active_unique", uniqueViolationName(&pgconn.PgError{
		Code:           "23505",
		ConstraintName: "idx_credentials_owner_team_active_unique",
	}))
	assert.Empty(t, uniqueViolationName(&pgconn.PgError{Code: "23503"}))
	require.ErrorContains(t, credentialCreateConflict(&pgconn.PgError{
		Code:           "23505",
		ConstraintName: "idx_credentials_owner_team_active_unique",
	}, "name"), "api credential already exists")

	svc := NewCredentialServiceWithLogger(nil, nil, nil, nil, nil)
	require.Nil(t, svc.logger)
	svc.logAuditError(errors.New("audit failed"), "CREATE", "key-1", "corr")
}

func TestCredentialServiceCreateCredentialErrorAndAuditBranches(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()

	t.Run("profile lookup error", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		mockTeamService := new(MockTeamService)
		mockAuditService := new(MockAuditService)
		svc := NewCredentialService(mockRepo, mockTeamService, mockAuditService, nil)
		mockTeamService.On("Get", ctx, profileID).Return(nil, errors.New("profile failed"))

		key, raw, err := svc.CreateCredential(ctx, profileID, CreateCredentialRequest{}, nil, "system", "127.0.0.1", "corr")

		require.ErrorContains(t, err, "failed to verify team")
		require.Nil(t, key)
		require.Empty(t, raw)
		mockTeamService.AssertExpectations(t)
	})

	t.Run("invalid scopes", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		mockTeamService := new(MockTeamService)
		mockAuditService := new(MockAuditService)
		svc := NewCredentialService(mockRepo, mockTeamService, mockAuditService, nil)
		mockTeamService.On("Get", ctx, profileID).Return(&domain.Team{ID: profileID}, nil)

		key, raw, err := svc.CreateCredential(ctx, profileID, CreateCredentialRequest{Scopes: []string{"write"}}, nil, "system", "127.0.0.1", "corr")

		require.Error(t, err)
		require.Nil(t, key)
		require.Empty(t, raw)
		mockTeamService.AssertExpectations(t)
	})

	t.Run("repo error", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		mockTeamService := new(MockTeamService)
		mockAuditService := new(MockAuditService)
		svc := NewCredentialService(mockRepo, mockTeamService, mockAuditService, nil)
		mockTeamService.On("Get", ctx, profileID).Return(&domain.Team{ID: profileID}, nil)
		mockRepo.On("CountByTeam", ctx, profileID).Return(int64(0), nil)
		mockRepo.On("CreateCredential", ctx, mock.AnythingOfType("*domain.Credential")).Return(errors.New("insert failed"))

		key, raw, err := svc.CreateCredential(ctx, profileID, CreateCredentialRequest{Name: "ops"}, nil, "system", "127.0.0.1", "corr")

		require.ErrorContains(t, err, "failed to create api credential")
		require.Nil(t, key)
		require.Empty(t, raw)
		mockRepo.AssertExpectations(t)
		mockTeamService.AssertExpectations(t)
	})

	t.Run("manager role forces read write scopes without granting feedback by default", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		mockTeamService := new(MockTeamService)
		mockAuditService := new(MockAuditService)
		svc := NewCredentialService(mockRepo, mockTeamService, mockAuditService, nil)
		mockTeamService.On("Get", ctx, profileID).Return(&domain.Team{ID: profileID}, nil)
		mockRepo.On("CreateCredential", ctx, mock.AnythingOfType("*domain.Credential")).Run(func(args mock.Arguments) {
			key := args.Get(1).(*domain.Credential)
			key.ID = uuid.New()
			require.Equal(t, StandardCredentialScopes(), key.Scopes)
			require.Equal(t, CredentialRoleManager, key.Role)
		}).Return(nil)
		mockAuditService.On("CredentialCreated", ctx, mock.AnythingOfType("*string"), mock.AnythingOfType("string"), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

		key, raw, err := svc.CreateCredential(ctx, profileID, CreateCredentialRequest{Name: "manager", Scopes: []string{CredentialScopeRead}, Role: CredentialRoleManager}, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, key)
		require.NotEmpty(t, raw)
		mockRepo.AssertExpectations(t)
		mockTeamService.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})

	t.Run("audit error is non-fatal", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		mockTeamService := new(MockTeamService)
		mockAuditService := new(MockAuditService)
		svc := NewCredentialService(mockRepo, mockTeamService, mockAuditService, nil)
		expiresAt := time.Now().UTC().Add(time.Hour)
		mockTeamService.On("Get", ctx, profileID).Return(&domain.Team{ID: profileID}, nil)
		mockRepo.On("CountByTeam", ctx, profileID).Return(int64(1), nil)
		mockRepo.On("CreateCredential", ctx, mock.AnythingOfType("*domain.Credential")).Run(func(args mock.Arguments) {
			args.Get(1).(*domain.Credential).ID = uuid.New()
		}).Return(nil)
		mockAuditService.On("CredentialCreated", ctx, mock.AnythingOfType("*string"), mock.AnythingOfType("string"), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(errors.New("audit failed"))

		key, raw, err := svc.CreateCredential(ctx, profileID, CreateCredentialRequest{Name: "ops", ExpiresAt: &expiresAt}, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, key)
		require.NotEmpty(t, raw)
		mockRepo.AssertExpectations(t)
		mockTeamService.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})
}

func TestCredentialServiceUpdateNameBranches(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()
	keyID := uuid.New()

	t.Run("invalid name", func(t *testing.T) {
		svc := NewCredentialService(new(MockCredentialRepository), new(MockTeamService), new(MockAuditService), nil)
		key, err := svc.UpdateNameForTeam(ctx, profileID, keyID, " ", nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, key)
	})

	t.Run("same name returns sanitized key without update", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)
		key := &domain.Credential{ID: keyID, TeamID: profileID, Name: "research", KeyHash: "secret"}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil)

		updated, err := svc.UpdateNameForTeam(ctx, profileID, keyID, "research", nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.Empty(t, updated.KeyHash)
		mockRepo.AssertExpectations(t)
	})

	t.Run("not found and rows affected zero", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(nil, nil).Once()
		updated, err := svc.UpdateNameForTeam(ctx, profileID, keyID, "new", nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)

		key := &domain.Credential{ID: keyID, TeamID: profileID, Name: "old"}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateNameForTeam", ctx, profileID, keyID, "new").Return(int64(0), nil).Once()
		updated, err = svc.UpdateNameForTeam(ctx, profileID, keyID, "new", nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repo and audit errors", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		mockAuditService := new(MockAuditService)
		svc := NewCredentialService(mockRepo, new(MockTeamService), mockAuditService, nil)
		key := &domain.Credential{ID: keyID, TeamID: profileID, Name: "old"}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateNameForTeam", ctx, profileID, keyID, "new").Return(int64(0), errors.New("update failed")).Once()
		updated, err := svc.UpdateNameForTeam(ctx, profileID, keyID, "new", nil, "system", "127.0.0.1", "corr")
		require.ErrorContains(t, err, "failed to update credential name")
		require.Nil(t, updated)

		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateNameForTeam", ctx, profileID, keyID, "newer").Return(int64(1), nil).Once()
		mockAuditService.On("Append", ctx, mock.AnythingOfType("service.AuditLogEntry")).Return(errors.New("audit failed")).Once()
		updated, err = svc.UpdateNameForTeam(ctx, profileID, keyID, "newer", nil, "system", "127.0.0.1", "corr")
		require.NoError(t, err)
		require.Equal(t, "newer", updated.Name)
		mockRepo.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})
}

func TestCredentialServiceRotateRevokeDeleteBranches(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()
	keyID := uuid.New()

	t.Run("rotate success with non-fatal invalidation and audit errors", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		mockAuditService := new(MockAuditService)
		mockSessionInvalidator := new(MockCredentialSessionInvalidator)
		svc := NewCredentialService(mockRepo, new(MockTeamService), mockAuditService, mockSessionInvalidator)
		key := &domain.Credential{ID: keyID, TeamID: profileID, Name: "research", KeyHash: "old"}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil)
		mockRepo.On("RotateForTeam", ctx, profileID, keyID, mock.AnythingOfType("string"), mock.AnythingOfType("string"), mock.AnythingOfType("string"), (*time.Time)(nil)).Return(int64(1), nil)
		mockSessionInvalidator.On("InvalidateCredentialSessions", ctx, profileID.String(), keyID.String()).Return(errors.New("redis failed"))
		mockAuditService.On("Append", ctx, mock.MatchedBy(func(entry AuditLogEntry) bool {
			return entry.Operation == "ROTATE_KEY" && entry.EntityType == "api_key"
		})).Return(errors.New("audit failed"))

		updated, raw, err := svc.RotateForTeam(ctx, profileID, keyID, CreateCredentialRequest{}, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotEmpty(t, raw)
		require.Empty(t, updated.KeyHash)
		require.Nil(t, updated.LastUsedAt)
		require.Nil(t, updated.RevokedAt)
		mockRepo.AssertExpectations(t)
		mockSessionInvalidator.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})

	t.Run("rotate not found and rows zero", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(nil, nil).Once()
		updated, raw, err := svc.RotateForTeam(ctx, profileID, keyID, CreateCredentialRequest{}, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)
		require.Empty(t, raw)

		key := &domain.Credential{ID: keyID, TeamID: profileID, Name: "research"}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("RotateForTeam", ctx, profileID, keyID, mock.AnythingOfType("string"), mock.AnythingOfType("string"), mock.AnythingOfType("string"), (*time.Time)(nil)).Return(int64(0), nil).Once()
		updated, raw, err = svc.RotateForTeam(ctx, profileID, keyID, CreateCredentialRequest{}, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)
		require.Empty(t, raw)
		mockRepo.AssertExpectations(t)
	})

	t.Run("revoke conflict rows zero and repo errors", func(t *testing.T) {
		now := time.Now().UTC()
		mockRepo := new(MockCredentialRepository)
		svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)
		revokedKey := &domain.Credential{ID: keyID, TeamID: profileID, RevokedAt: &now}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(revokedKey, nil).Once()
		err := svc.RevokeForTeam(ctx, profileID, keyID, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)

		activeKey := &domain.Credential{ID: keyID, TeamID: profileID}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(activeKey, nil).Once()
		mockRepo.On("RevokeForTeam", ctx, profileID, keyID).Return(int64(0), nil).Once()
		err = svc.RevokeForTeam(ctx, profileID, keyID, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)

		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(activeKey, nil).Once()
		mockRepo.On("RevokeForTeam", ctx, profileID, keyID).Return(int64(0), errors.New("revoke failed")).Once()
		err = svc.RevokeForTeam(ctx, profileID, keyID, nil, "system", "127.0.0.1", "corr")
		require.ErrorContains(t, err, "failed to revoke api credential")
		mockRepo.AssertExpectations(t)
	})

	t.Run("delete not found rows zero and repo error", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(nil, nil).Once()
		err := svc.DeleteForTeam(ctx, profileID, keyID, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)

		key := &domain.Credential{ID: keyID, TeamID: profileID}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("DeleteForTeam", ctx, profileID, keyID).Return(int64(0), nil).Once()
		err = svc.DeleteForTeam(ctx, profileID, keyID, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)

		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("DeleteForTeam", ctx, profileID, keyID).Return(int64(0), errors.New("delete failed")).Once()
		err = svc.DeleteForTeam(ctx, profileID, keyID, nil, "system", "127.0.0.1", "corr")
		require.ErrorContains(t, err, "failed to delete api credential")
		mockRepo.AssertExpectations(t)
	})

	t.Run("get count and repository errors", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(nil, errors.New("get failed")).Once()
		key, err := svc.GetByIDForTeam(ctx, profileID, keyID)
		require.ErrorContains(t, err, "failed to get api credential")
		require.Nil(t, key)

		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(nil, nil).Once()
		key, err = svc.GetByIDForTeam(ctx, profileID, keyID)
		require.Error(t, err)
		require.Nil(t, key)

		mockRepo.On("CountByTeam", ctx, profileID).Return(int64(3), nil).Once()
		total, err := svc.CountByTeam(ctx, profileID)
		require.NoError(t, err)
		require.Equal(t, int64(3), total)
		mockRepo.AssertExpectations(t)
	})
}
