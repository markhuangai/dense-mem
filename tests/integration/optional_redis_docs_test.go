package integration

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptionalRedisDocs_CoreRepoFilesUpdated(t *testing.T) {
	paths := []string{
		"../../AGENTS.md",
		"../../README.md",
		"../../examples/.env.example",
	}

	for _, path := range paths {
		b, err := os.ReadFile(path)
		require.NoError(t, err, path)
		text := string(b)

		assert.Contains(t, text, "single-node")
		assert.Contains(t, text, "multi-instance")
		assert.NotContains(t, text, "Redis/Cache")
	}
}

func TestStandaloneMemoryDocs_NoLegacyDiscoveryReferences(t *testing.T) {
	paths := []string{
		"../../README.md",
	}

	for _, path := range paths {
		b, err := os.ReadFile(path)
		require.NoError(t, err, path)
		text := string(b)

		assert.NotContains(t, text, "digital"+"-life")
		assert.NotContains(t, text, "Digital"+" Life")
		assert.NotContains(t, text, "digital"+" life")
		assert.NotContains(t, text, "digital"+"-life-extraction-discovery.md")
		assert.Contains(t, text, "standalone MCP Streamable HTTP memory server")
	}
}

func TestOptionalRedisDocs_UsesRatelimitPrefix(t *testing.T) {
	b, err := os.ReadFile("../../AGENTS.md")
	require.NoError(t, err)

	text := string(b)
	assert.Contains(t, text, "ratelimit:")
	assert.NotContains(t, text, "rate:")
	assert.NotContains(t, text, "cache:search")
}
