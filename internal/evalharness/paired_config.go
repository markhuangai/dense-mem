package evalharness

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const PairSchema = "dense-mem.eval.pair.v1"

var pairSHA = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var pairRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)
var pairName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,95}$`)
var pairEnvName = regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)

type PairBinary struct {
	Path           string `json:"path"`
	SHA256         string `json:"sha256"`
	SourceRevision string `json:"source_revision"`
}

type PairModels struct {
	Provider                string `json:"provider"`
	Remember                string `json:"remember"`
	Ontology                string `json:"ontology"`
	BackingRevision         string `json:"backing_revision"`
	VerifierEndpointSHA256  string `json:"verifier_endpoint_sha256"`
	EmbeddingEndpointSHA256 string `json:"embedding_endpoint_sha256"`
	EmbeddingModel          string `json:"embedding_model"`
	EmbeddingDimensions     int    `json:"embedding_dimensions"`
	EmbeddingIndexStrategy  string `json:"embedding_index_strategy"`
}

type PairRuntime struct {
	Project           string `json:"project"`
	ServerContainer   string `json:"server_container"`
	PostgresContainer string `json:"postgres_container"`
	BaseURL           string `json:"base_url"`
	ControlURL        string `json:"control_url"`
	CredentialID      string `json:"credential_id"`
	APIKeyEnv         string `json:"api_key_env"`
	ControlTokenEnv   string `json:"control_token_env"`
	StateDir          string `json:"state_dir"`
}

type PairRelease struct {
	Release           string       `json:"release"`
	SourceRevision    string       `json:"source_revision"`
	ImageID           string       `json:"image_id"`
	Runner            PairBinary   `json:"runner"`
	Scope             PairScope    `json:"scope"`
	Models            PairModels   `json:"models"`
	Runtime           PairRuntime  `json:"runtime"`
	Protocol          string       `json:"protocol"`
	Capacity          PairCapacity `json:"capacity"`
	ImportConcurrency int          `json:"import_concurrency"`
	AdmissionLimit    int          `json:"admission_limit"`
	CaptureDir        string       `json:"capture_dir,omitempty"`
	CaptureSHA256     string       `json:"capture_sha256,omitempty"`
}

type PairConfig struct {
	SchemaVersion string      `json:"schema_version"`
	RunID         string      `json:"run_id"`
	SeedManifest  string      `json:"seed_manifest"`
	Suite         string      `json:"suite"`
	SeedHash      string      `json:"seed_hash"`
	SuiteSHA256   string      `json:"suite_sha256"`
	OutDir        string      `json:"out_dir"`
	Deadline      time.Time   `json:"deadline"`
	Baseline      PairRelease `json:"baseline"`
	Candidate     PairRelease `json:"candidate"`
	Scorer        PairBinary  `json:"scorer"`
}

func LoadPairConfig(path string) (PairConfig, error) {
	var c PairConfig
	if err := readPairJSON(path, &c); err != nil {
		return c, err
	}
	if c.SchemaVersion != PairSchema || !pairName.MatchString(c.RunID) || c.Deadline.IsZero() {
		return c, fmt.Errorf("paired config requires schema, bounded run identity and observation deadline")
	}
	for _, p := range []string{c.SeedManifest, c.Suite, c.OutDir, c.Scorer.Path} {
		if !filepath.IsAbs(p) {
			return c, fmt.Errorf("paired paths must be absolute")
		}
	}
	if !pairSHA.MatchString(c.SeedHash) || !pairSHA.MatchString(c.SuiteSHA256) {
		return c, fmt.Errorf("invalid paired input hash")
	}
	for side, r := range map[string]PairRelease{"baseline": c.Baseline, "candidate": c.Candidate} {
		if err := validatePairRelease(r, side == "baseline"); err != nil {
			return c, fmt.Errorf("%s: %w", side, err)
		}
	}
	if c.Candidate.CaptureDir != "" {
		return c, fmt.Errorf("candidate must be freshly imported, not reused from a capture")
	}
	if c.Baseline.Runtime.ServerContainer == c.Candidate.Runtime.ServerContainer || c.Baseline.Runtime.PostgresContainer == c.Candidate.Runtime.PostgresContainer || c.Baseline.Runtime.StateDir == c.Candidate.Runtime.StateDir {
		return c, fmt.Errorf("paired runtimes and state directories must be distinct")
	}
	for _, directories := range [][2]string{
		{c.OutDir, c.Baseline.CaptureDir},
		{c.OutDir, c.Candidate.Runtime.StateDir},
		{c.OutDir, c.Baseline.Runtime.StateDir},
		{c.Candidate.Runtime.StateDir, c.Baseline.CaptureDir},
		{c.Baseline.Runtime.StateDir, c.Baseline.CaptureDir},
	} {
		if directories[1] == "" {
			continue
		}
		output, err := resolvedPairPath(directories[0])
		if err != nil {
			return c, err
		}
		protected, err := resolvedPairPath(directories[1])
		if err != nil {
			return c, err
		}
		if output == protected || pairWithin(protected, output) || pairWithin(output, protected) {
			return c, fmt.Errorf("paired directories must not overlap: %s and %s", directories[0], directories[1])
		}
	}

	manifest, corpus, cases, qrels, dreams, suite, seedHash, err := loadRunInputs(c.SeedManifest, c.Suite)
	if err != nil {
		return c, err
	}
	if err := validateRunInputs(c.SeedManifest, manifest, corpus, cases, qrels, dreams, suite, seedHash); err != nil {
		return c, err
	}
	suiteHash, err := PairFileHash(c.Suite)
	if err != nil {
		return c, err
	}
	if seedHash != c.SeedHash || suiteHash != c.SuiteSHA256 {
		return c, fmt.Errorf("paired seed or suite identity mismatch")
	}
	if manifest.SeedID == "public_6axis_1k_v3" && (len(corpus) != 991 || len(suite) != 204 || seedHash != "sha256:0923aa14109291e5d5cf24fe419f0b6fc229823bf3dd4292e17d45447365e675") {
		return c, fmt.Errorf("frozen public cohort identity changed")
	}
	return c, nil
}

func validatePairRelease(r PairRelease, baseline bool) error {
	if r.Release == "" || !pairRevision.MatchString(r.SourceRevision) || !pairSHA.MatchString(r.ImageID) || r.Runner.SourceRevision != r.SourceRevision {
		return fmt.Errorf("release, source, image and native runner identities are required")
	}
	if r.Protocol != "adr0014" && r.Protocol != "legacy" && r.Protocol != "none" {
		return fmt.Errorf("unknown readiness protocol")
	}
	if !baseline && r.Protocol != "adr0014" {
		return fmt.Errorf("new candidate runs require ADR 0014; retain legacy runs under their recorded workflow")
	}
	if r.Protocol == "legacy" && r.CaptureDir == "" {
		return fmt.Errorf("legacy runs require a verified existing capture; their recorded workflow owns processing")
	}
	for _, id := range []string{r.Scope.TeamID, r.Scope.OwnerID, r.Scope.SpaceID, r.Runtime.CredentialID} {
		if _, err := uuid.Parse(id); err != nil {
			return fmt.Errorf("invalid runtime scope UUID")
		}
	}
	if r.Scope.Generation < 1 || r.ImportConcurrency < 1 || r.ImportConcurrency > r.AdmissionLimit || r.AdmissionLimit > MaxImportConcurrency {
		return fmt.Errorf("explicit generation and compatible import/admission limits are required")
	}
	if r.Protocol == "adr0014" && (r.Capacity.Global < 1 || r.Capacity.Global > 8 || r.Capacity.Team < 1 || r.Capacity.Team > r.Capacity.Global || r.Capacity.ContextWindowTokens < 1) {
		return fmt.Errorf("explicit global/team concurrency and model context capacity are required")
	}
	if r.Models.Provider == "" || r.Models.Remember == "" || r.Models.BackingRevision == "" || r.Models.EmbeddingModel == "" || r.Models.EmbeddingDimensions < 1 || r.Models.EmbeddingIndexStrategy == "" || !pairSHA.MatchString(r.Models.VerifierEndpointSHA256) || !pairSHA.MatchString(r.Models.EmbeddingEndpointSHA256) {
		return fmt.Errorf("model and embedding identities are required; use unknown for an unavailable backing revision")
	}
	if r.Protocol != "none" && r.Models.Ontology == "" {
		return fmt.Errorf("ontology model identity is required")
	}
	if !pairName.MatchString(r.Runtime.Project) || !pairName.MatchString(r.Runtime.ServerContainer) || !pairName.MatchString(r.Runtime.PostgresContainer) || !pairEnvName.MatchString(r.Runtime.APIKeyEnv) || !pairEnvName.MatchString(r.Runtime.ControlTokenEnv) {
		return fmt.Errorf("invalid prepared runtime identifiers or credential environment references")
	}
	for _, p := range []string{r.Runner.Path, r.Runtime.StateDir} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("runtime paths must be absolute")
		}
	}
	if r.CaptureDir != "" && !filepath.IsAbs(r.CaptureDir) {
		return fmt.Errorf("capture path must be absolute")
	}
	if r.CaptureDir != "" && !pairSHA.MatchString(r.CaptureSHA256) {
		return fmt.Errorf("reused capture requires its pinned proof hash")
	}
	for _, address := range []string{r.Runtime.BaseURL, r.Runtime.ControlURL} {
		u, err := url.Parse(address)
		if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || u.Port() == "" || u.User != nil || u.RawQuery != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("prepared runtime URLs must identify explicit local published ports without credentials")
		}
	}
	return nil
}

func VerifyPairBinary(binary PairBinary) error {
	if !pairSHA.MatchString(binary.SHA256) || !pairRevision.MatchString(binary.SourceRevision) {
		return fmt.Errorf("invalid runner source or binary identity")
	}
	hash, err := PairFileHash(binary.Path)
	if err != nil {
		return err
	}
	if hash != binary.SHA256 {
		return fmt.Errorf("runner binary hash mismatch")
	}
	info, err := buildinfo.ReadFile(binary.Path)
	if err != nil {
		return fmt.Errorf("read native runner provenance: %w", err)
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["vcs.revision"] != binary.SourceRevision || settings["vcs.modified"] != "false" {
		return fmt.Errorf("runner must be built from the pinned clean source revision")
	}
	return nil
}

func PairFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func pairBytesHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readPairJSON(path string, out any) error {
	var reader io.Reader
	if path == "-" {
		reader = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		reader = f
	}
	d := json.NewDecoder(reader)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("invalid paired JSON: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("paired JSON must contain one document")
	}
	return nil
}

func writePairJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".paired-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func pairRelease(c PairConfig, side string) (PairRelease, error) {
	switch side {
	case "baseline":
		return c.Baseline, nil
	case "candidate":
		return c.Candidate, nil
	}
	return PairRelease{}, fmt.Errorf("pair side must be baseline or candidate")
}

func pairIdentity(c PairConfig, r PairRelease) (string, error) {
	return canonicalJSONHash(struct {
		Seed, Suite string
		Release     PairRelease
		Scorer      PairBinary
	}{c.SeedHash, c.SuiteSHA256, r, c.Scorer})
}

func pairEnv(environment []string) map[string]string {
	result := map[string]string{}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}

func resolvedPairPath(path string) (string, error) {
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = resolvedPairPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}
