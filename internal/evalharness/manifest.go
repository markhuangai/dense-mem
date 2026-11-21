package evalharness

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var imagePattern = regexp.MustCompile(`^ghcr\.io/markhuangai/dense-mem(?::[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}$`)
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9@][A-Za-z0-9_./:-]{0,127}$`)
var seedBuildPattern = regexp.MustCompile(`^local-[a-f0-9]{32}$`)
var localImagePattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func RequireRunner() error {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Getenv("GITHUB_RUN_ID") == "" || os.Getenv("GITHUB_RUN_ATTEMPT") != "1" {
		return errors.New("evaluation requires a fresh GitHub-hosted workflow run; local execution and job reruns are unsupported")
	}
	return nil
}

func ReadManifest(path string) (Manifest, error) {
	var manifest Manifest
	if err := readJSON(path, &manifest); err != nil {
		return manifest, err
	}
	bake := manifest.Operation == "bake-ontology"
	if manifest.SeedBuildID == "" {
		if err := RequireRunner(); err != nil {
			return manifest, err
		}
		if manifest.WorkflowRunID != os.Getenv("GITHUB_RUN_ID") || (manifest.Mode == "live" && manifest.Operation != "run") {
			return manifest, errors.New("hosted execution manifest identity or operation is invalid")
		}
	} else if os.Getenv("GITHUB_ACTIONS") == "true" || manifest.WorkflowRunID != "" || manifest.Mode != "live" || !seedBuildPattern.MatchString(manifest.SeedBuildID) || manifest.SeedBuildID != os.Getenv("EVAL_LOCAL_SEED_ID") ||
		(manifest.Operation != "bootstrap" && manifest.Operation != "prepare" && manifest.Operation != "verify-seed" && !bake) {
		return manifest, errors.New("local execution permits only public seed preparation and restore verification")
	}
	imageValid := imagePattern.MatchString(manifest.Image)
	if manifest.IntakeProof {
		imageValid = manifest.SeedBuildID != "" && localImagePattern.MatchString(manifest.Image) && (manifest.Operation == "prepare" || manifest.Operation == "verify-seed")
	}
	if manifest.SkipAISecurityReview && (manifest.SeedBuildID == "" || manifest.Operation != "prepare" || !seedImagePattern.MatchString(manifest.SeedImage)) {
		return manifest, errors.New("skipping AI security review requires preparation from a public seed bootstrap")
	}
	if manifest.ResumeScope != nil && (manifest.SeedBuildID == "" || (manifest.Operation != "prepare" && !bake) || manifest.IntakeProof || !seedImagePattern.MatchString(manifest.SeedImage)) {
		return manifest, errors.New("resuming requires a local public seed build and its pinned bootstrap")
	}
	if manifest.Version != 1 || manifest.RunID == "" || !revisionPattern.MatchString(manifest.SourceRevision) || !imageValid {
		return manifest, errors.New("execution manifest identity is invalid")
	}
	if manifest.Mode != "live" && manifest.Mode != "fixtures" {
		return manifest, errors.New("invalid runner campaign mode")
	}
	if manifest.Condition != "" &&
		((manifest.Condition != "off" && manifest.Condition != "on") || (manifest.Operation != "run" && manifest.Operation != "verify-seed")) {
		return manifest, errors.New("invalid evaluation condition")
	}
	if manifest.RunID != manifest.DatasetRunID() {
		return manifest, errors.New("manifest does not belong to this dataset job")
	}
	if !modelPattern.MatchString(manifest.Model) || strings.Contains(manifest.Model, "://") || manifest.EmbeddingModel != "text-embedding-3-small" || manifest.EmbeddingDimensions != 1536 || manifest.ContextWindowTokens < 16384 {
		return manifest, errors.New("explicit verified model and context settings are required")
	}
	if manifest.Operation != "prepare" && manifest.Operation != "run" && manifest.Operation != "bootstrap" && manifest.Operation != "verify-seed" && !bake {
		return manifest, errors.New("invalid workflow-internal operation")
	}
	if manifest.Operation == "run" {
		if manifest.Mode == "live" && manifest.Condition == "" {
			return manifest, errors.New("live scoring requires a frozen condition")
		}
		manifest.AnswerWorkers = manifest.effectiveAnswerWorkers()
		if manifest.AnswerWorkers < defaultAnswerWorkers || manifest.AnswerWorkers > maxAnswerWorkers {
			return manifest, errors.New("answer_workers must be 4..10")
		}
	} else if manifest.AnswerWorkers != 0 {
		return manifest, errors.New("answer_workers applies only to scored evaluation")
	}
	if manifest.Operation == "prepare" || manifest.Operation == "bootstrap" {
		if err := validateImportWorkers(manifest.ImportWorkers); err != nil {
			return manifest, err
		}
		if !releasePattern.MatchString(manifest.BaseRelease) {
			return manifest, errors.New("seed preparation base release is invalid")
		}
	} else if manifest.ImportWorkers != 0 {
		return manifest, errors.New("scored evaluation has no import workers")
	}
	if manifest.SeedDir == "" || (manifest.Operation == "run" && manifest.Mode == "live" && !seedImagePattern.MatchString(manifest.SeedImage)) {
		return manifest, errors.New("immutable seed identity and directory are required")
	}
	if manifest.Operation == "verify-seed" && !manifest.IntakeProof && !localImagePattern.MatchString(manifest.SeedImage) {
		return manifest, errors.New("seed boot verification requires an immutable local PostgreSQL image")
	}
	if manifest.Operation == "run" && manifest.Mode == "fixtures" && manifest.SeedImage != "" && !localImagePattern.MatchString(manifest.SeedImage) {
		return manifest, errors.New("fixture seed must use an immutable local PostgreSQL image")
	}
	if manifest.Condition == "on" {
		if !validParentSeed(manifest, manifest.ParentSeedImage) || manifest.ParentSeedImage == manifest.SeedImage {
			return manifest, errors.New("ontology-on restore requires a distinct pinned parent")
		}
	} else if manifest.ParentSeedImage != "" {
		return manifest, errors.New("parent pin applies only to ontology-on restore")
	}
	if manifest.DataDir == "" || manifest.OutputDir == "" || len(manifest.RegistryHash) != 64 {
		return manifest, errors.New("manifest data and output identity missing")
	}
	maximum := 330 * time.Minute
	if bake {
		maximum = 24 * time.Hour
		if !validParentSeed(manifest, manifest.SeedImage) || !filepath.IsAbs(manifest.BakeStateDir) ||
			(manifest.Mode == "live" && (manifest.Model != "dense-mem-testing" || manifest.ContextWindowTokens != 500000)) {
			return manifest, errors.New("ontology preparation requires a pinned seed, private checkpoint and verified model settings")
		}
	} else if manifest.BakeStateDir != "" {
		return manifest, errors.New("ontology checkpoint applies only to preparation")
	}
	if manifest.ModelDeadline.IsZero() || manifest.ModelDeadline.After(time.Now().Add(maximum)) {
		return manifest, errors.New("model deadline is missing or exceeds the campaign limit")
	}
	return manifest, nil
}

func (m Manifest) effectiveAnswerWorkers() int {
	if m.AnswerWorkers == 0 {
		return defaultAnswerWorkers
	}
	return m.AnswerWorkers
}

func (m Manifest) PostgresRuntimeImage() string {
	if (m.Operation == "run" || m.Operation == "verify-seed" || m.Operation == "bake-ontology") && m.SeedImage != "" {
		return m.SeedImage
	}
	return PostgresImage
}

func validParentSeed(m Manifest, image string) bool {
	if m.Mode == "fixtures" {
		return localImagePattern.MatchString(image)
	}
	return seedImagePattern.MatchString(image)
}

func (m Manifest) ExecutionID() string {
	if m.SeedBuildID != "" {
		return m.SeedBuildID
	}
	return m.WorkflowRunID
}

func (m Manifest) DatasetRunID() string {
	id := m.ExecutionID() + "-" + m.Dataset
	if m.Condition != "" {
		id += "-" + m.Condition
	}
	return id
}

func validateImportWorkers(workers int) error {
	if workers < 1 || workers > maxImportWorkers {
		return errors.New("import workers must be between 1 and 100")
	}
	return nil
}
