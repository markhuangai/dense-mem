package evalharness

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"time"
)

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var imagePattern = regexp.MustCompile(`^ghcr\.io/markhuangai/dense-mem(?::[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}$`)
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9@][A-Za-z0-9_./:-]{0,127}$`)

func RequireRunner() error {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Getenv("GITHUB_RUN_ID") == "" || os.Getenv("GITHUB_RUN_ATTEMPT") != "1" {
		return errors.New("evaluation requires a fresh GitHub-hosted workflow run; local execution and job reruns are unsupported")
	}
	return nil
}

func ReadManifest(path string) (Manifest, error) {
	var manifest Manifest
	if err := RequireRunner(); err != nil {
		return manifest, err
	}
	if err := readJSON(path, &manifest); err != nil {
		return manifest, err
	}
	if manifest.Version != 1 || manifest.RunID == "" || manifest.WorkflowRunID != os.Getenv("GITHUB_RUN_ID") || !revisionPattern.MatchString(manifest.SourceRevision) || !imagePattern.MatchString(manifest.Image) {
		return manifest, errors.New("execution manifest identity is invalid")
	}
	if manifest.Mode != "live" && manifest.Mode != "fixtures" && manifest.Mode != "replay" {
		return manifest, errors.New("invalid runner campaign mode")
	}
	if manifest.Mode != "replay" && manifest.RunID != manifest.WorkflowRunID+"-"+manifest.Dataset {
		return manifest, errors.New("manifest does not belong to this dataset job")
	}
	if !modelPattern.MatchString(manifest.Model) || strings.Contains(manifest.Model, "://") || manifest.EmbeddingModel != "@cf/baai/bge-m3" || manifest.EmbeddingDimensions != 1024 || manifest.ContextWindowTokens < 16384 {
		return manifest, errors.New("explicit verified model and context settings are required")
	}
	if manifest.DataDir == "" || manifest.OutputDir == "" || len(manifest.RegistryHash) != 64 {
		return manifest, errors.New("manifest data and output identity missing")
	}
	if manifest.Mode != "replay" && (manifest.ModelDeadline.IsZero() || manifest.ModelDeadline.After(time.Now().Add(330*time.Minute))) {
		return manifest, errors.New("model deadline is missing or exceeds 330 minutes")
	}
	return manifest, nil
}
