package migrationsupervisor

const migrationGateEvidenceVersion = "dense-mem.v2.1.migration-gates.v1"

type releaseEvidenceEntry struct {
	message  string
	criteria string
	sources  []string
}

var staticReleaseEvidence = map[string]releaseEvidenceEntry{
	"postgres_schema_topology": {
		message:  "PostgreSQL V2 schema, pgvector search, and migration tables are release-gated.",
		criteria: "Committed PostgreSQL migrations and static schema checks define the V2 authority topology.",
		sources: []string{
			"migrations/postgres",
			"internal/storage/postgres/schema_test.go",
			"internal/storage/postgres/migration_order_test.go",
			"internal/repository/v2_migration_control_repository_integration_test.go",
			"internal/repository/v2_search_repository_integration_test.go",
			"docs/v2/pr71-parity-ledger.tsv",
		},
	},
	"tenant_integrity": {
		message:  "Tenant-scoped repositories and migration RLS paths are release-gated.",
		criteria: "Repository tests cover team/profile scoping before IDs leave storage adapters.",
		sources: []string{
			"internal/repository/v2_migration_control_repository_integration_test.go",
			"internal/repository/profile_repository_test.go",
			"docs/v2/pr71-parity-ledger.tsv",
		},
	},
	"team_owner_reconciliation": {
		message:  "Legacy owner/profile pairs are reconciled against PostgreSQL before migration submit.",
		criteria: "The executor records owner mismatches as cutover-blocking exclusions.",
		sources: []string{
			"internal/service/migrationexecutor/service_test.go",
			"internal/repository/v2_migration_control_repository_integration_test.go",
		},
	},
	"exclusion_manifest": {
		message:  "Cutover-blocking exclusions are durable and included in migration finalization.",
		criteria: "Migration progress tables persist exclusions and finalization waits for blocking state.",
		sources: []string{
			"internal/service/migrationexecutor/service_test.go",
			"internal/repository/v2_migration_control_repository_integration_test.go",
		},
	},
	"rls_security": {
		message:  "Migration state is available only in system or migration transaction modes.",
		criteria: "Forced-RLS migration tables reject request/profile access paths.",
		sources: []string{
			"internal/storage/postgres/client_test.go",
			"internal/repository/v2_migration_control_repository_integration_test.go",
			"docs/v2/migration-2026071909-v2-migration-control-plane.md",
		},
	},
	"relationship_identity_uniqueness": {
		message:  "V2 relationship identity uses deterministic profile-owned identity policy.",
		criteria: "Claim identity tests cover relationship identity stability and collision boundaries.",
		sources: []string{
			"internal/service/claimidentity/identity.go",
			"internal/service/claimidentity/identity_test.go",
			"internal/repository/v2_placement_commit_repository_test.go",
		},
	},
	"active_endpoint_integrity": {
		message:  "Active MCP and browser endpoints use the V2 registry and HTTP contracts after cutover.",
		criteria: "Contract tests cover tool visibility, schema validation, and endpoint routing.",
		sources: []string{
			"internal/mcp/server_v2_contract_test.go",
			"internal/tools/registry/v2_contract_uat_test.go",
			"web/tests/control-portal.spec.ts",
		},
	},
	"relationship_owner_preservation": {
		message:  "Relationship writes preserve authenticated owner authority.",
		criteria: "Placement tests use same-team visibility without widening mutation authority.",
		sources: []string{
			"internal/service/memoryservice/placement_test.go",
			"internal/repository/v2_placement_commit_repository_integration_test.go",
			"internal/repository/v2_placement_resolution_actions_integration_test.go",
		},
	},
	"inactive_candidate_review_boundary": {
		message:  "Candidates and hypotheses remain outside active recall and graph reads.",
		criteria: "Placement and registry tests keep inactive review state out of default reads.",
		sources: []string{
			"internal/service/memoryservice/v2_semantic_placement_worker_test.go",
			"internal/tools/registry/v2_contract_read_outputs.go",
			"internal/tools/registry/v2_contract_output_validation_test.go",
		},
	},
	"predicate_identity_hold_policy": {
		message:  "Predicate identity and hold policy are enforced before durable semantic promotion.",
		criteria: "Identity and placement policy tests cover duplicate, hold, and lifecycle boundaries.",
		sources: []string{
			"internal/service/claimidentity/identity_test.go",
			"internal/repository/v2_placement_commit_repository_test.go",
			"internal/service/invariant_scan_service_test.go",
		},
	},
	"recall_oracle_hnsw": {
		message:  "Recall ranking and HNSW fallback contracts are covered by the deterministic release gate.",
		criteria: "The committed release policy requires the approved public six-axis 1k baseline.",
		sources: []string{
			"tests/eval/baselines/v2.1.1_public_6axis_1k_baseline.json",
			"internal/evalharness/release_gate_test.go",
			"internal/repository/v2_search_repository_integration_test.go",
		},
	},
	"trace_lineage": {
		message:  "Trace reads preserve PostgreSQL lineage and do not treat read models as authority.",
		criteria: "Trace and graph-view tests cover source lineage and read-model hydration.",
		sources: []string{
			"internal/repository/v2_semantic_trace_graph_integration_test.go",
			"internal/service/graphview/graphview_test.go",
			"internal/service/contextservice/context_test.go",
		},
	},
	"v2_smoke_lifecycle": {
		message:  "V2 remember, placement, lifecycle, and read contracts have smoke coverage.",
		criteria: "Service and registry tests cover accepted placement through active reads.",
		sources: []string{
			"internal/service/memoryservice/placement_test.go",
			"internal/tools/registry/v2_uat_placement_test.go",
			"tests/uat/e2e-journey.spec.ts",
		},
	},
	"abc_visibility_mutation": {
		message:  "A/B/C actor visibility and mutation authority are covered by release tests.",
		criteria: "Different-owner tests cover same-team reads separately from mutation authority.",
		sources: []string{
			"internal/repository/v2_placement_resolution_actions_integration_test.go",
			"internal/tools/registry/v2_contract_uat_test.go",
			"tests/uat/auth-matrix.spec.ts",
		},
	},
	"release_gate_1k": {
		message:  "The V2 release train includes the hard deterministic 1k public evaluation gate.",
		criteria: "The committed gate policy must match the approved baseline without weaker thresholds.",
		sources: []string{
			"tests/eval/baselines/v2.1.1_public_6axis_1k_baseline.json",
			"internal/evalharness/release_gate_test.go",
			"tests/eval/README.md",
		},
	},
	"uat_mcp_http_browser": {
		message:  "MCP, HTTP, and browser UAT surfaces are covered by committed release checks.",
		criteria: "UAT tests cover control portal, MCP contract, browser journey, and auth matrix.",
		sources: []string{
			"tests/uat/phase8-mcp.spec.ts",
			"tests/uat/control-portal-live.spec.ts",
			"web/tests/control-portal.spec.ts",
		},
	},
	"compose_rehearsal": {
		message:  "Compose rehearsal coverage is part of the V2 cutover evidence set.",
		criteria: "Compose tests cover production-like wiring and optional Redis behavior.",
		sources: []string{
			"tests/compose/demo_e2e_test.go",
			"tests/compose/compose_optional_redis_test.go",
			"web/tests-compose/compose-portal.spec.ts",
		},
	},
	"restart_rehearsal": {
		message:  "Startup and restart behavior is covered by boot-classifier and server tests.",
		criteria: "Boot tests cover marker-driven authority selection and post-cutover restart state.",
		sources: []string{
			"cmd/server/v2_authority_bootstrap_test.go",
			"cmd/server/v2_active_server_test.go",
			"internal/service/migrationsupervisor/service_test.go",
		},
	},
	"rollback_compatibility": {
		message:  "Rollback compatibility is bounded by operator backup confirmation.",
		criteria: "Control-plane docs and tests require the operator to confirm PostgreSQL and Neo4j backups; Dense-Mem does not create or verify them.",
		sources: []string{
			"docs/v2/migration-2026071909-v2-migration-control-plane.md",
			"internal/service/migrationcontrol/service_test.go",
			"internal/http/control_portal_migration_test.go",
		},
	},
	"no_neo4j_fallback": {
		message:  "Neo4j is legacy migration input only and absent from active V2 request paths.",
		criteria: "Boot classifier and V2 service wiring reject Neo4j fallback reads after cutover.",
		sources: []string{
			"cmd/server/v2_authority_bootstrap.go",
			"cmd/server/v2_active_server.go",
			"internal/service/migrationexecutor/service.go",
		},
	},
}

func embeddedReleaseEvidence(name string) (GateEvidence, bool) {
	entry, ok := staticReleaseEvidence[name]
	if !ok {
		return GateEvidence{}, false
	}
	details := map[string]any{
		"version":        migrationGateEvidenceVersion,
		"gate":           name,
		"evidence_basis": "embedded_release_evidence",
		"criteria":       entry.criteria,
		"source_refs":    append([]string(nil), entry.sources...),
	}
	return GateEvidence{
		Ref:     "dense-mem://migration/release-evidence/" + name,
		Message: entry.message,
		Details: details,
	}, true
}
