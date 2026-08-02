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

func TestImageBuildJobsUseRootlessDockerRunnerOnly(t *testing.T) {
	rootlessJobs := map[string]bool{
		".github/workflows/ci-shared.yml/container-images":      false,
		".github/workflows/publish-demo-image.yml/publish-demo": false,
		".github/workflows/publish-image.yml/publish":           false,
	}

	paths, err := filepath.Glob(filepath.Join(repoRoot(t), ".github", "workflows", "*.yml"))
	require.NoError(t, err)
	for _, path := range paths {
		var workflow struct {
			Jobs map[string]struct {
				RunsOn string `yaml:"runs-on"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(readFile(t, path), &workflow))
		relativePath, err := filepath.Rel(repoRoot(t), path)
		require.NoError(t, err)
		for jobName, job := range workflow.Jobs {
			if job.RunsOn == "" {
				continue
			}
			key := filepath.ToSlash(relativePath) + "/" + jobName
			expectedRunner := "docker-runner"
			if _, buildsImage := rootlessJobs[key]; buildsImage {
				expectedRunner = "rootless-docker"
				rootlessJobs[key] = true
			}
			assert.Equal(t, expectedRunner, job.RunsOn, key)
		}
	}
	for key, seen := range rootlessJobs {
		assert.True(t, seen, key)
	}
}

func TestStableReleasePreflightsBothImagesBeforeTagging(t *testing.T) {
	contents := readRepoFile(t, ".github/workflows/release.yml")
	var workflow struct {
		Jobs map[string]struct {
			Needs yaml.Node `yaml:"needs"`
			Steps []struct {
				Name string         `yaml:"name"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(contents, &workflow))

	createTag, ok := workflow.Jobs["create-release-tag"]
	require.True(t, ok)
	var createTagNeeds []string
	require.NoError(t, createTag.Needs.Decode(&createTagNeeds))
	assert.Contains(t, createTagNeeds, "preflight-images")

	promote, ok := workflow.Jobs["promote-images"]
	require.True(t, ok)
	var promoteNeeds []string
	require.NoError(t, promote.Needs.Decode(&promoteNeeds))
	assert.Contains(t, promoteNeeds, "create-release-tag")

	preflight, ok := workflow.Jobs["preflight-images"]
	require.True(t, ok)
	checkoutFound := false
	for _, step := range preflight.Steps {
		if step.Name != "Checkout RC source" {
			continue
		}
		checkoutFound = true
		persistCredentials, configured := step.With["persist-credentials"]
		require.True(t, configured)
		assert.Equal(t, false, persistCredentials)
	}
	require.True(t, checkoutFound)

	text := string(contents)
	assert.Contains(t, text, "--variant release")
	assert.Contains(t, text, "--variant demo")
	assert.Contains(t, text, "demo-${{ needs.prepare-release.outputs.release_tag }}")
	assert.NotContains(t, text, "demo_ref=\"${image}:demo\"")
}

func TestPublishVerificationUsesEnvironmentBoundary(t *testing.T) {
	tests := []struct {
		path     string
		stepName string
	}{
		{path: ".github/workflows/publish-image.yml", stepName: "Verify published image"},
		{path: ".github/workflows/publish-demo-image.yml", stepName: "Verify published demo image"},
	}

	for _, tc := range tests {
		t.Run(filepath.Base(tc.path), func(t *testing.T) {
			var workflow struct {
				Jobs map[string]struct {
					Steps []struct {
						Name string            `yaml:"name"`
						Env  map[string]string `yaml:"env"`
						Run  string            `yaml:"run"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			require.NoError(t, yaml.Unmarshal(readRepoFile(t, tc.path), &workflow))

			found := false
			for _, job := range workflow.Jobs {
				for _, step := range job.Steps {
					if step.Name != tc.stepName {
						continue
					}
					found = true
					assert.Contains(t, step.Env, "IMAGE_NAME")
					assert.Contains(t, step.Env, "REVISION")
					assert.Contains(t, step.Env, "TAG")
					assert.NotContains(t, step.Run, "${{")
				}
			}
			require.True(t, found)
		})
	}
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
			assert.Contains(t, contents, "--start-period=30m")
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
