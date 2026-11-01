package evalharness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestPairNativeObservationRejectsMissingAndForeignState(t *testing.T) {
	c := pairedFixture(t)
	r := c.Candidate
	encode := func(v any) json.RawMessage { raw, err := json.Marshal(v); require.NoError(t, err); return raw }
	db := pairDatabaseSnapshot{ObservedAt: time.Now(), Scope: r.Scope, ScopeVerified: true, CanonicalHash: pairBytesHash(nil), SourceHash: pairBytesHash(nil), Imports: []PairImportAttempt{}, ProviderAttempts: []PairProviderAttempt{}}
	status := ontology.MaintenanceStatus{ObservedAt: time.Now(), Enabled: true, Paused: true, DiscoveryComplete: true, CoverageComplete: true,
		Window: &ontology.MaintenanceWindow{ID: "window", Policy: domain.OntologyMaintenanceConfig{MaxConcurrency: r.Capacity.Global, TeamMaxConcurrency: r.Capacity.Team, ContextWindowTokens: r.Capacity.ContextWindowTokens, Model: r.Models.Ontology}}}
	snapshot := PairNativeSnapshot{Database: encode(db), Ontology: encode(status),
		Search:    encode(map[string]any{"status": "converged", "expected_documents": 0, "current_documents": 0, "drifted_documents": 0, "contract": map[string]any{"provider": r.Models.Provider, "model": r.Models.EmbeddingModel, "dimensions": r.Models.EmbeddingDimensions, "index_strategy": r.Models.EmbeddingIndexStrategy}}),
		Community: json.RawMessage(`{"projection_coverage":{"pending_topics":0,"stale_topics":0,"failed_topics":0,"coverage_complete":true}}`)}
	o, err := DecodePairObservation(r, snapshot, true)
	require.NoError(t, err)
	ready, err := EvaluatePairReadiness(r.Protocol, o, true)
	require.NoError(t, err)
	require.True(t, ready.Ready)
	for _, field := range []string{"canonical_count", "unclaimed_evidence", "active_batches", "imports"} {
		changed := snapshot
		var object map[string]any
		require.NoError(t, json.Unmarshal(snapshot.Database, &object))
		delete(object, field)
		changed.Database = encode(object)
		_, err := DecodePairObservation(r, changed, true)
		require.ErrorContains(t, err, "missing")
	}
	db.ForeignSources = 1
	snapshot.Database = encode(db)
	_, err = DecodePairObservation(r, snapshot, true)
	require.ErrorContains(t, err, "isolated")
}

func TestPairNativeImportContractRequiresPinnedLegacyRelease(t *testing.T) {
	c := pairedFixture(t)
	row := PairImportAttempt{ContractVersion: "dense-mem.v2.6.3"}
	snapshot := commandNativeSnapshot(t, c.Candidate, []PairImportAttempt{row})
	_, err := DecodePairObservation(c.Candidate, snapshot, false)
	require.ErrorContains(t, err, "legacy import contract")
	r := c.Baseline
	r.Protocol, r.Release, r.SourceRevision = "none", "v2.6.5", "49ba9d36acaf19bde5a8ddbe7552fd04621d19bd"
	snapshot = commandNativeSnapshot(t, r, []PairImportAttempt{row})
	_, err = DecodePairObservation(r, snapshot, false)
	require.NoError(t, err)
	row.ContractVersion = "dense-mem.v2.7.0"
	snapshot = commandNativeSnapshot(t, r, []PairImportAttempt{row})
	_, err = DecodePairObservation(r, snapshot, false)
	require.ErrorContains(t, err, "pinned baseline import contract")
	row.ContractVersion = "dense-mem.v2.6.3"
	r.SourceRevision = strings.Repeat("a", 40)
	snapshot = commandNativeSnapshot(t, r, []PairImportAttempt{row})
	_, err = DecodePairObservation(r, snapshot, false)
	require.ErrorContains(t, err, "legacy import contract")
}

func TestPairNativeObservationAllowsEmptyDisabledPreparation(t *testing.T) {
	c := pairedFixture(t)
	for _, tc := range []struct {
		name      string
		fresh     bool
		part      string
		field     string
		value     any
		wantError bool
	}{
		{name: "fresh", fresh: true},
		{name: "resumed"},
		{name: "enabled", part: "ontology", field: "enabled", value: true, wantError: true},
		{name: "evidence", part: "database", field: "evidence_count", value: 1, wantError: true},
		{name: "canonical", part: "database", field: "canonical_count", value: 1, wantError: true},
		{name: "import", part: "database", field: "imports", value: []any{map[string]any{}}, wantError: true},
		{name: "provider_attempt", part: "database", field: "provider_attempts", value: []any{map[string]any{}}, wantError: true},
		{name: "batch", part: "database", field: "active_batches", value: 1, wantError: true},
		{name: "preparation", part: "database", field: "active_preparations", value: 1, wantError: true},
		{name: "projection", part: "database", field: "active_projections", value: 1, wantError: true},
		{name: "unreconciled", part: "database", field: "unreconciled_attempts", value: 1, wantError: true},
		{name: "active_work", part: "ontology", field: "active_work", value: 1, wantError: true},
		{name: "community_enabled", part: "community", field: "effective_config", value: map[string]any{"enabled": true}, wantError: true},
		{name: "legacy_active", part: "community", field: "legacy_processing_retired", value: false, wantError: true},
		{name: "missing_disabled_config", part: "community", field: "effective_config", value: nil, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := commandNativeSnapshot(t, c.Candidate, []PairImportAttempt{})
			var status map[string]any
			require.NoError(t, json.Unmarshal(snapshot.Ontology, &status))
			status["enabled"] = false
			status["configuration_error"] = "configuration_invalid"
			raw, err := json.Marshal(status)
			require.NoError(t, err)
			snapshot.Ontology = raw
			snapshot.Community = json.RawMessage(`{"legacy_processing_retired":true,"effective_config":{"enabled":false}}`)
			if tc.part != "" {
				parts := map[string]*json.RawMessage{"database": &snapshot.Database, "ontology": &snapshot.Ontology, "community": &snapshot.Community}
				var object map[string]any
				require.NoError(t, json.Unmarshal(*parts[tc.part], &object))
				object[tc.field] = tc.value
				raw, err := json.Marshal(object)
				require.NoError(t, err)
				*parts[tc.part] = raw
			}
			o, err := DecodePairObservation(c.Candidate, snapshot, tc.fresh)
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.False(t, o.ProjectionCoverageComplete)
			ready, err := EvaluatePairReadiness(c.Candidate.Protocol, o, true)
			require.ErrorContains(t, err, "ontology must remain enabled")
			require.False(t, ready.Ready)
		})
	}
}

func TestPairRuntimeIdentityAndRunnerProvenance(t *testing.T) {
	r := pairedFixture(t).Candidate
	server := pairContainer{ID: "server", Image: r.ImageID}
	server.State.Running = true
	server.Config.Labels = map[string]string{"com.docker.compose.project": r.Runtime.Project, "org.opencontainers.image.revision": r.SourceRevision, "org.opencontainers.image.version": r.Release}
	server.Config.Env = []string{"AI_API_URL=http://provider", "AI_VERIFIER_MODEL=fixture", "AI_API_EMBEDDING_MODEL=embedding", "AI_API_EMBEDDING_DIMENSIONS=3"}
	require.NoError(t, json.Unmarshal([]byte(`{"Ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"18080"}],"8090/tcp":[{"HostIp":"127.0.0.1","HostPort":"18090"}]}}`), &server.NetworkSettings))
	postgres := server
	postgres.ID = "postgres"
	encode := func() []byte {
		raw, err := json.Marshal([]pairContainer{server, postgres})
		require.NoError(t, err)
		return raw
	}
	require.NoError(t, VerifyPairContainers(r, encode()))
	delete(server.Config.Labels, "org.opencontainers.image.version")
	require.ErrorContains(t, VerifyPairContainers(r, encode()), "identity")
	server.Config.Labels["org.opencontainers.image.version"] = "wrong"
	require.ErrorContains(t, VerifyPairContainers(r, encode()), "identity")
	server.Config.Labels["org.opencontainers.image.version"] = r.Release
	server.Image = "different"
	require.ErrorContains(t, VerifyPairContainers(r, encode()), "identity")
	raw, err := json.Marshal(map[string]any{"data": map[string]any{"team": map[string]any{"id": r.Scope.TeamID}, "credential": map[string]any{"id": r.Runtime.CredentialID, "team_id": r.Scope.TeamID}, "memory_access": map[string]any{"team_shared": map[string]any{"write": true}}}})
	require.NoError(t, err)
	require.NoError(t, VerifyPairSession(r, raw))
	r.Runtime.CredentialID = "different"
	require.Error(t, VerifyPairSession(r, raw))
	binary := r.Runner
	binary.Path = filepath.Join(t.TempDir(), "stale-runner")
	require.NoError(t, os.WriteFile(binary.Path, []byte("not a Go runner"), 0o700))
	require.ErrorContains(t, VerifyPairBinary(binary), "hash mismatch")
	binary.SHA256, err = PairFileHash(binary.Path)
	require.NoError(t, err)
	require.ErrorContains(t, VerifyPairBinary(binary), "provenance")
}

func TestPairProtocolRequiresNativeApplicability(t *testing.T) {
	r := pairedFixture(t).Baseline
	r.Protocol = "none"
	require.NoError(t, VerifyPairProtocol(r, 404, nil))
	for _, status := range []int{200, 401, 500} {
		require.Error(t, VerifyPairProtocol(r, status, []byte(`{"enabled":false}`)))
	}
	r.Protocol = "adr0014"
	raw, err := json.Marshal(map[string]any{"data": ontology.MaintenanceStatus{}})
	require.NoError(t, err)
	require.NoError(t, VerifyPairProtocol(r, 200, raw))
	require.Error(t, VerifyPairProtocol(r, 404, nil))
	require.Error(t, VerifyPairProtocol(r, 200, []byte(`{"enabled":true}`)))
}

func TestPairNativeObservationRejectsUntrustedWireValues(t *testing.T) {
	cases := []struct {
		name string
		part string
		edit func(map[string]any)
	}{
		{"database_type", "database", func(m map[string]any) { m["evidence_count"] = "unknown" }},
		{"unclaimed_evidence", "database", func(m map[string]any) { m["unclaimed_evidence"] = 1 }},
		{"missing_fingerprint", "database", func(m map[string]any) { m["source_hash"] = "" }},
		{"window_changed", "database", func(m map[string]any) { m["legacy_window"] = map[string]any{"id": "different-window"} }},
		{"provider_receipt_identity", "database", func(m map[string]any) { m["provider_attempts"] = []any{map[string]any{}} }},
		{"search_type", "search", func(m map[string]any) { m["expected_documents"] = "unknown" }},
		{"embedding_contract", "search", func(m map[string]any) { m["contract"].(map[string]any)["model"] = "different-model" }},
		{"ontology_type", "ontology", func(m map[string]any) { m["active_work"] = "unknown" }},
		{"configuration_failure", "ontology", func(m map[string]any) { m["configuration_error"] = "invalid" }},
		{"community_type", "community", func(m map[string]any) { m["projection_coverage"].(map[string]any)["pending_topics"] = "unknown" }},
		{"community_missing", "community", func(m map[string]any) { delete(m, "projection_coverage") }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			c := pairedFixture(t)
			snapshot := commandNativeSnapshot(t, c.Candidate, []PairImportAttempt{})
			parts := map[string]*json.RawMessage{"database": &snapshot.Database, "search": &snapshot.Search, "ontology": &snapshot.Ontology, "community": &snapshot.Community}
			var object map[string]any
			require.NoError(t, json.Unmarshal(*parts[test.part], &object))
			test.edit(object)
			raw, err := json.Marshal(object)
			require.NoError(t, err)
			*parts[test.part] = raw
			_, err = DecodePairObservation(c.Candidate, snapshot, false)
			require.Error(t, err)
		})
	}
	c := pairedFixture(t)
	snapshot := commandNativeSnapshot(t, c.Baseline, []PairImportAttempt{})
	c.Baseline.Protocol = "none"
	o, err := DecodePairObservation(c.Baseline, snapshot, false)
	require.NoError(t, err)
	require.True(t, o.CoverageComplete)
	require.True(t, o.ProjectionCoverageComplete)
	c.Baseline.Protocol = "legacy"
	_, err = DecodePairObservation(c.Baseline, snapshot, false)
	require.ErrorContains(t, err, "recorded workflow")
}

func TestPairSessionVerifiesPinnedBaselineBinding(t *testing.T) {
	r := pairedFixture(t).Baseline
	r.Protocol, r.Release = "none", "v2.6.5"
	r.SourceRevision = "49ba9d36acaf19bde5a8ddbe7552fd04621d19bd"
	data := func() map[string]any {
		return map[string]any{
			"team":       map[string]any{"id": r.Scope.TeamID},
			"membership": map[string]any{"team_id": r.Scope.TeamID, "grants": []string{"read", "write"}},
			"credential": map[string]any{"id": r.Runtime.CredentialID, "team_id": r.Scope.TeamID,
				"scopes": []string{"read", "write"}, "memory_binding": "shared_only", "memory_space_kind": "team_shared"},
		}
	}
	encode := func(value map[string]any) []byte {
		raw, err := json.Marshal(map[string]any{"data": value})
		require.NoError(t, err)
		return raw
	}
	require.NoError(t, VerifyPairSession(r, encode(data())))
	for _, test := range []struct {
		name, group, field string
		value              any
	}{
		{"foreign_team", "team", "id", "00000000-0000-4000-8000-000000000099"},
		{"foreign_membership", "membership", "team_id", "00000000-0000-4000-8000-000000000099"},
		{"foreign_credential_team", "credential", "team_id", "00000000-0000-4000-8000-000000000099"},
		{"different_owner", "credential", "id", "00000000-0000-4000-8000-000000000099"},
		{"readonly_membership", "membership", "grants", []string{"read"}},
		{"readonly_credential", "credential", "scopes", []string{"read"}},
		{"private_binding", "credential", "memory_binding", "credential_private"},
		{"private_space", "credential", "memory_space_kind", "credential_private"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := data()
			value[test.group].(map[string]any)[test.field] = test.value
			require.Error(t, VerifyPairSession(r, encode(value)))
		})
	}
	for _, change := range []func(*PairRelease){
		func(r *PairRelease) { r.Protocol = "adr0014" },
		func(r *PairRelease) { r.Release = "v2.6.4" },
		func(r *PairRelease) { r.SourceRevision = "0000000000000000000000000000000000000001" },
	} {
		changed := r
		change(&changed)
		require.Error(t, VerifyPairSession(changed, encode(data())), "legacy shape requires the exact pinned native baseline")
	}
	value := data()
	value["memory_access"] = map[string]any{"team_shared": map[string]any{"write": true}}
	require.Error(t, VerifyPairSession(r, encode(value)), "the pinned legacy source must retain its native session shape")
}
