package compose_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerComposeExample_RedisBehindProfile(t *testing.T) {
	b, err := os.ReadFile("../../docker-compose.example.yml")
	require.NoError(t, err)

	text := string(b)
	assert.Contains(t, text, `profiles: ["redis"]`)
	assert.NotContains(t, text, "redis:\n        condition: service_healthy")
}

func TestDockerCompose_DefaultCompose_MarksRedisOptional(t *testing.T) {
	b, err := os.ReadFile("../../docker-compose.yml")
	require.NoError(t, err)

	text := string(b)
	assert.Contains(t, text, "optional for single-node")
	assert.NotContains(t, text, "redis:\n        condition: service_healthy")
}

func TestDockerComposeBaseExample_LocalOnly(t *testing.T) {
	text := readExample(t, "docker-compose.base.yml")

	assert.Contains(t, text, "127.0.0.1:${DENSE_MEM_PORT:-8080}:8080")
	assert.Contains(t, text, "127.0.0.1:${CONTROL_PORTAL_PORT:-8090}:8090")
	assert.NotContains(t, text, "traefik")
	assert.NotContains(t, text, "profiles:")
}

func TestDockerComposeExpertExample_HasOptionalProfiles(t *testing.T) {
	text := readExample(t, "docker-compose.expert.yml")

	assert.Contains(t, text, `profiles: ["traefik"]`)
	assert.Contains(t, text, `profiles: ["redis"]`)
	assert.NotContains(t, text, "redis:\n        condition: service_healthy")
}

func readExample(t *testing.T, name string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("../../examples", name))
	require.NoError(t, err)
	return string(b)
}
