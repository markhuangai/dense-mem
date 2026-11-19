package evalharness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const PostgresImage = "pgvector/pgvector:0.8.2-pg18-trixie"

var seedImagePattern = regexp.MustCompile(`^ghcr\.io/markhuangai/dense-mem-eval-postgres@sha256:[0-9a-f]{64}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var releasePattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?$`)

type SeedMetadata struct {
	IntakeProof         bool    `json:"intake_proof,omitempty"`
	AISecurityReview    string  `json:"ai_security_review,omitempty"`
	Version             int     `json:"version"`
	Dataset             string  `json:"dataset"`
	RegistryHash        string  `json:"registry_sha256"`
	BaseRelease         string  `json:"base_release"`
	SourceRevision      string  `json:"source_revision"`
	Scope               Scope   `json:"scope"`
	CorpusRows          int     `json:"corpus_rows"`
	CanonicalHash       string  `json:"canonical_sha256"`
	SearchHash          string  `json:"search_sha256"`
	MappingHash         string  `json:"mapping_sha256"`
	DumpHash            string  `json:"dump_sha256"`
	DumpBytes           int64   `json:"dump_bytes"`
	SchemaVersion       int64   `json:"schema_version"`
	PostgresImage       string  `json:"postgres_image"`
	Model               string  `json:"model"`
	ContextWindowTokens int     `json:"context_window_tokens"`
	EmbeddingModel      string  `json:"embedding_model"`
	EmbeddingDimensions int     `json:"embedding_dimensions"`
	ImportWorkers       int     `json:"import_workers"`
	ElapsedMS           float64 `json:"elapsed_ms"`
}

func (s SeedMetadata) Validate(manifest Manifest, scope Scope, corpusRows int) error {
	if s.Version != 1 || s.IntakeProof != manifest.IntakeProof || s.Dataset != manifest.Dataset || s.RegistryHash != manifest.RegistryHash || s.CorpusRows != corpusRows || corpusRows < 1 || s.Scope != scope {
		return errors.New("seed dataset, source registry, denominator or authenticated scope differs")
	}
	if !digestPattern.MatchString(s.CanonicalHash) || !digestPattern.MatchString(s.SearchHash) || !digestPattern.MatchString(s.MappingHash) || !digestPattern.MatchString(s.DumpHash) || s.DumpBytes <= 0 || s.SchemaVersion <= 0 || !releasePattern.MatchString(s.BaseRelease) || !revisionPattern.MatchString(s.SourceRevision) {
		return errors.New("seed provenance or content digest is incomplete")
	}
	for _, id := range []string{s.Scope.TeamID, s.Scope.CredentialID, s.Scope.OwnerID, s.Scope.SpaceID} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil {
			return errors.New("seed identity is invalid")
		}
	}
	if s.Scope.Generation < 1 || s.PostgresImage != PostgresImage || s.EmbeddingModel != manifest.EmbeddingModel || s.EmbeddingDimensions != manifest.EmbeddingDimensions || !modelPattern.MatchString(s.Model) || strings.Contains(s.Model, "://") || s.ContextWindowTokens < 16384 {
		return errors.New("seed runtime or embedding contract is incompatible")
	}
	if s.AISecurityReview != "" && s.AISecurityReview != "skipped" || s.IntakeProof && s.AISecurityReview != "skipped" {
		return errors.New("seed AI security review provenance is invalid")
	}
	return validateImportWorkers(s.ImportWorkers)
}

func ReadSeed(dir string) (SeedMetadata, KnowledgeMapping, error) {
	var seed SeedMetadata
	var mapping KnowledgeMapping
	if err := readJSON(filepath.Join(dir, "seed.json"), &seed); err != nil {
		return seed, mapping, err
	}
	for _, file := range []struct{ Name, Hash string }{{"mapping.json", seed.MappingHash}, {"database.dump", seed.DumpHash}} {
		hash, err := FileHash(filepath.Join(dir, file.Name))
		if err != nil {
			return seed, mapping, err
		}
		if !digestPattern.MatchString(file.Hash) || hash != file.Hash {
			return seed, mapping, errors.New("seed file digest differs")
		}
	}
	info, err := os.Stat(filepath.Join(dir, "database.dump"))
	if err != nil {
		return seed, mapping, err
	}
	if info.Size() != seed.DumpBytes {
		return seed, mapping, errors.New("seed dump size differs")
	}
	err = readJSON(filepath.Join(dir, "mapping.json"), &mapping)
	return seed, mapping, err
}

func VerifySeed(ctx context.Context, manifest Manifest, corpus []CorpusItem, client *HTTPClient, native *Native) (SeedMetadata, KnowledgeMapping, map[string]CanonicalEvidence, NativeObservation, error) {
	var observation NativeObservation
	if err := native.ResolveScope(ctx); err != nil {
		return SeedMetadata{}, KnowledgeMapping{}, nil, observation, err
	}
	seed, mapping, err := ReadSeed(manifest.SeedDir)
	if err != nil {
		return seed, mapping, nil, observation, err
	}
	if err = seed.Validate(manifest, native.Scope, len(corpus)); err != nil {
		return seed, mapping, nil, observation, err
	}
	if _, err = client.ensureContract(ctx); err != nil {
		return seed, mapping, nil, observation, err
	}
	canonical, err := native.VerifyMappings(ctx, corpus, mapping)
	if err != nil {
		return seed, mapping, nil, observation, err
	}
	if seed.AISecurityReview == "skipped" {
		if err := native.verifySkippedAIReview(ctx, len(corpus)); err != nil {
			return seed, mapping, nil, observation, err
		}
	}
	observation, err = native.Snapshot(ctx)
	if err == nil {
		err = NativeReady(observation, false, true, manifest, native.Scope.TeamID)
	}
	if err == nil && (observation.CanonicalHash != seed.CanonicalHash || observation.Ontology.Counts.Organized != 0) {
		err = errors.New("restored seed canonical data or ontology-off state differs")
	}
	if err == nil {
		var searchHash string
		searchHash, err = native.SearchFingerprint(ctx)
		if err == nil && searchHash != seed.SearchHash {
			err = errors.New("restored search state changed; rebuild the seed instead of repairing it during evaluation")
		}
	}
	return seed, mapping, canonical, observation, err
}

func Prepare(ctx context.Context, manifest Manifest, corpus []CorpusItem, client *HTTPClient, native *Native) (SeedMetadata, error) {
	started := time.Now()
	seed := SeedMetadata{Version: 1, Dataset: manifest.Dataset, RegistryHash: manifest.RegistryHash, BaseRelease: manifest.BaseRelease, SourceRevision: manifest.SourceRevision, CorpusRows: len(corpus), PostgresImage: PostgresImage, Model: manifest.Model, ContextWindowTokens: manifest.ContextWindowTokens, EmbeddingModel: manifest.EmbeddingModel, EmbeddingDimensions: manifest.EmbeddingDimensions, ImportWorkers: manifest.ImportWorkers, IntakeProof: manifest.IntakeProof}
	if manifest.SkipAISecurityReview {
		seed.AISecurityReview = "skipped"
	}
	work, cancel := context.WithDeadline(ctx, manifest.ModelDeadline)
	defer cancel()
	if err := native.ResolveScope(work); err != nil {
		return seed, err
	}
	if manifest.ResumeScope != nil && *manifest.ResumeScope != native.Scope {
		return seed, errors.New("checkpoint database scope differs from the pinned bootstrap")
	}
	before, err := native.Snapshot(work)
	if err != nil {
		return seed, err
	}
	if (before.EvidenceCount != 0 && manifest.ResumeScope == nil) || before.ForeignSources != 0 || before.Ontology.Enabled || !before.Ontology.Paused {
		return seed, errors.New("seed preparation requires an empty bootstrap with ontology disabled and paused")
	}
	options := corpusImportOptions{native: native}
	if manifest.SeedBuildID != "" && !manifest.IntakeProof {
		options.rateLimit = &importRateLimit{cooldown: seedRateLimitCooldown}
	}
	if manifest.ResumeScope != nil {
		recovered, canonical, err := native.readSeedMappings(work, corpus, true)
		if err != nil {
			return seed, err
		}
		if len(canonical) != before.EvidenceCount {
			return seed, errors.New("checkpoint contains Evidence outside its recorded shared space")
		}
		if err = NativeReady(before, false, true, manifest, native.Scope.TeamID); err != nil {
			return seed, err
		}
		if seed.AISecurityReview == "skipped" {
			if err := native.verifySkippedAIReview(work, len(recovered.Partitions)); err != nil {
				return seed, err
			}
		}
		options.recovered = recovered
	}
	mapping, err := client.importCorpusWithOutcomes(work, corpus, manifest.ImportWorkers, options)
	if err != nil {
		return seed, err
	}
	if _, err = native.VerifyMappings(work, corpus, mapping); err != nil {
		return seed, err
	}
	if seed.AISecurityReview == "skipped" {
		if err := native.verifySkippedAIReview(work, len(corpus)); err != nil {
			return seed, err
		}
	}
	ready, err := native.Snapshot(work)
	if err != nil {
		return seed, err
	}
	if err = NativeReady(ready, false, true, manifest, native.Scope.TeamID); err != nil {
		return seed, err
	}
	if ready.Ontology.Counts.Organized != 0 {
		return seed, errors.New("seed contains organized Evidence")
	}
	seed.Scope, seed.CanonicalHash = native.Scope, ready.CanonicalHash
	seed.SearchHash, err = native.SearchFingerprint(work)
	if err != nil {
		return seed, err
	}
	if err = native.DB.QueryRowContext(work, `SELECT COALESCE(max(version_id),0) FROM (SELECT DISTINCT ON (version_id) version_id,is_applied FROM goose_db_version ORDER BY version_id,id DESC) latest WHERE is_applied`).Scan(&seed.SchemaVersion); err != nil {
		return seed, err
	}
	if err = os.MkdirAll(manifest.SeedDir, 0700); err != nil {
		return seed, err
	}
	mappingPath := filepath.Join(manifest.SeedDir, "mapping.json")
	if err = writeJSON(mappingPath, mapping); err != nil {
		return seed, err
	}
	seed.MappingHash, err = FileHash(mappingPath)
	if err != nil {
		return seed, err
	}
	seed.ElapsedMS = float64(time.Since(started)) / float64(time.Millisecond)
	if err = writeJSON(filepath.Join(manifest.SeedDir, "seed.json"), seed); err != nil {
		return seed, err
	}
	err = client.Capture.Emit("seed_prepared", map[string]any{"dataset": seed.Dataset, "corpus_rows": seed.CorpusRows, "canonical_sha256": seed.CanonicalHash, "elapsed_ms": seed.ElapsedMS, "workers": seed.ImportWorkers})
	return seed, err
}

func (n *Native) SearchFingerprint(ctx context.Context) (string, error) {
	tx, err := n.systemTx(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var hash string
	err = tx.QueryRowContext(ctx, `SELECT encode(sha256(convert_to(COALESCE(string_agg(h,',' ORDER BY h),''),'UTF8')),'hex') FROM (SELECT encode(sha256(convert_to(to_jsonb(r)::text,'UTF8')),'hex') h FROM search_documents r WHERE team_id=$1::uuid) AS seed_documents`, n.Scope.TeamID).Scan(&hash)
	return hash, err
}

func (n *Native) verifySkippedAIReview(ctx context.Context, expected int) error {
	tx, err := n.systemTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var total, skipped int
	err = tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE s.response_history->-1->'normalized_response'->>'ai_security_review'='skipped')
FROM remember_attempts a JOIN semantic_assessments s ON s.team_id=a.team_id AND s.attempt_id=a.attempt_id
WHERE a.team_id=$1::uuid AND a.owner_profile_id=$2::uuid AND a.space_id=$3::uuid AND a.space_generation=$4 AND a.outcome='completed'`,
		n.Scope.TeamID, n.Scope.OwnerID, n.Scope.SpaceID, n.Scope.Generation).Scan(&total, &skipped)
	if err != nil {
		return err
	}
	if total != expected || skipped != expected {
		return errors.New("seed assessment provenance differs from the selected AI security review policy")
	}
	return nil
}
