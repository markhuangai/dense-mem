# Evidence-only removal inventory

Governing decision: `adr/0017-evidence-only-memory-without-factual-relationships.md`.
Deployed migrations and retained #681 experimental captures are immutable.

| Owner | Existing purpose and consumers | Disposition |
|---|---|---|
| `internal/assessor` | Entity grounding, predicate registration, Relationship proposal validation; Remember and session provider contracts | Remove graph request/results, schemas, prompts, normalization, and validation. Retain complete-response security, Evidence equivalence/conflicts, budget and repair policy. |
| `internal/remember` | Client proposals, citation coverage, graph handles, terminal graph receipts; MCP/HTTP, sessions, evaluations | Replace with exact Evidence intake; bind v3 hashes, reject retired fields, retire historical operation receipts. Retain authenticated destination, security admission and request deadlines. |
| `internal/knowledge` | Atomic graph acceptance, support, factual correction/conflict, graph search projections; Remember, session, lifecycle | Remove graph writers and ports. Preserve Evidence/source/occurrence lineage, duplicate resolution, required embeddings, lifecycle and Evidence conflicts in the existing transaction. |
| `internal/session` | Factual extraction and linking checkpoints; raw-event ingestion and retry | Replace extraction with exact current-event excerpts and offsets. Preserve raw-request hashes and generation/authorization fences; retire only obsolete unfinished checkpoints. |
| `internal/ontology` | Relationship-backed publication, entity classes and predicate concepts; organization, maintenance and Recall | Retain Evidence topics, direct membership, equivalence and organizational hierarchy. Remove fact-graph source kinds and refinement semantics. |
| `internal/recall`, search and embeddings | Factual expansion/reranking, communities, graph feedback; memory retrieval | Retain Evidence text/vector fusion, eligibility, temporal reads, conflicts and feedback. Replace discovery with scoped direct Evidence-topic filters and hints. |
| `internal/trace`, `internal/memorypack` | Relationship-rooted provenance traversal and graph export; tools, clients, UI | Replace roots with authorized Evidence IDs, retain bounded occurrences, revisions, decisions, lifecycle, canonical hashes and omissions. |
| Dream, community, factual graph adapters | Derived inference, proposals, graph readiness and projections; startup, control workers and evaluations | Remove executable production/evaluation paths, configuration, readiness dependencies and storage. Retain shared model-provider infrastructure. |
| Registry, transports, clients and UI | Public graph contracts and navigation | Adopt `dense-mem.v3.0.0`, remove graph tools and screens, update supported clients and list/call parity together. |
| PostgreSQL and privacy inventories | Graph tables, foreign keys, triggers, indexes, mixed receipts/diagnostics; startup migrations, erasure and copy | Add one ordered transactional forward migration with explicit dependency order, legal-hold checks and retained fingerprints. Preserve security accounting and original operation identity; no cascading graph archive or compatibility reader. |
| Evaluation and production regressions | Graph-generating intake, community readiness and historical graph captures | Remove executable graph modes. Retain offline historical Evidence measurements; extend existing paired owners with retained-baseline and `evidence_only_v5` identities. |

## Recovery boundary

Stop incompatible instances and verify a matching restore-read-tested backup before
startup applies the destructive migration. A rollback before transaction commit
retains the old catalog. After commit, recovery requires that backup; after new
writes, roll forward unless the maintainer separately approves restoration.

## Production scenario helpers

The graph conflict provider/driver, graph conflict queue, community recall, factual
ontology refinement, correction and Dream journeys existed to drive the removed
production owners. Their scenario registrations, helper containers and dedicated
fixtures are retired together. Evidence conflicts remain covered through the
Remember scenario and the Evidence-conflict browser journey; direct Evidence
topics and organizational hierarchy remain in the ontology scenarios. The full
scenario retains operational telemetry and Evidence export. Historical sealed
evaluation captures and experimental artifacts are unchanged.
