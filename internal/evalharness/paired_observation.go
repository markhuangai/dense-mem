package evalharness

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

// ErrPairSnapshotWindowChanged identifies a verified window rollover between native reads.
var ErrPairSnapshotWindowChanged = errors.New("window changed while collecting the native snapshot; collect again before proceeding")

//go:embed paired_import_snapshot.sql
var PairImportSnapshotSQL string

//go:embed paired_ontology_snapshot.sql
var PairOntologySnapshotSQL string

type pairDatabaseSnapshot struct {
	ObservedAt         time.Time             `json:"observed_at"`
	Scope              PairScope             `json:"scope"`
	ScopeVerified      bool                  `json:"scope_verified"`
	ForeignSources     int64                 `json:"foreign_sources"`
	ForeignWork        int64                 `json:"foreign_work"`
	EvidenceCount      int64                 `json:"evidence_count"`
	CanonicalCount     int64                 `json:"canonical_count"`
	UnclaimedEvidence  int64                 `json:"unclaimed_evidence"`
	CanonicalHash      string                `json:"canonical_hash"`
	SourceHash         string                `json:"source_hash"`
	Imports            []PairImportAttempt   `json:"imports"`
	ActiveBatches      int64                 `json:"active_batches"`
	ActivePreparations int64                 `json:"active_preparations"`
	ActiveProjections  int64                 `json:"active_projections"`
	Unreconciled       int64                 `json:"unreconciled_attempts"`
	BudgetDeferred     int64                 `json:"budget_deferred"`
	ProviderAttempts   []PairProviderAttempt `json:"provider_attempts"`
	LegacyWindow       *struct {
		ID             string `json:"id"`
		ReservedInput  int64  `json:"reserved_input"`
		ReservedOutput int64  `json:"reserved_output"`
		Overrun        bool   `json:"overrun"`
	} `json:"legacy_window"`
}

type PairNativeSnapshot struct {
	Database  json.RawMessage `json:"database"`
	Ontology  json.RawMessage `json:"ontology"`
	Community json.RawMessage `json:"community"`
	Search    json.RawMessage `json:"search"`
}

func DecodePairObservation(release PairRelease, snapshot PairNativeSnapshot, fresh bool) (PairObservation, error) {
	o := PairObservation{}
	if err := requirePairFields(snapshot.Database, "observed_at", "scope", "scope_verified", "foreign_sources", "evidence_count", "canonical_count", "unclaimed_evidence", "canonical_hash", "source_hash", "imports"); err != nil {
		return o, err
	}
	var db pairDatabaseSnapshot
	if err := json.Unmarshal(snapshot.Database, &db); err != nil {
		return o, err
	}
	if !db.ScopeVerified || db.Scope != release.Scope || db.ForeignSources != 0 || db.ForeignWork != 0 {
		return o, fmt.Errorf("prepared runtime is not isolated to the expected active team, credential owner and generation")
	}
	if fresh && (db.CanonicalCount != 0 || db.EvidenceCount != 0 || len(db.Imports) != 0) {
		return o, fmt.Errorf("candidate or uncaptured baseline requires a fresh runtime")
	}
	if db.UnclaimedEvidence != 0 {
		return o, fmt.Errorf("runtime contains evidence outside its paired import receipts")
	}
	if !pairSHA.MatchString(db.CanonicalHash) || !pairSHA.MatchString(db.SourceHash) {
		return o, fmt.Errorf("missing database fingerprints")
	}
	o.ObservedAt, o.Scope = db.ObservedAt, db.Scope
	o.SourceHash, o.CanonicalHash, o.Imports = db.SourceHash, db.CanonicalHash, db.Imports
	if release.Mode == PairModeRetainedBaseline {
		return o, fmt.Errorf("retained baseline is offline only")
	}
	for _, row := range db.Imports {
		if row.ContractVersion != "dense-mem.v3.0.0" {
			return o, fmt.Errorf("import contract does not match the Evidence-only runtime")
		}
	}
	if err := requirePairFields(snapshot.Search, "status", "expected_documents", "current_documents", "drifted_documents"); err != nil {
		return o, err
	}
	var search struct {
		Status   string `json:"status"`
		Expected int64  `json:"expected_documents"`
		Current  int64  `json:"current_documents"`
		Drifted  int64  `json:"drifted_documents"`
		Contract *struct {
			Provider   string `json:"provider"`
			Model      string `json:"model"`
			Dimensions int    `json:"dimensions"`
			Strategy   string `json:"index_strategy"`
		} `json:"contract"`
	}
	if err := json.Unmarshal(snapshot.Search, &search); err != nil {
		return o, err
	}
	if search.Contract != nil && (search.Contract.Provider != release.Models.Provider || search.Contract.Model != release.Models.EmbeddingModel || search.Contract.Dimensions != release.Models.EmbeddingDimensions || search.Contract.Strategy != release.Models.EmbeddingIndexStrategy) {
		return o, fmt.Errorf("native embedding contract differs from the paired identity")
	}
	o.SearchConverged = search.Contract != nil && search.Status == "converged" && search.Drifted == 0 && search.Expected == search.Current
	if release.Protocol == "none" {
		o.CoverageComplete, o.ProjectionCoverageComplete = true, true
		return o, nil
	}
	if release.Protocol != PairModeEvidenceOnlyV5 {
		return o, fmt.Errorf("legacy processing remains owned by its recorded workflow")
	}
	if err := requirePairFields(snapshot.Ontology, "observed_at", "enabled", "paused", "discovery_complete", "coverage_complete", "counts.eligible", "counts.organized", "counts.pending", "counts.failed", "counts.ambiguous", "active_work", "usage.attempts", "usage.unreconciled_attempts", "usage.unavailable_usage_attempts", "usage.partial_usage_attempts", "usage.reported_input_tokens", "usage.reported_output_tokens", "usage.reported_total_tokens", "historical_usage"); err != nil {
		return o, err
	}
	if err := requirePairFields(snapshot.Database, "active_batches", "active_preparations", "unreconciled_attempts", "budget_deferred", "foreign_work", "provider_attempts"); err != nil {
		return o, err
	}
	var status ontology.MaintenanceStatus
	if err := json.Unmarshal(snapshot.Ontology, &status); err != nil {
		return o, err
	}
	preparing := !status.Enabled && db.CanonicalCount == 0 && db.EvidenceCount == 0 && len(db.Imports) == 0 &&
		status.ActiveWork == 0 && db.ActiveBatches == 0 && db.ActivePreparations == 0 && db.ActiveProjections == 0 &&
		db.Unreconciled == 0 && len(db.ProviderAttempts) == 0
	if status.ConfigurationError != "" && !fresh && !preparing {
		return o, fmt.Errorf("native ontology configuration is invalid")
	}
	o.Enabled, o.Paused = status.Enabled, status.Paused
	o.DiscoveryComplete, o.CoverageComplete = status.DiscoveryComplete, status.CoverageComplete
	o.Eligible, o.Organized = status.Counts.Eligible, status.Counts.Organized
	o.Pending, o.Failed, o.Ambiguous = status.Counts.Pending, status.Counts.Failed, status.Counts.Ambiguous
	o.ActiveWork, o.ActiveBatches, o.ActivePreparations = int64(status.ActiveWork), db.ActiveBatches, db.ActivePreparations
	o.Usage, o.HistoricalUsage = pairUsage(status.Usage), pairUsage(status.HistoricalUsage)
	o.Usage.Unreconciled = max(o.Usage.Unreconciled, db.Unreconciled)
	o.BudgetDeferred, o.ProviderAttempts = db.BudgetDeferred, db.ProviderAttempts
	if status.Window != nil {
		policy := status.Window.Policy
		o.WindowID, o.Model = status.Window.ID, policy.Model
		o.Policy = PairCapacity{Global: policy.MaxConcurrency, Team: policy.TeamConcurrency(release.Scope.TeamID), ContextWindowTokens: policy.ContextWindowTokens}
	}
	if _, err := MeasurePairAttempts(o.ProviderAttempts); err != nil {
		return o, err
	}
	if db.LegacyWindow != nil {
		o.ReservedInput, o.ReservedOutput, o.Overrun = db.LegacyWindow.ReservedInput, db.LegacyWindow.ReservedOutput, db.LegacyWindow.Overrun
		if o.WindowID != db.LegacyWindow.ID {
			oldID, oldErr := uuid.Parse(db.LegacyWindow.ID)
			newID, newErr := uuid.Parse(o.WindowID)
			if oldErr == nil && newErr == nil && oldID != uuid.Nil && newID != uuid.Nil && oldID != newID &&
				!db.ObservedAt.IsZero() && status.Window != nil &&
				db.ObservedAt.Before(status.Window.StartsAt) &&
				!status.ObservedAt.Before(status.Window.StartsAt) && status.ObservedAt.Before(status.Window.EndsAt) {
				return o, fmt.Errorf("%w: database_window=%s database_observed_at=%s status_window=%s status_observed_at=%s window_starts_at=%s",
					ErrPairSnapshotWindowChanged, oldID, db.ObservedAt.Format(time.RFC3339Nano), newID,
					status.ObservedAt.Format(time.RFC3339Nano), status.Window.StartsAt.Format(time.RFC3339Nano))
			}
			return o, fmt.Errorf("window changed while collecting the native snapshot; collect again before proceeding")
		}
	}
	return o, nil
}

func pairUsage(u ontology.MaintenanceUsage) PairUsage {
	return PairUsage{Attempts: u.Attempts, Unreconciled: u.UnreconciledAttempts, Unavailable: u.UnavailableUsageAttempts, Partial: u.PartialUsageAttempts, Input: u.ReportedInputTokens, Output: u.ReportedOutputTokens, Total: u.ReportedTotalTokens}
}

func requirePairFields(raw []byte, paths ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("invalid native observation: %w", err)
	}
	for _, path := range paths {
		current := object
		parts := strings.Split(path, ".")
		for i, part := range parts {
			value, ok := current[part]
			if !ok || string(value) == "null" {
				return fmt.Errorf("native observation missing %s", path)
			}
			if i != len(parts)-1 {
				if err := json.Unmarshal(value, &current); err != nil {
					return fmt.Errorf("invalid native field %s", path)
				}
			}
		}
	}
	return nil
}

type pairContainer struct {
	ID     string                 `json:"Id"`
	Image  string                 `json:"Image"`
	State  struct{ Running bool } `json:"State"`
	Config struct {
		Env    []string
		Labels map[string]string
	} `json:"Config"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string
		}
	} `json:"NetworkSettings"`
}

func VerifyPairContainers(release PairRelease, raw []byte) error {
	var containers []pairContainer
	if err := json.Unmarshal(raw, &containers); err != nil {
		return fmt.Errorf("invalid container inspection")
	}
	if len(containers) != 2 {
		return fmt.Errorf("inspect exactly the prepared server and PostgreSQL containers")
	}
	server, postgres := containers[0], containers[1]
	for _, c := range containers {
		if !c.State.Running || c.Config.Labels["com.docker.compose.project"] != release.Runtime.Project {
			return fmt.Errorf("prepared container is stopped or belongs to another Compose project")
		}
	}
	if server.ID == postgres.ID || server.Config.Labels["org.opencontainers.image.version"] != release.Release || server.Image != release.ImageID || server.Config.Labels["org.opencontainers.image.revision"] != release.SourceRevision {
		return fmt.Errorf("prepared image/source identity mismatch")
	}
	for port, address := range map[string]string{"8080/tcp": release.Runtime.BaseURL, "8090/tcp": release.Runtime.ControlURL} {
		u, err := url.Parse(address)
		if err != nil {
			return err
		}
		found := false
		for _, binding := range server.NetworkSettings.Ports[port] {
			if binding.HostPort == u.Port() && (binding.HostIP == "127.0.0.1" || binding.HostIP == "::1") {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("runtime URL is not the inspected server's local published port")
		}
	}
	env := pairEnv(server.Config.Env)
	embeddingURL := env["AI_API_URL"]
	verifierURL := env["AI_VERIFIER_API_URL"]
	if verifierURL == "" {
		verifierURL = embeddingURL
	}
	remember := strings.TrimSpace(env["AI_REMEMBER_MODEL"])
	if remember == "" {
		remember = strings.TrimSpace(env["AI_VERIFIER_MODEL"])
	}
	dimensions := 3072
	if raw := env["AI_API_EMBEDDING_DIMENSIONS"]; raw != "" && raw != "0" {
		var err error
		dimensions, err = strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("invalid prepared embedding dimensions")
		}
	}
	if remember != release.Models.Remember || env["AI_API_EMBEDDING_MODEL"] != release.Models.EmbeddingModel || dimensions != release.Models.EmbeddingDimensions || pairBytesHash([]byte(verifierURL)) != release.Models.VerifierEndpointSHA256 || pairBytesHash([]byte(embeddingURL)) != release.Models.EmbeddingEndpointSHA256 {
		return fmt.Errorf("prepared model or provider endpoint differs from paired identity")
	}
	pool := 25
	if raw := env["POSTGRES_MAX_OPEN_CONNS"]; raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("invalid prepared pool capacity")
		}
		pool = value
	}
	if pool > 0 && release.AdmissionLimit >= pool {
		return fmt.Errorf("declared admission limit leaves no application pool capacity")
	}
	return nil
}

func VerifyPairSession(release PairRelease, raw []byte) error {
	if release.Mode == PairModeRetainedBaseline {
		return fmt.Errorf("retained baseline is offline only")
	}

	var session struct {
		Data struct {
			Team struct {
				ID string `json:"id"`
			} `json:"team"`
			Membership struct {
				TeamID string   `json:"team_id"`
				Grants []string `json:"grants"`
			} `json:"membership"`
			Credential *struct {
				ID              string   `json:"id"`
				TeamID          string   `json:"team_id"`
				Scopes          []string `json:"scopes"`
				MemoryBinding   string   `json:"memory_binding"`
				MemorySpaceKind string   `json:"memory_space_kind"`
			} `json:"credential"`
			MemoryAccess *struct {
				Shared struct {
					Write bool `json:"write"`
				} `json:"team_shared"`
			} `json:"memory_access"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		return fmt.Errorf("invalid authenticated runtime identity")
	}
	s := session.Data
	if s.Team.ID != release.Scope.TeamID || s.Credential == nil || s.Credential.ID != release.Runtime.CredentialID || s.Credential.TeamID != release.Scope.TeamID {
		return fmt.Errorf("credential does not authenticate the prepared team with shared write access")
	}
	sharedWrite := s.MemoryAccess != nil && s.MemoryAccess.Shared.Write
	if !sharedWrite {
		return fmt.Errorf("credential does not authenticate the prepared team with shared write access")
	}
	return nil
}

func VerifyPairProtocol(release PairRelease, status int, body []byte) error {
	if release.Protocol == "none" {
		if status != 404 {
			return fmt.Errorf("protocol none requires a runtime without the ontology interface")
		}
		return nil
	}
	if release.Protocol != PairModeEvidenceOnlyV5 || status != 200 {
		return fmt.Errorf("prepared runtime does not expose its declared readiness protocol")
	}
	var response struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("invalid native protocol response: %w", err)
	}
	return requirePairFields(response.Data, "enabled", "paused", "discovery_complete", "coverage_complete", "active_work", "usage.unreconciled_attempts", "historical_usage")
}
