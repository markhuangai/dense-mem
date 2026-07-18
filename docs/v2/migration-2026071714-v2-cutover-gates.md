# Migration 2026071714: V2 Cutover Gates

## Scope

Adds the cutover enforcement layer for the V2 legacy-corpus migration:

- stores the final gate report hash on `v2_migration_runs`
- versions each `v2_migration_gate_results` row
- enforces a single compatible `v2_cutover` marker
- adds private control actions to finalize gate results and commit cutover
- accepts `V2_BOOT_MODE=active` as a marker-gated V2 runtime mode; startup
  fails before data-plane wiring when the compatible marker is absent

This migration does not remove Neo4j code, configuration, drivers, or Compose
services. That is the `2.2.0` removal boundary.

## Wiki Contract

This implements the Release Process hard-gate invariant: a generated report
alone cannot authorize cutover. The marker transaction rechecks the current
PostgreSQL migration state before writing the compatible marker.

Required gate rows must include:

```text
gate_name
outcome = pass
evidence_ref
evidence_hash
gate_version
```

The `release_gate_1k` evidence is a local/on-demand evaluation artifact. Seeds,
run results, and generated comparisons stay under ignored local evaluation
paths; release evidence records only the evidence reference and hash.

## Cutover Checks

The repository transaction blocks cutover when:

- any required gate is missing, failed, warning-only, or lacks evidence metadata
- any corpus item is still `pending` or `failed`
- any migration exclusion has `blocks_cutover=true`
- any excluded corpus item lacks an exclusion manifest row
- a compatible cutover marker already exists
- the final corpus hash or gate report hash changed between gate finalization
  and marker commit

## Control API

The private control listener adds:

```text
POST /control/api/v2/migration/finalize-gates
POST /control/api/v2/migration/cutover
```

`finalize-gates` writes the machine-readable gate results and moves the run to
`ready_to_cutover` only when the database predicates pass. `cutover` writes the
compatible marker and moves the run to `cut_over` in one PostgreSQL transaction.

## Rollback Boundary

Before the cutover marker is written, a failed gate leaves the server in
recoverable maintenance state with the run at `verifying` and a bounded
`last_error`. After the compatible marker is written, PostgreSQL is
authoritative; recovery uses PostgreSQL-compatible forward fixes or rollback
builds, never Neo4j authority.

## Verification

Focused checks:

```bash
go test ./internal/service/migrationcontrol ./internal/http ./internal/config ./cmd/server -count=1
go test ./internal/repository -run TestV2MigrationControlRepository -count=1
```

Run the local 1k release gate on demand when the local seed/import is available;
do not require generated seed artifacts in remote CI.
