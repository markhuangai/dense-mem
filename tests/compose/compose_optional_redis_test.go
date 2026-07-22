package compose_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDockerComposeBaseExample_LocalOnly(t *testing.T) {
	text := readExample(t, "docker-compose.base.yml")
	compose := readComposeExample(t, "docker-compose.base.yml")
	server := requireComposeService(t, compose, "server")
	neo4j := requireComposeService(t, compose, "neo4j")

	assert.Contains(t, text, "127.0.0.1:${DENSE_MEM_PORT:-8080}:8080")
	assert.Contains(t, text, "127.0.0.1:${CONTROL_PORTAL_PORT:-8090}:8090")
	assert.NotContains(t, text, "\n      HTTP_ADDR:")
	assert.NotContains(t, text, "traefik")
	assert.NotContains(t, compose.Services, "redis")
	assert.ElementsMatch(t, []string{"legacy-neo4j"}, neo4j.Profiles)
	assert.Equal(t, "${NEO4J_URI:-}", server.Environment["NEO4J_URI"])
	assert.NotContains(t, server.DependsOn, "neo4j")
}

func TestDockerComposeExpertExample_HasOptionalProfiles(t *testing.T) {
	text := readExample(t, "docker-compose.expert.yml")
	compose := readComposeExample(t, "docker-compose.expert.yml")
	server := requireComposeService(t, compose, "server")

	assert.ElementsMatch(t, []string{"traefik"}, requireComposeService(t, compose, "traefik").Profiles)
	assert.ElementsMatch(t, []string{"redis"}, requireComposeService(t, compose, "redis").Profiles)
	assert.ElementsMatch(t, []string{"legacy-neo4j"}, requireComposeService(t, compose, "neo4j").Profiles)
	assert.Contains(t, text, "Redis")
	assert.Contains(t, text, "--entrypoints.websecure.http3")
	assert.Contains(t, text, "${TRAEFIK_HTTPS_PORT:-443}:443/udp")
	assert.NotContains(t, text, "\n      HTTP_ADDR:")
	assert.Equal(t, "${NEO4J_URI:-}", server.Environment["NEO4J_URI"])
	assert.NotContains(t, server.DependsOn, "redis")
	assert.NotContains(t, server.DependsOn, "neo4j")
}

func TestDockerComposeDemoExample_UsesDemoImageAndRedis(t *testing.T) {
	text := readExample(t, "docker-compose.demo.yml")
	compose := readComposeExample(t, "docker-compose.demo.yml")
	demo := requireComposeService(t, compose, "demo")

	assert.Equal(t, "${DENSE_MEM_DEMO_IMAGE:-ghcr.io/markhuangai/dense-mem:demo}", demo.Image)
	assert.Contains(t, demo.Environment, "DEMO_PUBLIC_BASE_URL")
	assert.Equal(t, "redis:6379", demo.Environment["REDIS_ADDR"])
	require.Contains(t, demo.DependsOn, "redis")
	assert.Equal(t, "service_healthy", demo.DependsOn["redis"].Condition)
	assert.Contains(t, text, "24-hour demo service")
	assert.ElementsMatch(t, []string{"legacy-neo4j"}, requireComposeService(t, compose, "neo4j").Profiles)
	assert.Equal(t, "${NEO4J_URI:-}", demo.Environment["NEO4J_URI"])
	assert.NotContains(t, demo.DependsOn, "neo4j")
	assert.NotContains(t, text, "\n      HTTP_ADDR:")
	assert.NotContains(t, text, "CONTROL_PORTAL_TOKEN")
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
