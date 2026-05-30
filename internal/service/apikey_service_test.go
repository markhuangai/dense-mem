package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

// MockAPIKeyRepository is a mock implementation of repository.APIKeyRepository
type MockAPIKeyRepository struct {
	mock.Mock
}

func (m *MockAPIKeyRepository) CreateStandardKey(ctx context.Context, key *domain.APIKey) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

func (m *MockAPIKeyRepository) ListByProfile(ctx context.Context, profileID uuid.UUID, limit, offset int) ([]*domain.APIKey, error) {
	args := m.Called(ctx, profileID, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.APIKey), args.Error(1)
}

func (m *MockAPIKeyRepository) GetActiveByPrefix(ctx context.Context, prefix string) (*domain.APIKey, error) {
	args := m.Called(ctx, prefix)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.APIKey), args.Error(1)
}

func (m *MockAPIKeyRepository) RevokeForProfile(ctx context.Context, profileID, id uuid.UUID) (int64, error) {
	args := m.Called(ctx, profileID, id)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockAPIKeyRepository) RotateForProfile(ctx context.Context, profileID, id uuid.UUID, keyHash, keyPrefix, keySuffix string, expiresAt *time.Time) (int64, error) {
	args := m.Called(ctx, profileID, id, keyHash, keyPrefix, keySuffix, expiresAt)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockAPIKeyRepository) UpdateNameForProfile(ctx context.Context, profileID, id uuid.UUID, name string) (int64, error) {
	args := m.Called(ctx, profileID, id, name)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockAPIKeyRepository) DeleteForProfile(ctx context.Context, profileID, id uuid.UUID) (int64, error) {
	args := m.Called(ctx, profileID, id)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockAPIKeyRepository) GetByIDForProfile(ctx context.Context, profileID, id uuid.UUID) (*domain.APIKey, error) {
	args := m.Called(ctx, profileID, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.APIKey), args.Error(1)
}

func (m *MockAPIKeyRepository) CountByProfile(ctx context.Context, profileID uuid.UUID) (int64, error) {
	args := m.Called(ctx, profileID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockAPIKeyRepository) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

// MockProfileService is a mock implementation of ProfileService
type MockProfileService struct {
	mock.Mock
}

func (m *MockProfileService) Create(ctx context.Context, req CreateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error) {
	args := m.Called(ctx, req, actorKeyID, actorRole, clientIP, correlationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Profile), args.Error(1)
}

func (m *MockProfileService) Get(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Profile), args.Error(1)
}

func (m *MockProfileService) List(ctx context.Context, limit, offset int) ([]*domain.Profile, error) {
	args := m.Called(ctx, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.Profile), args.Error(1)
}

func (m *MockProfileService) Update(ctx context.Context, id uuid.UUID, req UpdateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error) {
	args := m.Called(ctx, id, req, actorKeyID, actorRole, clientIP, correlationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Profile), args.Error(1)
}

func (m *MockProfileService) Delete(ctx context.Context, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, id, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockProfileService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Profile), args.Error(1)
}

func (m *MockProfileService) Count(ctx context.Context) (int64, error) {
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

func (m *MockAuditService) ProfileCreated(ctx context.Context, profileID string, afterPayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, afterPayload, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) ProfileUpdated(ctx context.Context, profileID string, beforePayload, afterPayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, beforePayload, afterPayload, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) ProfileDeleteBlocked(ctx context.Context, profileID string, beforePayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string, reason string) error {
	args := m.Called(ctx, profileID, beforePayload, actorKeyID, actorRole, clientIP, correlationID, reason)
	return args.Error(0)
}

func (m *MockAuditService) ProfileDeleted(ctx context.Context, profileID string, beforePayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, beforePayload, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) APIKeyCreated(ctx context.Context, profileID *string, keyID string, afterPayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, keyID, afterPayload, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) APIKeyRevoked(ctx context.Context, profileID *string, keyID string, beforePayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, keyID, beforePayload, actorKeyID, actorRole, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) AuthFailure(ctx context.Context, profileID *string, entityType, entityID string, metadata map[string]interface{}, clientIP, correlationID string) error {
	args := m.Called(ctx, profileID, entityType, entityID, metadata, clientIP, correlationID)
	return args.Error(0)
}

func (m *MockAuditService) CrossProfileDenied(ctx context.Context, actorProfileID, targetProfileID string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
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

// MockKeySessionInvalidator is a mock implementation of KeySessionInvalidator
type MockKeySessionInvalidator struct {
	mock.Mock
}

func (m *MockKeySessionInvalidator) InvalidateKeySessions(ctx context.Context, profileID, keyID string) error {
	args := m.Called(ctx, profileID, keyID)
	return args.Error(0)
}

// MockProfileStatePurger is a mock implementation of ProfileStatePurger
type MockProfileStatePurger struct {
	mock.Mock
}

func (m *MockProfileStatePurger) PurgeProfileState(ctx context.Context, profileID string) error {
	args := m.Called(ctx, profileID)
	return args.Error(0)
}

// TestGenerateAPIKeyFormat verifies the raw key format
func TestGenerateAPIKeyFormat(t *testing.T) {
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

// TestVerifyAPIKeyCorrect verifies that a valid key passes verification
func TestVerifyAPIKeyCorrect(t *testing.T) {
	rawKey, err := crypto.GenerateRawKey()
	require.NoError(t, err)

	hash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)

	// Verify the correct key passes
	assert.True(t, crypto.VerifyKey(rawKey, hash), "Correct key should verify")
}

// TestVerifyAPIKeyWrongKey verifies that a wrong key fails verification
func TestVerifyAPIKeyWrongKey(t *testing.T) {
	rawKey1, err := crypto.GenerateRawKey()
	require.NoError(t, err)

	rawKey2, err := crypto.GenerateRawKey()
	require.NoError(t, err)

	hash, err := crypto.HashKey(rawKey1)
	require.NoError(t, err)

	// Verify wrong key fails
	assert.False(t, crypto.VerifyKey(rawKey2, hash), "Wrong key should not verify")
}

// TestVerifyAPIKeyTampered verifies that a tampered hash fails verification
func TestVerifyAPIKeyTampered(t *testing.T) {
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

// TestAPIKeyServiceCreate verifies that raw key is returned once and hash is stored
func TestAPIKeyServiceCreate(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()

	mockRepo := new(MockAPIKeyRepository)
	mockProfileService := new(MockProfileService)
	mockAuditService := new(MockAuditService)
	mockSessionInvalidator := new(MockKeySessionInvalidator)
	mockStatePurger := new(MockProfileStatePurger)

	service := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, mockSessionInvalidator, mockStatePurger)

	// Setup expectations
	mockProfileService.On("Get", ctx, profileID).Return(&domain.Profile{ID: profileID}, nil)
	mockRepo.On("CreateStandardKey", ctx, mock.AnythingOfType("*domain.APIKey")).Run(func(args mock.Arguments) {
		key := args.Get(1).(*domain.APIKey)
		assert.Equal(t, []string{"read", "write"}, key.Scopes)
		key.ID = uuid.New() // Simulate DB assigning ID
	}).Return(nil)
	mockAuditService.On("APIKeyCreated", ctx, mock.AnythingOfType("*string"), mock.AnythingOfType("string"), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	req := CreateAPIKeyRequest{
		RateLimit: 100,
	}

	key, rawKey, err := service.CreateStandardKey(ctx, profileID, req, nil, "system", "127.0.0.1", "test-correlation")

	require.NoError(t, err)
	assert.NotNil(t, key)
	assert.NotEmpty(t, rawKey)
	assert.True(t, strings.HasPrefix(rawKey, "dm_default-prof_"), "Raw key should include the profile slug")
	assert.Empty(t, key.KeyHash, "KeyHash should not be returned")
	assert.Equal(t, crypto.GetKeySuffix(rawKey), key.KeySuffix, "KeySuffix should be last 6 raw key characters")
	assert.Equal(t, []string{"read", "write"}, key.Scopes)
	assert.Equal(t, "default profile", key.Label)
	assert.Equal(t, "default profile", key.Name)

	// Verify the raw key is not logged or stored in the response
	mockRepo.AssertExpectations(t)
	mockProfileService.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}

func TestAPIKeyServiceCreateReadOnlyKey(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()

	mockRepo := new(MockAPIKeyRepository)
	mockProfileService := new(MockProfileService)
	mockAuditService := new(MockAuditService)
	mockSessionInvalidator := new(MockKeySessionInvalidator)
	mockStatePurger := new(MockProfileStatePurger)

	service := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, mockSessionInvalidator, mockStatePurger)

	mockProfileService.On("Get", ctx, profileID).Return(&domain.Profile{ID: profileID}, nil)
	mockRepo.On("CreateStandardKey", ctx, mock.AnythingOfType("*domain.APIKey")).Run(func(args mock.Arguments) {
		key := args.Get(1).(*domain.APIKey)
		assert.Equal(t, []string{"read"}, key.Scopes)
		key.ID = uuid.New()
	}).Return(nil)
	mockAuditService.On("APIKeyCreated", ctx, mock.AnythingOfType("*string"), mock.AnythingOfType("string"), mock.MatchedBy(func(payload map[string]interface{}) bool {
		scopes, ok := payload["scopes"].([]string)
		return ok && assert.ObjectsAreEqual([]string{"read"}, scopes)
	}), mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	key, rawKey, err := service.CreateStandardKey(ctx, profileID, CreateAPIKeyRequest{
		Name:      "automation",
		Scopes:    []string{"read"},
		RateLimit: 100,
	}, nil, "system", "127.0.0.1", "test-correlation")

	require.NoError(t, err)
	require.NotNil(t, key)
	assert.NotEmpty(t, rawKey)
	assert.Equal(t, []string{"read"}, key.Scopes)
	assert.Empty(t, key.KeyHash)
	mockRepo.AssertExpectations(t)
	mockProfileService.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}

func TestNormalizeAPIKeyScopesRejectsUnsupportedSets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scopes []string
	}{
		{name: "write without read", scopes: []string{"write"}},
		{name: "unknown scope", scopes: []string{"read", "admin"}},
		{name: "blank scope", scopes: []string{"read", " "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := NormalizeAPIKeyScopes(tc.scopes)
			require.Nil(t, normalized)
			require.Error(t, err)
			var apiErr *httperr.APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, httperr.VALIDATION_ERROR, apiErr.Code)
		})
	}
}

// TestAPIKeyServiceListNeverReturnsHash verifies that list never returns key_hash.
func TestAPIKeyServiceListNeverReturnsHash(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()

	mockRepo := new(MockAPIKeyRepository)
	mockProfileService := new(MockProfileService)
	mockAuditService := new(MockAuditService)
	mockSessionInvalidator := new(MockKeySessionInvalidator)
	mockStatePurger := new(MockProfileStatePurger)

	service := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, mockSessionInvalidator, mockStatePurger)

	// Setup expectations for list
	keyID := uuid.New()
	now := time.Now()
	keys := []*domain.APIKey{
		{
			ID:        keyID,
			ProfileID: profileID,
			Label:     "test-key",
			Scopes:    []string{"read"},
			CreatedAt: now,
		},
	}
	mockRepo.On("ListByProfile", ctx, profileID, 20, 0).Return(keys, nil)

	// Test list
	result, err := service.ListByProfile(ctx, profileID, 20, 0)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Empty(t, result[0].KeyHash, "List should not return key_hash")

	mockRepo.AssertExpectations(t)
}

func TestAPIKeyServiceUpdateNameForProfile(t *testing.T) {
	ctx := context.Background()
	keyID := uuid.New()
	profileID := uuid.New()

	mockRepo := new(MockAPIKeyRepository)
	mockProfileService := new(MockProfileService)
	mockAuditService := new(MockAuditService)
	mockSessionInvalidator := new(MockKeySessionInvalidator)
	mockStatePurger := new(MockProfileStatePurger)

	service := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, mockSessionInvalidator, mockStatePurger)

	key := &domain.APIKey{
		ID:        keyID,
		ProfileID: profileID,
		TeamID:    profileID,
		Label:     "default profile",
		Name:      "default profile",
		KeyHash:   "secret-hash",
		CreatedAt: time.Now(),
	}

	mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil)
	mockRepo.On("UpdateNameForProfile", ctx, profileID, keyID, "research profile").Return(int64(1), nil)
	mockAuditService.On("Append", ctx, mock.MatchedBy(func(entry AuditLogEntry) bool {
		return entry.Operation == "UPDATE" &&
			entry.EntityType == "team_profile" &&
			entry.EntityID == keyID.String() &&
			entry.ProfileID != nil &&
			*entry.ProfileID == profileID.String()
	})).Return(nil)

	updated, err := service.UpdateNameForProfile(ctx, profileID, keyID, " research profile ", nil, "system", "127.0.0.1", "test-correlation")

	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, "research profile", updated.Name)
	assert.Equal(t, "research profile", updated.Label)
	assert.Empty(t, updated.KeyHash, "KeyHash should not be returned")
	mockRepo.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}

func TestAPIKeyServiceUpdateNameForProfileDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "pgx", err: &pgconn.PgError{Code: "23505"}},
		{name: "pq", err: &pq.Error{Code: "23505"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			keyID := uuid.New()
			profileID := uuid.New()

			mockRepo := new(MockAPIKeyRepository)
			mockProfileService := new(MockProfileService)
			mockAuditService := new(MockAuditService)
			mockSessionInvalidator := new(MockKeySessionInvalidator)
			mockStatePurger := new(MockProfileStatePurger)

			service := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, mockSessionInvalidator, mockStatePurger)

			key := &domain.APIKey{
				ID:        keyID,
				ProfileID: profileID,
				TeamID:    profileID,
				Label:     "default profile",
				Name:      "default profile",
				CreatedAt: time.Now(),
			}

			mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil)
			mockRepo.On("UpdateNameForProfile", ctx, profileID, keyID, "existing profile").Return(int64(0), tc.err)

			updated, err := service.UpdateNameForProfile(ctx, profileID, keyID, "existing profile", nil, "system", "127.0.0.1", "test-correlation")

			require.Nil(t, updated)
			require.Error(t, err)
			var apiErr *httperr.APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, httperr.CONFLICT, apiErr.Code)
			mockRepo.AssertExpectations(t)
		})
	}
}

// TestAPIKeyServiceDeleteForProfile_CallsSessionInvalidator proves that DeleteForProfile
// hard-deletes the key and invalidates sessions.
func TestAPIKeyServiceDeleteForProfile_CallsSessionInvalidator(t *testing.T) {
	ctx := context.Background()
	keyID := uuid.New()
	profileID := uuid.New()

	mockRepo := new(MockAPIKeyRepository)
	mockProfileService := new(MockProfileService)
	mockAuditService := new(MockAuditService)
	mockSessionInvalidator := new(MockKeySessionInvalidator)
	mockStatePurger := new(MockProfileStatePurger)

	service := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, mockSessionInvalidator, mockStatePurger)

	now := time.Now()
	key := &domain.APIKey{
		ID:        keyID,
		ProfileID: profileID,
		RateLimit: 100,
		CreatedAt: now,
	}

	mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil)
	mockRepo.On("DeleteForProfile", ctx, profileID, keyID).Return(int64(1), nil)
	mockSessionInvalidator.On("InvalidateKeySessions", ctx, profileID.String(), keyID.String()).Return(nil)
	mockAuditService.On("Append", ctx, mock.MatchedBy(func(entry AuditLogEntry) bool {
		return entry.Operation == "DELETE" &&
			entry.EntityType == "api_key" &&
			entry.EntityID == keyID.String() &&
			entry.ProfileID != nil &&
			*entry.ProfileID == profileID.String()
	})).Return(nil)

	err := service.DeleteForProfile(ctx, profileID, keyID, nil, "system", "127.0.0.1", "test-correlation")

	require.NoError(t, err)
	mockSessionInvalidator.AssertCalled(t, "InvalidateKeySessions", ctx, profileID.String(), keyID.String())
	mockRepo.AssertExpectations(t)
	mockSessionInvalidator.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}

// TestAPIKeyServiceRevokeForProfile_CallsSessionInvalidator proves that RevokeForProfile
// invokes InvalidateKeySessions on the injected session invalidator (AC-03).
func TestAPIKeyServiceRevokeForProfile_CallsSessionInvalidator(t *testing.T) {
	ctx := context.Background()
	keyID := uuid.New()
	profileID := uuid.New()

	mockRepo := new(MockAPIKeyRepository)
	mockProfileService := new(MockProfileService)
	mockAuditService := new(MockAuditService)
	mockSessionInvalidator := new(MockKeySessionInvalidator)
	mockStatePurger := new(MockProfileStatePurger)

	service := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, mockSessionInvalidator, mockStatePurger)

	now := time.Now()
	key := &domain.APIKey{
		ID:        keyID,
		ProfileID: profileID,
		Label:     "test-key",
		Scopes:    []string{"read"},
		CreatedAt: now,
		RevokedAt: nil,
	}

	// GetByIDForProfile is called to verify ownership
	mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil)
	// RevokeForProfile returns 1 row affected
	mockRepo.On("RevokeForProfile", ctx, profileID, keyID).Return(int64(1), nil)
	// InvalidateKeySessions must be called
	mockSessionInvalidator.On("InvalidateKeySessions", ctx, profileID.String(), keyID.String()).Return(nil)
	mockAuditService.On("APIKeyRevoked", ctx, mock.AnythingOfType("*string"), keyID.String(), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	err := service.RevokeForProfile(ctx, profileID, keyID, nil, "system", "127.0.0.1", "test-correlation")

	require.NoError(t, err)
	mockSessionInvalidator.AssertCalled(t, "InvalidateKeySessions", ctx, profileID.String(), keyID.String())
	mockRepo.AssertExpectations(t)
	mockSessionInvalidator.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}

// TestAPIKeyServiceRevokeForProfile_NilInvalidatorIsSafe proves that a nil session invalidator
// does not panic and the revoke still succeeds (AC-E2).
func TestAPIKeyServiceRevokeForProfile_NilInvalidatorIsSafe(t *testing.T) {
	ctx := context.Background()
	keyID := uuid.New()
	profileID := uuid.New()

	mockRepo := new(MockAPIKeyRepository)
	mockProfileService := new(MockProfileService)
	mockAuditService := new(MockAuditService)
	mockStatePurger := new(MockProfileStatePurger)

	// sessionInvalidator is nil — this must not panic
	service := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, nil, mockStatePurger)

	now := time.Now()
	key := &domain.APIKey{
		ID:        keyID,
		ProfileID: profileID,
		Label:     "test-key",
		Scopes:    []string{"read"},
		CreatedAt: now,
		RevokedAt: nil,
	}

	mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil)
	mockRepo.On("RevokeForProfile", ctx, profileID, keyID).Return(int64(1), nil)
	mockAuditService.On("APIKeyRevoked", ctx, mock.AnythingOfType("*string"), keyID.String(), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	err := service.RevokeForProfile(ctx, profileID, keyID, nil, "system", "127.0.0.1", "test-correlation")

	require.NoError(t, err)
	mockRepo.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}

func TestAPIKeyServiceScopeNameAndConstructorHelpers(t *testing.T) {
	scopes := StandardAPIKeyScopes()
	scopes[0] = "mutated"
	assert.Equal(t, []string{"read", "write"}, StandardAPIKeyScopes())

	normalized, err := NormalizeAPIKeyScopes([]string{" WRITE ", "Read"})
	require.NoError(t, err)
	assert.Equal(t, []string{"read", "write"}, normalized)

	normalized, err = NormalizeAPIKeyScopes(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"read", "write"}, normalized)

	name, err := normalizeTeamProfileName(" research ")
	require.NoError(t, err)
	assert.Equal(t, "research", name)

	_, err = normalizeTeamProfileName(" ")
	require.Error(t, err)
	_, err = normalizeTeamProfileName(strings.Repeat("x", 101))
	require.Error(t, err)
	assert.Nil(t, teamProfileNameConflict(errors.New("not unique"), "name"))

	svc := NewAPIKeyServiceWithLogger(nil, nil, nil, nil, nil, nil)
	require.Nil(t, svc.logger)
	svc.logAuditError(errors.New("audit failed"), "CREATE", "key-1", "corr")
}

func TestAPIKeyServiceCreateStandardKeyErrorAndAuditBranches(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()

	t.Run("profile lookup error", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		mockProfileService := new(MockProfileService)
		mockAuditService := new(MockAuditService)
		svc := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, nil, nil)
		mockProfileService.On("Get", ctx, profileID).Return(nil, errors.New("profile failed"))

		key, raw, err := svc.CreateStandardKey(ctx, profileID, CreateAPIKeyRequest{}, nil, "system", "127.0.0.1", "corr")

		require.ErrorContains(t, err, "failed to verify profile")
		require.Nil(t, key)
		require.Empty(t, raw)
		mockProfileService.AssertExpectations(t)
	})

	t.Run("invalid scopes", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		mockProfileService := new(MockProfileService)
		mockAuditService := new(MockAuditService)
		svc := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, nil, nil)
		mockProfileService.On("Get", ctx, profileID).Return(&domain.Profile{ID: profileID}, nil)

		key, raw, err := svc.CreateStandardKey(ctx, profileID, CreateAPIKeyRequest{Scopes: []string{"write"}}, nil, "system", "127.0.0.1", "corr")

		require.Error(t, err)
		require.Nil(t, key)
		require.Empty(t, raw)
		mockProfileService.AssertExpectations(t)
	})

	t.Run("repo error", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		mockProfileService := new(MockProfileService)
		mockAuditService := new(MockAuditService)
		svc := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, nil, nil)
		mockProfileService.On("Get", ctx, profileID).Return(&domain.Profile{ID: profileID}, nil)
		mockRepo.On("CreateStandardKey", ctx, mock.AnythingOfType("*domain.APIKey")).Return(errors.New("insert failed"))

		key, raw, err := svc.CreateStandardKey(ctx, profileID, CreateAPIKeyRequest{Name: "ops"}, nil, "system", "127.0.0.1", "corr")

		require.ErrorContains(t, err, "failed to create api key")
		require.Nil(t, key)
		require.Empty(t, raw)
		mockRepo.AssertExpectations(t)
		mockProfileService.AssertExpectations(t)
	})

	t.Run("audit error is non-fatal", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		mockProfileService := new(MockProfileService)
		mockAuditService := new(MockAuditService)
		svc := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, nil, nil)
		expiresAt := time.Now().UTC().Add(time.Hour)
		mockProfileService.On("Get", ctx, profileID).Return(&domain.Profile{ID: profileID}, nil)
		mockRepo.On("CreateStandardKey", ctx, mock.AnythingOfType("*domain.APIKey")).Run(func(args mock.Arguments) {
			args.Get(1).(*domain.APIKey).ID = uuid.New()
		}).Return(nil)
		mockAuditService.On("APIKeyCreated", ctx, mock.AnythingOfType("*string"), mock.AnythingOfType("string"), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(errors.New("audit failed"))

		key, raw, err := svc.CreateStandardKey(ctx, profileID, CreateAPIKeyRequest{Name: "ops", ExpiresAt: &expiresAt}, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, key)
		require.NotEmpty(t, raw)
		mockRepo.AssertExpectations(t)
		mockProfileService.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})
}

func TestAPIKeyServiceUpdateNameBranches(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()
	keyID := uuid.New()

	t.Run("invalid name", func(t *testing.T) {
		svc := NewAPIKeyService(new(MockAPIKeyRepository), new(MockProfileService), new(MockAuditService), nil, nil)
		key, err := svc.UpdateNameForProfile(ctx, profileID, keyID, " ", nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, key)
	})

	t.Run("same name returns sanitized key without update", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), new(MockAuditService), nil, nil)
		key := &domain.APIKey{ID: keyID, ProfileID: profileID, TeamID: profileID, Name: "research", Label: "research", KeyHash: "secret"}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil)

		updated, err := svc.UpdateNameForProfile(ctx, profileID, keyID, "research", nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.Empty(t, updated.KeyHash)
		mockRepo.AssertExpectations(t)
	})

	t.Run("not found and rows affected zero", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), new(MockAuditService), nil, nil)
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(nil, nil).Once()
		updated, err := svc.UpdateNameForProfile(ctx, profileID, keyID, "new", nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)

		key := &domain.APIKey{ID: keyID, ProfileID: profileID, TeamID: profileID, Name: "old", Label: "old"}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateNameForProfile", ctx, profileID, keyID, "new").Return(int64(0), nil).Once()
		updated, err = svc.UpdateNameForProfile(ctx, profileID, keyID, "new", nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repo and audit errors", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		mockAuditService := new(MockAuditService)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), mockAuditService, nil, nil)
		key := &domain.APIKey{ID: keyID, ProfileID: profileID, TeamID: profileID, Name: "old", Label: "old"}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateNameForProfile", ctx, profileID, keyID, "new").Return(int64(0), errors.New("update failed")).Once()
		updated, err := svc.UpdateNameForProfile(ctx, profileID, keyID, "new", nil, "system", "127.0.0.1", "corr")
		require.ErrorContains(t, err, "failed to update team profile name")
		require.Nil(t, updated)

		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateNameForProfile", ctx, profileID, keyID, "newer").Return(int64(1), nil).Once()
		mockAuditService.On("Append", ctx, mock.AnythingOfType("service.AuditLogEntry")).Return(errors.New("audit failed")).Once()
		updated, err = svc.UpdateNameForProfile(ctx, profileID, keyID, "newer", nil, "system", "127.0.0.1", "corr")
		require.NoError(t, err)
		require.Equal(t, "newer", updated.Name)
		mockRepo.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})
}

func TestAPIKeyServiceRotateRevokeDeleteBranches(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()
	keyID := uuid.New()

	t.Run("rotate success with non-fatal invalidation and audit errors", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		mockAuditService := new(MockAuditService)
		mockSessionInvalidator := new(MockKeySessionInvalidator)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), mockAuditService, mockSessionInvalidator, nil)
		key := &domain.APIKey{ID: keyID, ProfileID: profileID, TeamID: profileID, Name: "research", Label: "research", KeyHash: "old"}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil)
		mockRepo.On("RotateForProfile", ctx, profileID, keyID, mock.AnythingOfType("string"), mock.AnythingOfType("string"), mock.AnythingOfType("string"), (*time.Time)(nil)).Return(int64(1), nil)
		mockSessionInvalidator.On("InvalidateKeySessions", ctx, profileID.String(), keyID.String()).Return(errors.New("redis failed"))
		mockAuditService.On("Append", ctx, mock.AnythingOfType("service.AuditLogEntry")).Return(errors.New("audit failed"))

		updated, raw, err := svc.RotateForProfile(ctx, profileID, keyID, CreateAPIKeyRequest{}, nil, "system", "127.0.0.1", "corr")

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
		mockRepo := new(MockAPIKeyRepository)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), new(MockAuditService), nil, nil)
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(nil, nil).Once()
		updated, raw, err := svc.RotateForProfile(ctx, profileID, keyID, CreateAPIKeyRequest{}, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)
		require.Empty(t, raw)

		key := &domain.APIKey{ID: keyID, ProfileID: profileID, TeamID: profileID, Name: "research", Label: "research"}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("RotateForProfile", ctx, profileID, keyID, mock.AnythingOfType("string"), mock.AnythingOfType("string"), mock.AnythingOfType("string"), (*time.Time)(nil)).Return(int64(0), nil).Once()
		updated, raw, err = svc.RotateForProfile(ctx, profileID, keyID, CreateAPIKeyRequest{}, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)
		require.Empty(t, raw)
		mockRepo.AssertExpectations(t)
	})

	t.Run("revoke conflict rows zero and repo errors", func(t *testing.T) {
		now := time.Now().UTC()
		mockRepo := new(MockAPIKeyRepository)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), new(MockAuditService), nil, nil)
		revokedKey := &domain.APIKey{ID: keyID, ProfileID: profileID, RevokedAt: &now}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(revokedKey, nil).Once()
		err := svc.RevokeForProfile(ctx, profileID, keyID, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)

		activeKey := &domain.APIKey{ID: keyID, ProfileID: profileID}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(activeKey, nil).Once()
		mockRepo.On("RevokeForProfile", ctx, profileID, keyID).Return(int64(0), nil).Once()
		err = svc.RevokeForProfile(ctx, profileID, keyID, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)

		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(activeKey, nil).Once()
		mockRepo.On("RevokeForProfile", ctx, profileID, keyID).Return(int64(0), errors.New("revoke failed")).Once()
		err = svc.RevokeForProfile(ctx, profileID, keyID, nil, "system", "127.0.0.1", "corr")
		require.ErrorContains(t, err, "failed to revoke api key")
		mockRepo.AssertExpectations(t)
	})

	t.Run("delete not found rows zero and repo error", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), new(MockAuditService), nil, nil)
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(nil, nil).Once()
		err := svc.DeleteForProfile(ctx, profileID, keyID, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)

		key := &domain.APIKey{ID: keyID, ProfileID: profileID}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("DeleteForProfile", ctx, profileID, keyID).Return(int64(0), nil).Once()
		err = svc.DeleteForProfile(ctx, profileID, keyID, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)

		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("DeleteForProfile", ctx, profileID, keyID).Return(int64(0), errors.New("delete failed")).Once()
		err = svc.DeleteForProfile(ctx, profileID, keyID, nil, "system", "127.0.0.1", "corr")
		require.ErrorContains(t, err, "failed to delete api key")
		mockRepo.AssertExpectations(t)
	})

	t.Run("get count and repository errors", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), new(MockAuditService), nil, nil)
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(nil, errors.New("get failed")).Once()
		key, err := svc.GetByIDForProfile(ctx, profileID, keyID)
		require.ErrorContains(t, err, "failed to get api key")
		require.Nil(t, key)

		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(nil, nil).Once()
		key, err = svc.GetByIDForProfile(ctx, profileID, keyID)
		require.Error(t, err)
		require.Nil(t, key)

		mockRepo.On("CountByProfile", ctx, profileID).Return(int64(3), nil).Once()
		total, err := svc.CountByProfile(ctx, profileID)
		require.NoError(t, err)
		require.Equal(t, int64(3), total)
		mockRepo.AssertExpectations(t)
	})
}
