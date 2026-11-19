package evalharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func validSeedFixture(t *testing.T) (SeedMetadata, Manifest) {
	t.Helper()
	scope := Scope{TeamID: uuid.NewString(), CredentialID: uuid.NewString(), OwnerID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1}
	manifest := Manifest{Dataset: "hotpotqa", RegistryHash: strings.Repeat("a", 64), EmbeddingModel: "text-embedding-3-small", EmbeddingDimensions: 1536}
	seed := SeedMetadata{Version: 1, Dataset: manifest.Dataset, RegistryHash: manifest.RegistryHash, Scope: scope, CorpusRows: 3, CanonicalHash: strings.Repeat("b", 64), SearchHash: strings.Repeat("f", 64), MappingHash: strings.Repeat("c", 64), DumpHash: strings.Repeat("d", 64), DumpBytes: 10, SchemaVersion: 20261113020000, BaseRelease: "v2.8.1-rc.5", SourceRevision: strings.Repeat("e", 40), PostgresImage: PostgresImage, Model: "dense-mem-testing", ContextWindowTokens: 500000, EmbeddingModel: manifest.EmbeddingModel, EmbeddingDimensions: manifest.EmbeddingDimensions, ImportWorkers: 20}
	return seed, manifest
}

func TestSeedValidationBindsSourcesCredentialsAndEmbeddingContract(t *testing.T) {
	seed, manifest := validSeedFixture(t)
	require.NoError(t, seed.Validate(manifest, seed.Scope, 3))
	for _, change := range []func(*SeedMetadata){
		func(s *SeedMetadata) { s.Dataset = "musique" },
		func(s *SeedMetadata) { s.RegistryHash = strings.Repeat("f", 64) },
		func(s *SeedMetadata) { s.CorpusRows-- },
		func(s *SeedMetadata) { s.Scope.TeamID = uuid.NewString() },
		func(s *SeedMetadata) { s.Scope.CredentialID = uuid.NewString() },
		func(s *SeedMetadata) { s.Scope.OwnerID = uuid.NewString() },
		func(s *SeedMetadata) { s.Scope.SpaceID = uuid.NewString() },
		func(s *SeedMetadata) { s.Scope.Generation++ },
		func(s *SeedMetadata) { s.CanonicalHash = "" },
		func(s *SeedMetadata) { s.MappingHash = "" },
		func(s *SeedMetadata) { s.DumpHash = "" },
		func(s *SeedMetadata) { s.BaseRelease = "latest" },
		func(s *SeedMetadata) { s.EmbeddingDimensions = 1024 },
		func(s *SeedMetadata) { s.EmbeddingModel = "other" },
		func(s *SeedMetadata) { s.ImportWorkers = 101 },
	} {
		changed := seed
		change(&changed)
		require.Error(t, changed.Validate(manifest, seed.Scope, 3))
	}
	manifest.Model = "another-answer-model"
	require.NoError(t, seed.Validate(manifest, seed.Scope, 3), "a frozen ingestion seed can evaluate a different answer model")
}

func TestSeedValidationRequiresSkipProvenanceForIntakeProof(t *testing.T) {
	seed, manifest := validSeedFixture(t)
	seed.IntakeProof, manifest.IntakeProof = true, true
	require.ErrorContains(t, seed.Validate(manifest, seed.Scope, 3), "AI security review provenance")
	seed.AISecurityReview = "skipped"
	require.NoError(t, seed.Validate(manifest, seed.Scope, 3))
	seed.AISecurityReview = "unknown"
	require.ErrorContains(t, seed.Validate(manifest, seed.Scope, 3), "AI security review provenance")
}

func TestSeedAdmissionManifestIsReadableByTheContainerUser(t *testing.T) {
	directory := t.TempDir()
	scopePath, manifestPath := filepath.Join(directory, "scope.json"), filepath.Join(directory, "seed-admission.json")
	seed, _ := validSeedFixture(t)
	require.NoError(t, writeJSON(scopePath, seed.Scope))
	require.NoError(t, WriteSeedAdmission(manifestPath, scopePath, []CorpusItem{{SourceDocID: "hotpotqa:1", SourceDataset: "hotpotqa", Content: "Exact corpus text."}}))
	info, err := os.Stat(manifestPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0444), info.Mode().Perm())
}

func TestSeedFilesRejectCorruptedDumpMappingAndSize(t *testing.T) {
	seed, _ := validSeedFixture(t)
	dir := t.TempDir()
	mapping := KnowledgeMapping{Partitions: map[string][]ImportedPartition{}}
	require.NoError(t, writeJSON(filepath.Join(dir, "mapping.json"), mapping))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "database.dump"), []byte("synthetic binary checksum fixture"), 0600))
	var err error
	seed.MappingHash, err = FileHash(filepath.Join(dir, "mapping.json"))
	require.NoError(t, err)
	seed.DumpHash, err = FileHash(filepath.Join(dir, "database.dump"))
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(dir, "database.dump"))
	require.NoError(t, err)
	seed.DumpBytes = info.Size()
	require.NoError(t, writeJSON(filepath.Join(dir, "seed.json"), seed))
	actual, restored, err := ReadSeed(dir)
	require.NoError(t, err)
	require.Equal(t, seed, actual)
	require.Equal(t, mapping, restored)
	seed.DumpBytes++
	require.NoError(t, writeJSON(filepath.Join(dir, "seed.json"), seed))
	_, _, err = ReadSeed(dir)
	require.ErrorContains(t, err, "size")
	seed.DumpBytes--
	require.NoError(t, writeJSON(filepath.Join(dir, "seed.json"), seed))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "database.dump"), []byte("corrupted dump"), 0600))
	_, _, err = ReadSeed(dir)
	require.ErrorContains(t, err, "digest")
}

func TestEvaluationManifestRejectsImportCapacityAndUnpinnedSeeds(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	t.Setenv("GITHUB_RUN_ID", "42")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	_, manifest := validSeedFixture(t)
	manifest.Version, manifest.RunID, manifest.WorkflowRunID, manifest.Operation, manifest.Mode = 1, "42-hotpotqa", "42", "run", "live"
	manifest.SourceRevision, manifest.Image = strings.Repeat("a", 40), "ghcr.io/markhuangai/dense-mem@sha256:"+strings.Repeat("b", 64)
	manifest.Model, manifest.ContextWindowTokens = "dense-mem-testing", 500000
	manifest.DataDir, manifest.OutputDir, manifest.SeedDir = "data", "public", "seed"
	manifest.SeedImage = "ghcr.io/markhuangai/dense-mem-eval-postgres@sha256:" + strings.Repeat("c", 64)
	manifest.ModelDeadline = time.Now().Add(time.Hour)
	file := filepath.Join(t.TempDir(), "manifest.json")
	require.NoError(t, writeJSON(file, manifest))
	_, err := ReadManifest(file)
	require.NoError(t, err)
	manifest.ImportWorkers = 20
	require.NoError(t, writeJSON(file, manifest))
	_, err = ReadManifest(file)
	require.ErrorContains(t, err, "no import workers")
	manifest.ImportWorkers = 0
	manifest.SeedImage = "ghcr.io/markhuangai/dense-mem-eval-postgres:hotpotqa-latest"
	require.NoError(t, writeJSON(file, manifest))
	_, err = ReadManifest(file)
	require.ErrorContains(t, err, "immutable seed")
}
