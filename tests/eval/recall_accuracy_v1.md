# Frozen Recall accuracy cohort

Issue #666 adds `recall_accuracy_v1`, a separate synthetic relevance cohort.
It contains 24 queries: four each for generic predicates, ambiguous names,
graph hubs, synonyms, polarity/time distinctions, and multihop retrieval.
Every case has explicitly relevant and irrelevant evidence plus unjudged
distractors. Bad@K counts only explicit negatives. Unjudged evidence receives
no relevance credit and is not presumed bad.

Generate a new local bundle:

```bash
go run ./cmd/eval-runner -mode generate-recall-accuracy \
  -out tests/eval/seeds/recall_accuracy_v1
```

The output directory must not exist, including as a file or symlink. An export
failure is an error; a partially written new directory is not a valid bundle.
The generator never opens an existing bundle for replacement.

The bundle uses existing seed-v2, corpus, cases, suite, and qrel formats. Each
corpus row has native Relationship proposals with evidence coverage and passes
current Remember contract validation and the production admission scanner.
`judgments.jsonl` records reasons and exact codepoint spans. `cohort.json`
retains the synthetic graph and ontology setup. Neither file is sent to
providers or imported as evidence. Only corpus content and native proposals
enter the ordinary import path.

The committed `source_locks/recall_accuracy_v1.json` binds generator source,
the complete cohort, judgments, seed hash, and every exported input artifact.
Ordering and generated content are deterministic. Freeze this lock before
running Recall; never adjust judgments to fit observed rankings. Existing
public-v3 generators, seed assets, source locks, and judgments remain unchanged.

## PostgreSQL baseline

The registered `TestRecallAccuracyCohort`, `TestRecallAccuracyVariations`, and
`TestRecallAccuracyIsolation` tests use real PostgreSQL, canonical Knowledge
writes, ontology publication, Recall application retrieval, and the existing
evaluation scorer. The complete corpus is loaded together. Both ontology-off
and ontology-on queries run against that same state, using fixed evidence and
search-document timestamps, deterministic document UUIDs, and explicit source
mappings. Two fresh fixtures must reproduce all source rankings and metrics.
The cohort and variation tests may run concurrently in separate disposable
PostgreSQL containers; shared-DSN execution remains serial.
The four hub cases publish one shared topic because `HubPlatform` has one
catalog alias owner. Every case's names and source assignments are retained.

The baseline uses identical three-dimensional unit vectors `[1,0,0]` for every
document and query. These vectors are deliberately independent of gold labels;
they exercise fusion and ordering, not learned embedding quality. Additional
runs reverse UUID and insertion ordering and omit embeddings. Positive
controls exercise predicate queries, common-word names, alias-only discovery,
grouped displayed alternates, and explicit entity expansion. Private-space,
cross-team, and different-owner controls exercise real RLS and mutation policy.
The grouped variation uses the production organization service with synthetic
provider equivalence proposals; no judgment labels or reasons reach that fixture.

The Go JSON test stream includes `RECALL_ACCURACY_CASE`,
`RECALL_ACCURACY_SUMMARY`, `RECALL_ACCURACY_LOCK`, and
`RECALL_ACCURACY_ISOLATION` and `RECALL_ACCURACY_REPEATABILITY` records. Preserve these logs with the tested commit,
trusted workflow revision, selected cases, run URL, and production-image digest.
Score rows report Recall@10, MRR, nDCG@10, Bad@10, and missing references.
Known ranking failures are baseline observations. Broken provenance, missing
mappings, failed preservation/isolation, and skipped cases fail the tests.

```bash
go test ./internal/evalharness ./cmd/eval-runner \
  -run '^(TestRecallAccuracy|TestEvalRunnerRecallAccuracy)' -count=1
env -u DATABASE_URL DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 \
  DENSE_MEM_REQUIRE_POSTGRES_TESTS=1 \
  go test -tags integration ./internal/recall/postgres \
  -run '^TestRecallAccuracy' -count=1 -timeout=45m -json
```

The approved delivery runs PostgreSQL tests through Development E2E on the
exact pushed commit. Confirm the current registry partition before selecting
shard 5, and require every registered test to complete. Run the existing
`ontology_organization` and `space_aware_recall` production scenarios as well.
Focused checks do not replace final PR CI and full production-image E2E.
The complete evaluator package suite runs in `ci-shared.yml`'s `go-tests` job.

The [committed baseline](baselines/recall_accuracy_v1.json) names `e0ccd0b3`
as its capture commit and preserves the identical first observations from
`41f94067`. It records all 210 required observations, complete mappings,
preservation and isolation checks, frozen hashes, and known ranking failures.

| Mode | Recall@10 | MRR | nDCG@10 | Mean Bad@10 |
| --- | ---: | ---: | ---: | ---: |
| Ontology off | 0.1667 | 0.0622 | 0.0855 | 0.1250 |
| Ontology on | 0.3750 | 0.3393 | 0.3472 | 0.7083 |

Keep generated bundles and complete logs under ignored seed/run directories.
This cohort establishes a reproducible retrieval-logic baseline; it does not
establish live provider accuracy or replace the release comparison in #660.
