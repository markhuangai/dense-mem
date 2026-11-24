package evalharness

import (
	"encoding/json"
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
	invalid := seed
	invalid.Scope.OwnerID = uuid.Nil.String()
	require.ErrorContains(t, invalid.Validate(manifest, invalid.Scope, 3), "seed identity is invalid")
	manifest.Operation, manifest.SeedImage = "verify-seed", "sha256:"+strings.Repeat("e", 64)
	require.ErrorContains(t, seed.Validate(manifest, seed.Scope, 3), "format differs")
	seed.Version = bootableSeedVersion
	require.NoError(t, seed.Validate(manifest, seed.Scope, 3))
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

func TestSeedAdmissionManifestReplacesReadOnlyFileOnResume(t *testing.T) {
	directory := t.TempDir()
	scopePath, manifestPath := filepath.Join(directory, "scope.json"), filepath.Join(directory, "seed-admission.json")
	seed, _ := validSeedFixture(t)
	require.NoError(t, writeJSON(scopePath, seed.Scope))
	corpus := []CorpusItem{{SourceDocID: "hotpotqa:1", SourceDataset: "hotpotqa", Content: "Exact corpus text."}}
	require.NoError(t, WriteSeedAdmission(manifestPath, scopePath, corpus))
	info, err := os.Stat(manifestPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0444), info.Mode().Perm())
	original, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	require.NoError(t, WriteSeedAdmission(manifestPath, scopePath, corpus))
	replaced, err := os.Stat(manifestPath)
	require.NoError(t, err)
	require.False(t, os.SameFile(info, replaced), "resume must replace the read-only file atomically")
	require.Equal(t, os.FileMode(0444), replaced.Mode().Perm())
	actual, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	require.Equal(t, original, actual)
}

func TestSeedFilesRejectCorruptedDumpMappingAndSize(t *testing.T) {
	seed, _ := validSeedFixture(t)
	dir := t.TempDir()
	_, _, err := ReadSeed(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, writeJSON(filepath.Join(dir, "seed.json"), seed))
	_, _, err = ReadSeed(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
	mapping := KnowledgeMapping{Partitions: map[string][]ImportedPartition{}}
	require.NoError(t, writeJSON(filepath.Join(dir, "mapping.json"), mapping))
	seed.MappingHash, err = FileHash(filepath.Join(dir, "mapping.json"))
	require.NoError(t, err)
	require.NoError(t, writeJSON(filepath.Join(dir, "seed.json"), seed))
	_, _, err = ReadSeed(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "database.dump"), []byte("synthetic binary checksum fixture"), 0600))
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

	seed.Version = bootableSeedVersion
	require.NoError(t, os.Remove(filepath.Join(dir, "database.dump")))
	require.NoError(t, writeJSON(filepath.Join(dir, "seed.json"), seed))
	actual, restored, err = ReadSeed(dir)
	require.NoError(t, err)
	require.Equal(t, seed, actual)
	require.Equal(t, mapping, restored)
	retired := struct {
		SeedMetadata
		OntologyPreparation json.RawMessage `json:"ontology_preparation"`
	}{seed, json.RawMessage("null")}
	require.NoError(t, writeJSON(filepath.Join(dir, "seed.json"), retired))
	_, _, err = ReadSeed(dir)
	require.ErrorContains(t, err, "ontology_preparation")
	seed.Version = 3
	require.NoError(t, writeJSON(filepath.Join(dir, "seed.json"), seed))
	_, _, err = ReadSeed(dir)
	require.ErrorContains(t, err, "unknown seed metadata version")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mapping.json"), []byte("{}"), 0600))
	_, _, err = ReadSeed(dir)
	require.ErrorContains(t, err, "mapping digest")
}

func TestEvaluationManifestRejectsImportCapacityAndUnpinnedSeeds(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	t.Setenv("GITHUB_RUN_ID", "42")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	_, manifest := validSeedFixture(t)
	manifest.Version, manifest.RunID, manifest.WorkflowRunID, manifest.Operation, manifest.Mode = 1, "42-hotpotqa-off", "42", "run", "live"
	manifest.Condition = "off"
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
