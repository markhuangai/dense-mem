package redis

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKeyBuilder_RateLimit_Format tests that RateLimit produces the correct key format.
func TestKeyBuilder_RateLimit_Format(t *testing.T) {
	kb := NewKeyBuilder()
	key, err := kb.RateLimit("profile123", "user@example.com")
	require.NoError(t, err)
	assert.Equal(t, "profile:profile123:ratelimit:user@example.com", key)
}

// TestKeyBuilder_Stream_Format tests that Stream produces the correct key format.
func TestKeyBuilder_Stream_Format(t *testing.T) {
	kb := NewKeyBuilder()
	key, err := kb.Stream("profile999", "stream-id-123")
	require.NoError(t, err)
	assert.Equal(t, "profile:profile999:stream:stream-id-123", key)
}

// TestKeyBuilder_EmptyProfileID_Rejects tests that empty profileID is rejected.
func TestKeyBuilder_EmptyProfileID_Rejects(t *testing.T) {
	kb := NewKeyBuilder()

	_, err := kb.RateLimit("", "identifier")
	assert.ErrorIs(t, err, ErrEmptyProfileID)

	_, err = kb.Stream("", "identifier")
	assert.ErrorIs(t, err, ErrEmptyProfileID)
}

// TestKeyBuilder_InvalidCategory_Rejects tests that invalid categories are rejected.
func TestKeyBuilder_InvalidCategory_Rejects(t *testing.T) {
	kb := NewKeyBuilder()
	_, err := kb.buildKey("profile123", "invalid_category", "identifier")
	assert.ErrorIs(t, err, ErrInvalidCategory)
}

// TestKeyBuilder_EmptyIdentifier_Rejects tests that empty identifier is rejected.
func TestKeyBuilder_EmptyIdentifier_Rejects(t *testing.T) {
	kb := NewKeyBuilder()
	_, err := kb.RateLimit("profile123", "")
	assert.ErrorIs(t, err, ErrEmptyIdentifier)
}

// TestRedisHealth_Ping tests that the Redis client can ping the server.
// This test requires a running Redis instance and the integration build tag.
func TestRedisHealth_Ping(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a mock config for testing
	cfg := &mockConfig{
		redisAddr:     "localhost:6379",
		redisPassword: "",
		redisDB:       0,
	}

	client, err := NewClient(ctx, cfg)
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	defer client.Close()

	err = client.Ping(ctx)
	require.NoError(t, err)
}

// TestRedisClient_NoRawKeyExposure tests that raw unprefixed keys are not exposed.
func TestRedisClient_NoRawKeyExposure(t *testing.T) {
	kb := NewKeyBuilder()

	// Verify that KeyBuilderInterface does not expose any raw key methods
	// All methods return prefixed keys
	var _ KeyBuilderInterface = kb

	// Verify that all methods return properly prefixed keys
	// and that there's no way to bypass the prefix
	key, err := kb.RateLimit("profile1", "id1")
	require.NoError(t, err)
	assert.Contains(t, key, "profile:profile1:")
	assert.Contains(t, key, ":ratelimit:")

	key, err = kb.Stream("profile1", "id1")
	require.NoError(t, err)
	assert.Contains(t, key, "profile:profile1:")
	assert.Contains(t, key, ":stream:")
}

func TestOptionsFromConfig_TLS(t *testing.T) {
	plain := optionsFromConfig(&mockConfig{
		redisAddr:     "localhost:6379",
		redisPassword: "secret",
		redisDB:       2,
	})
	require.Nil(t, plain.TLSConfig)
	assert.Equal(t, "localhost:6379", plain.Addr)
	assert.Equal(t, "secret", plain.Password)
	assert.Equal(t, 2, plain.DB)

	enabled := optionsFromConfig(&mockConfig{
		redisAddr:       "redis.example.com:6379",
		redisTLSEnabled: true,
	})
	require.NotNil(t, enabled.TLSConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), enabled.TLSConfig.MinVersion)
	assert.False(t, enabled.TLSConfig.InsecureSkipVerify)
}

// mockConfig implements ConfigProvider for testing
type mockConfig struct {
	redisAddr       string
	redisPassword   string
	redisDB         int
	redisTLSEnabled bool
}

func (m *mockConfig) GetRedisAddr() string     { return m.redisAddr }
func (m *mockConfig) GetRedisPassword() string { return m.redisPassword }
func (m *mockConfig) GetRedisDB() int          { return m.redisDB }
func (m *mockConfig) GetRedisTLSEnabled() bool { return m.redisTLSEnabled }
