package compose_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDockerComposeBaseExample_LocalOnly(t *testing.T) {
	text := readExample(t, "docker-compose.base.yml")
	compose := readComposeExample(t, "docker-compose.base.yml")
	server := requireComposeService(t, compose, "server")

	assert.Contains(t, text, "127.0.0.1:${DENSE_MEM_PORT:-8080}:8080")
	assert.Contains(t, text, "127.0.0.1:${CONTROL_PORTAL_PORT:-8090}:8090")
	assert.NotContains(t, text, "\n      HTTP_ADDR:")
	assert.NotContains(t, text, "traefik")
	assert.NotContains(t, compose.Services, "redis")
	assert.NotContains(t, compose.Services, "migrate")
	assert.NotContains(t, compose.Services, removedGraphServiceName())
	assert.NotContains(t, server.Environment, removedGraphEnvKey())
	assert.NotContains(t, server.Environment, "AI_REVIEWER_MODEL")
	assert.Contains(t, server.Environment["AI_VERIFIER_MODEL"], "AI_VERIFIER_MODEL must be set")
}

func TestDockerComposeDemoPinsVersionedImage(t *testing.T) {
	text := readExample(t, "docker-compose.demo.yml")
	compose := readComposeExample(t, "docker-compose.demo.yml")
	demo := requireComposeService(t, compose, "demo")

	assert.Equal(t, "ghcr.io/markhuangai/dense-mem:demo-v2.4.1", demo.Image)
	assert.NotContains(t, text, "DENSE_MEM_DEMO_IMAGE")
	assert.NotContains(t, text, "DENSE_MEM_DEMO_VERSION")
	assert.NotContains(t, text, "DENSE_MEM_DEMO_REPOSITORY")
	assert.NotContains(t, text, "dense-mem:demo\n")
}

func TestDockerComposeExpertExample_HasOptionalProfiles(t *testing.T) {
	text := readExample(t, "docker-compose.expert.yml")
	compose := readComposeExample(t, "docker-compose.expert.yml")
	server := requireComposeService(t, compose, "server")

	assert.ElementsMatch(t, []string{"traefik"}, requireComposeService(t, compose, "traefik").Profiles)
	assert.ElementsMatch(t, []string{"redis"}, requireComposeService(t, compose, "redis").Profiles)
	assert.NotContains(t, compose.Services, removedGraphServiceName())
	assert.Contains(t, text, "Redis")
	assert.Contains(t, text, "--entrypoints.websecure.http3")
	assert.Contains(t, text, "${TRAEFIK_HTTPS_PORT:-443}:443/udp")
	assert.NotContains(t, text, "\n      HTTP_ADDR:")
	assert.NotContains(t, server.Environment, removedGraphEnvKey())
	assert.NotContains(t, server.Environment, "AI_REVIEWER_MODEL")
	assert.Contains(t, server.Environment["AI_VERIFIER_MODEL"], "AI_VERIFIER_MODEL must be set")
	assert.NotContains(t, server.DependsOn, "redis")
}

func TestDockerComposeExpertExample_PreservesUiPathForCustomDomain(t *testing.T) {
	text := strings.ToLower(readExample(t, "docker-compose.expert.yml"))

	assert.Contains(t, text, "traefik.http.routers.densemem.rule=host(`${dense_mem_domain:-localhost}`)")
	assert.Contains(t, text, "traefik.http.services.densemem.loadbalancer.server.port=8080")
	assert.NotContains(t, text, "stripprefix")
	assert.NotContains(t, text, "replacepath")
}

func removedGraphServiceName() string {
	return "neo" + "4j"
}

func removedGraphEnvKey() string {
	return "NEO" + "4J_URI"
}

func readExample(t *testing.T, name string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("../../examples", name))
	require.NoError(t, err)
	return string(b)
}

func readComposeExample(t *testing.T, name string) composeExample {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("../../examples", name))
	require.NoError(t, err)
	var out composeExample
	require.NoError(t, yaml.Unmarshal(b, &out))
	return out
}

func requireComposeService(t *testing.T, compose composeExample, name string) composeService {
	t.Helper()

	service, ok := compose.Services[name]
	require.True(t, ok, "compose service %q missing", name)
	return service
}

type composeExample struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image       string                      `yaml:"image"`
	Profiles    []string                    `yaml:"profiles"`
	Environment map[string]string           `yaml:"environment"`
	DependsOn   map[string]composeDependsOn `yaml:"depends_on"`
}

type composeDependsOn struct {
	Condition string `yaml:"condition"`
}
