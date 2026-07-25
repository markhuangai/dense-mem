package serverapp

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildBackendBundle_NoRedis_UsesInMemory(t *testing.T) {
	bundle, err := buildBackendBundle(context.Background(), config.Config{
		RedisAddr:               "",
		SSEMaxConcurrentStreams: 10,
	})
	require.NoError(t, err)
	require.NotNil(t, bundle)

	assert.True(t, bundle.degraded)
	assert.Equal(t, "in-memory backend: no cross-instance rate limiting or session cleanup", bundle.reason)
	assert.NotNil(t, bundle.cleanupRepo)
	assert.NotNil(t, bundle.rateLimitService)
	assert.NotNil(t, bundle.concurrencyLimiter)
	assert.NotNil(t, bundle.streamCleanupRepo)
}

func TestBuildBackendBundle_DistributedRequiredWithoutRedisFails(t *testing.T) {
	bundle, err := buildBackendBundle(context.Background(), config.Config{
		RedisAddr:                       "",
		SSEMaxConcurrentStreams:         10,
		DistributedCoordinationRequired: true,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REDIS_ADDR")
	assert.Nil(t, bundle)
}

func TestLogInMemoryModeWarning_ContainsLockedWords(t *testing.T) {
	var buf bytes.Buffer
	logger := observability.NewWithHandler(slog.NewJSONHandler(&buf, nil))

	logInMemoryModeWarning(logger, true, "in-memory backend: no cross-instance rate limiting or session cleanup")
	assert.Contains(t, buf.String(), "in-memory")
	assert.Contains(t, buf.String(), "multi-instance")
}
