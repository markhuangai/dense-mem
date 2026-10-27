package evalharness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPairManifestRejectsUnsafeOrIncompatiblePreparedInputs(t *testing.T) {
	cases := []struct {
		name string
		edit func(*PairConfig)
	}{
		{"schema", func(c *PairConfig) { c.SchemaVersion = "future" }},
		{"run_path", func(c *PairConfig) { c.RunID = "../another-run" }},
		{"deadline", func(c *PairConfig) { c.Deadline = time.Time{} }},
		{"relative_output", func(c *PairConfig) { c.OutDir = "relative" }},
		{"input_hash", func(c *PairConfig) { c.SeedHash = "unknown" }},
		{"changed_seed", func(c *PairConfig) { c.SeedHash = pairBytesHash([]byte("another seed")) }},
		{"changed_suite", func(c *PairConfig) { c.SuiteSHA256 = pairBytesHash([]byte("another suite")) }},
		{"runner_source", func(c *PairConfig) { c.Candidate.Runner.SourceRevision = c.Candidate.ImageID }},
		{"unknown_protocol", func(c *PairConfig) { c.Candidate.Protocol = "future" }},
		{"candidate_protocol", func(c *PairConfig) { c.Candidate.Protocol = "none" }},
		{"legacy_processing", func(c *PairConfig) { c.Baseline.Protocol = "legacy" }},
		{"scope_uuid", func(c *PairConfig) { c.Candidate.Scope.OwnerID = "other" }},
		{"generation", func(c *PairConfig) { c.Candidate.Scope.Generation = 0 }},
		{"global_capacity", func(c *PairConfig) { c.Candidate.Capacity.Global = 9 }},
		{"team_capacity", func(c *PairConfig) { c.Candidate.Capacity.Team = 9 }},
		{"context_capacity", func(c *PairConfig) { c.Candidate.Capacity.ContextWindowTokens = 0 }},
		{"backing_revision", func(c *PairConfig) { c.Candidate.Models.BackingRevision = "" }},
		{"ontology_model", func(c *PairConfig) { c.Candidate.Models.Ontology = "" }},
		{"credential_reference", func(c *PairConfig) { c.Candidate.Runtime.APIKeyEnv = "inline-credential" }},
		{"runner_path", func(c *PairConfig) { c.Candidate.Runner.Path = "relative" }},
		{"capture_path", func(c *PairConfig) { c.Baseline.CaptureDir = "relative" }},
		{"unpinned_capture", func(c *PairConfig) { c.Baseline.CaptureDir = filepath.Join(t.TempDir(), "capture") }},
		{"shared_server", func(c *PairConfig) { c.Candidate.Runtime.ServerContainer = c.Baseline.Runtime.ServerContainer }},
		{"remote_endpoint", func(c *PairConfig) { c.Candidate.Runtime.BaseURL = "http://example.org:8080" }},
		{"credential_in_endpoint", func(c *PairConfig) { c.Candidate.Runtime.ControlURL = "http://operator:secret@127.0.0.1:8090" }},
		{"unbound_port", func(c *PairConfig) { c.Candidate.Runtime.BaseURL = "http://localhost" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			c := pairedFixture(t)
			output := c.OutDir
			test.edit(&c)
			config := filepath.Join(t.TempDir(), "manifest.json")
			require.NoError(t, writePairJSON(config, c))
			_, err := LoadPairConfig(config)
			require.Error(t, err)
			require.NoDirExists(t, output, "rejected configuration must not create runtime or stage state")
		})
	}
}

func TestPairManifestRequiresPublicCohortProvenanceAndOneDocument(t *testing.T) {
	c := pairedFixture(t)
	config := filepath.Join(t.TempDir(), "manifest.json")
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(config, append(raw, []byte("\n{}")...), 0o600))
	_, err = LoadPairConfig(config)
	require.ErrorContains(t, err, "one document")
	var seed SeedManifest
	require.NoError(t, readJSONFile(c.SeedManifest, &seed))
	seed.SeedID = "public_6axis_1k_v3"
	require.NoError(t, writeJSONFile(c.SeedManifest, seed))
	_, _, _, _, _, _, c.SeedHash, err = loadRunInputs(c.SeedManifest, c.Suite)
	require.NoError(t, err)
	require.NoError(t, writePairJSON(config, c))
	_, err = LoadPairConfig(config)
	require.ErrorContains(t, err, "requires validation_report_file")
	require.NoDirExists(t, c.OutDir)
}
