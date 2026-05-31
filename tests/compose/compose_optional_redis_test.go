package compose_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerComposeBaseExample_LocalOnly(t *testing.T) {
	text := readExample(t, "docker-compose.base.yml")

	assert.Contains(t, text, "127.0.0.1:${DENSE_MEM_PORT:-8080}:8080")
	assert.Contains(t, text, "127.0.0.1:${CONTROL_PORTAL_PORT:-8090}:8090")
	assert.NotContains(t, text, "\n      HTTP_ADDR:")
	assert.NotContains(t, text, "traefik")
	assert.NotContains(t, text, "profiles:")
}

func TestDockerComposeExpertExample_HasOptionalProfiles(t *testing.T) {
	text := readExample(t, "docker-compose.expert.yml")

	assert.Contains(t, text, `profiles: ["traefik"]`)
	assert.Contains(t, text, `profiles: ["redis"]`)
	assert.Contains(t, text, "Redis")
	assert.Contains(t, text, "--entrypoints.websecure.http3")
	assert.Contains(t, text, "${TRAEFIK_HTTPS_PORT:-443}:443/udp")
	assert.NotContains(t, text, "\n      HTTP_ADDR:")
	assert.NotContains(t, text, "redis:\n        condition: service_healthy")
}

func TestDockerComposeDemoExample_UsesDemoImageAndRedis(t *testing.T) {
	text := readExample(t, "docker-compose.demo.yml")

	assert.Contains(t, text, "ghcr.io/markhuangai/dense-mem:demo")
	assert.Contains(t, text, "DEMO_PUBLIC_BASE_URL")
	assert.Contains(t, text, "REDIS_ADDR: redis:6379")
	assert.Contains(t, text, "redis:\n        condition: service_healthy")
	assert.Contains(t, text, "24-hour demo service")
	assert.NotContains(t, text, "\n      HTTP_ADDR:")
	assert.NotContains(t, text, "CONTROL_PORTAL_TOKEN")
}

func readExample(t *testing.T, name string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("../../examples", name))
	require.NoError(t, err)
	return string(b)
}
