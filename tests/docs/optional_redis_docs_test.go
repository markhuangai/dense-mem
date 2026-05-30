package docs_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptionalRedisDocs_AllSixFilesUpdated(t *testing.T) {
	paths := []string{
		"../../.claude/rules/architecture.md",
		"../../.claude/rules/database.md",
		"../../.claude/rules/profile-isolation.md",
		"../../README.md",
		"../../examples/.env.example",
		"../../docs/standalone-mcp-memory-architecture.md",
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
		"../../docs/standalone-mcp-memory-architecture.md",
	}

	for _, path := range paths {
		b, err := os.ReadFile(path)
		require.NoError(t, err, path)
		text := string(b)

		assert.NotContains(t, text, "digital"+"-life")
		assert.NotContains(t, text, "Digital"+" Life")
		assert.NotContains(t, text, "digital"+" life")
		assert.NotContains(t, text, "digital"+"-life-extraction-discovery.md")
		assert.Contains(t, text, "standalone HTTP MCP memory server")
	}
}

func TestOptionalRedisDocs_UsesRatelimitPrefix(t *testing.T) {
	b, err := os.ReadFile("../../.claude/rules/profile-isolation.md")
	require.NoError(t, err)

	text := string(b)
	assert.Contains(t, text, "ratelimit:")
	assert.NotContains(t, text, "rate:")
	assert.NotContains(t, text, "cache:search")
}
