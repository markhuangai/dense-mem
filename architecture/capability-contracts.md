# Capability contract ownership

The schema-version-2 architecture manifest is assembled from capability
fragments under `architecture/modules/`. Each fragment owns the exact source
units, worker anchors, and database-case registrations for one capability. The
central manifest owns discovery, profiles, and fragment inventory. The loader
rejects missing, duplicate, unlisted, or undiscovered units.

The dependency role matrix is defined once by `architecture/manifest.mjs` and
is injected into the merged manifest. It is not copied into the JSON inventory.

The dependency direction is transport to application to domain and ports.
PostgreSQL adapters implement capability ports and may use only domain, ports,
and the shared PostgreSQL infrastructure. Composition constructs private
adapters. Cross-capability imports require a public target; private units stay
inside their owning capability.

## Native owners

- `internal/remember/service` stages exact Evidence and processing intent, invokes
  assessment and required embeddings outside the authoritative transaction, and
  orchestrates atomic acceptance through Knowledge ports. `internal/assessor`
  validates complete security, equivalence, and Evidence-conflict decisions.
- `internal/knowledge/contract` and `internal/knowledge/postgres` own Evidence,
  occurrences, source revisions, lifecycle, conflicts, search documents, and
  reconciliation writes. Scoped operation keys and request hashes determine
  replay. Retired contracts remain non-retryable historical outcomes.
- `internal/session` owns exact current-event excerpt selection and original
  Unicode offsets. Prior context is read-only. Session acceptance uses the same
  Evidence assessment and atomic commit owners as Remember.
- `internal/ontology/contract` owns organizational topics, direct Evidence
  memberships, equivalence, manager overrides, dependency fingerprints, and
  publication policy. Its PostgreSQL adapter implements current shared-source
  reads, maintenance coordination, and atomic publications. Organization never
  writes submitted Evidence or establishes factual support.
- `internal/recall` owns Evidence candidate fusion, ranking, known-result
  suppression, current topic hints, and direct-topic follow-up. Its adapter
  scopes selection and counting before hydration and reauthorizes hydrated data.
- `internal/trace` owns Evidence provenance and lifecycle budgets.
  `internal/memorypack` owns bounded Evidence export, canonical hashes, and
  omission reporting. Their adapters implement authorized repeatable reads.
- `internal/search` owns query and reconciliation orchestration, provider bounds,
  readiness, ranking, and error projection. Its adapter owns query reads,
  bootstrap, and index mechanics; Knowledge persists canonical projections.
- `internal/storage/postgres` owns shared RLS, migrations, locks, and transaction
  infrastructure. The ordered Evidence-only migration has an explicit inventory
  and fingerprint check; it does not call providers.
- Access, Privacy, Settings, Operations, and Audit retain their native application
  owners. Privacy inventories distinguish legacy upgrade sequencing from the
  final Evidence-only catalog. Audit and operator diagnostics retain separate
  exposure contracts. HTTP and MCP bind and translate application contracts;
  composition supplies private adapters and credential protection.

Every live implementation has one owner. Test fixtures use owner-local helpers
or public capability ports without creating a second runtime authority.

## Cross-capability contracts

- Team, authenticated spaces, ownership, active generations, and current write
  permissions bound every read and mutation. Read visibility does not grant
  another owner's lifecycle authority. Private Evidence never enters ontology.
- Recall's topic filter selects the union of current direct memberships without
  descendant expansion. Lifecycle, temporal eligibility, freshness, and known
  exclusions apply before selection and counts. Topic metadata remains current
  during temporal Evidence reads. Pause preserves eligible publications;
  disabling organization removes it from Recall.
- Trace derives its space from the authorized Evidence target. Export retains
  exact provenance and separately reports content omissions. Neither reconstructs
  a factual graph or promotes retrieved context into submitted Evidence.
- Evaluation composition uses the same native Evidence contracts with separate
  build-profile discovery. Historical experimental captures are data artifacts;
  they do not register executable graph contracts.
- Required errors remain visible. Optional Recall degradation is explicit.
  Provider exchanges use bounded protected stdout capture independently of
  optional telemetry; public errors and audit remain bounded separately.

## Database-case registry

Every tagged `_integration_test.go` source has one declaration in
`scripts/e2e-db-cases/`. The declaration's package is the source directory, and
its run expression names one concrete test. The registry loader checks duplicate
IDs, missing declarations, package mismatches, build tags, and stale source
paths before invoking a batch. Database-free cases remain ordinary `_test.go`
files and are covered by the normal Go test suite. The frozen baseline preserves
IDs, run expressions, phases, and scenario ownership while allowing a
capability to relocate a fixture to its native package.

The central architecture manifest, registry loader, composition root, and E2E
host controller are shared read-only infrastructure for capability cutovers.
Native fragments and fixtures may update their own ownership records and
registrations without restoring a broad shared facade.

## Coverage and verification

Database-free Go production and evaluation packages, browser modules, and the
MCP proxy are inventoried independently. Each report fails closed for missing
sources, empty reports, omitted browser modules, exact-boundary percentages,
and profile duplication. PostgreSQL adapter and integration coverage remains a
separate proof of SQL, RLS, transaction, lock, and concurrency behavior.

The architecture conformance UAT and checker validate fragment completeness,
visibility, dependency direction, worker anchors, database-case ownership, and
the absence of retired migration metadata. Workers remain permanent ownership
records; their identities are discovered from source and compared with the
manifest on every check.
