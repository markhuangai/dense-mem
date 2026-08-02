package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestWorkflowYAMLParses(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(repoRoot(t), ".github", "workflows", "*.yml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			var document any
			require.NoError(t, yaml.Unmarshal(readFile(t, path), &document))
		})
	}
}

func TestPublishWorkflowsHaveOneAutomaticOwner(t *testing.T) {
	release := string(readRepoFile(t, ".github/workflows/publish-image.yml"))
	demo := string(readRepoFile(t, ".github/workflows/publish-demo-image.yml"))
	rc := string(readRepoFile(t, ".github/workflows/release-rc.yml"))

	assert.NotContains(t, release, "\n  push:")
	assert.NotContains(t, demo, "\n  push:")
	assert.Contains(t, rc, "uses: ./.github/workflows/publish-image.yml")
	assert.Contains(t, rc, "uses: ./.github/workflows/publish-demo-image.yml")
	assert.NotContains(t, demo, "type=raw,value=demo\n")
	assert.Contains(t, demo, "type=raw,value=demo-${{ steps.version.outputs.tag }}")
}

func TestStableReleasePreflightsBothImagesBeforeTagging(t *testing.T) {
	workflow := string(readRepoFile(t, ".github/workflows/release.yml"))
	preflight := strings.Index(workflow, "  preflight-images:")
	createTag := strings.Index(workflow, "  create-release-tag:")
	promote := strings.Index(workflow, "  promote-images:")

	require.GreaterOrEqual(t, preflight, 0)
	require.Greater(t, createTag, preflight)
	require.Greater(t, promote, createTag)
	assert.Contains(t, workflow, "--variant release")
	assert.Contains(t, workflow, "--variant demo")
	assert.Contains(t, workflow, "demo-${{ needs.prepare-release.outputs.release_tag }}")
	assert.NotContains(t, workflow, "demo_ref=\"${image}:demo\"")
}

func TestDockerfilesBuildOneProjectExecutable(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		binaryPath string
	}{
		{name: "release", path: "Dockerfile", binaryPath: "/out/server"},
		{name: "demo", path: "Dockerfile.demo", binaryPath: "/out/demo-server"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contents := string(readRepoFile(t, tc.path))
			assert.Equal(t, 1, strings.Count(contents, "go build "))
			assert.Contains(t, contents, tc.binaryPath)
			assert.NotContains(t, contents, "/out/migrate")
			assert.NotContains(t, contents, "/out/review-conflicts")
			assert.NotContains(t, contents, "/out/provision-profile")
		})
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return root
}

func readRepoFile(t *testing.T, path string) []byte {
	t.Helper()
	return readFile(t, filepath.Join(repoRoot(t), path))
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return contents
}
