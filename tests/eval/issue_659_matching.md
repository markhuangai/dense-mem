# Ontology loose matching (#659)

The pinned PostgreSQL 127-word English list filters only loose ontology lookup
and definition-marker matching. Exact names/aliases, forced references, raw
64-word source vocabulary, immutable receipts, and semantic source text retain
their existing contracts. The matching source lock is
`source_locks/ontology_matching_v1.json`; its eight cases contain 16 sources and
cover stop-word overlap, domain terms, stop-word names/aliases, short identifiers,
Unicode, polarity, and time. Original organization and follow-up locks are unchanged.

Before any live run, freeze the matching inputs, definitions, judgments, generator
hash, source state, configured provider/model, context window, and three fresh-state
repetitions. Run `TestOntologyMaintenanceCohort` and
`TestOntologyMaintenanceMatchingCohort` with `DENSE_MEM_ONTOLOGY_LIVE_EVAL=1`,
`DENSE_MEM_REPOSITORY_TESTCONTAINERS=1`, `DENSE_MEM_REQUIRE_POSTGRES_TESTS=1`,
`AI_VERIFIER_MODEL=dense-mem`, `AI_VERIFIER_DISABLE_TEMPERATURE=false`, and
`ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS=500000`. Give each repetition a
separate ignored `DENSE_MEM_ORGANIZATION_REPORT_DIR`; preserve every failed result.
The approved #659 restart selects `dense-mem` only in the evaluation process
environment; the maintainer reports that it maps to `gpt-6-luna`. Preserve the
full strict schema and original `temperature: 0` request setting. Retain all
earlier runs, including the `dense-mem-testing` Muse diagnostics. Per-response
backend identity and internal revision remain unknown unless exposed by the provider.

Require complete source/fact retention, zero false consolidation, and zero repeated slots. The maintainer
approved #659's latency amendment after the original enabled/disabled comparison
failed on both the unchanged base and candidate. Retain those failures and run five
fresh alternating base/candidate pairs with the unchanged `BenchmarkRecallOntology`
fixture, 20 warmups and 200 measurements per workload/mode. Each arm uses `-count=1`
and fresh PostgreSQL state; odd pairs run base first, even pairs candidate first.
`scripts/compare_ontology_recall_pairs.py` requires all ten source-bound arms and
at most 10% median paired candidate/base p95 regression for each enabled workload.
Disabled-mode measurements remain explicit controls. Freeze the protocol and both
source identities before execution, retain every result, and do not retry failures
automatically. This amendment applies only to #659; #660's final requirements remain.
Record candidate counts, marker fanout, processing duration, and
provider calls separately; fixture counts do not establish runtime throughput.
Compare historical measurements only when fixture identity and execution context
permit. The intermediate 1k waiver belongs to #659; #660 owns final evaluation.
