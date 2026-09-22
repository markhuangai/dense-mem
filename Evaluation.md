# Evaluation

Dense-Mem evaluates retrieval through real MCP Remember and recall paths on a
dedicated local stack. Deterministic qrels score retrieval; model-generated
answers are not the acceptance oracle.

## Image Boundary And Tool Surface

The production image contains production memory tools. The separately built
`evaluation` target adds `eval_list_knowledge_refs`,
`eval_run_dream_cycle`, and `eval_run_recall_case`. No runtime switch exposes
these in the production binary.

`examples/docker-compose.evaluation.yml` provides the local evaluation stack.
Keep its team and persistent runtime separate from manual memory use.

## Current Retrieval Evaluation

The required quality comparison uses the approved deterministic 1k corpus.
Its six public axes cover SciFact, MS MARCO, HotpotQA, MuSiQue, QASPER, and
LongMemEval oracle retrieval.

Seed manifests, suites, qrels, cohort locks, and policy hashes establish the
evaluation identity. Preserve approved source text and source metadata.
Relationship-enriched datasets add non-authoritative proposals; the production
assessor still owns grounding and acceptance.

Current Remember accepts evidence without proposals. A particular enriched
evaluation fixture may require proposals for its benchmark purpose; that is a
dataset contract, not a universal public write requirement.

## Seed Validation And Provenance

The runner validates the manifest, corpus, suite, mappings, and public request
shape before a live import. Content must fit the 999-code-point evidence bound
without silent truncation.

For derived cohorts, validate the parent/source hashes, declared exclusions,
retained row order and content, copied sidecars, and proposal-ledger provenance.
Do not regenerate an approved seed and claim it has the same identity.

```bash
go run ./cmd/eval-runner --mode validate \
  --seed "<approved-seed-manifest>" \
  --suite "<approved-suite-jsonl>" \
  --out "<ignored-validation-directory>"
```

Angle-bracket values are placeholders. The runner has no default seed/suite;
supply the approved paths explicitly.

## 100-Row Smoke

The `local_eval_100` preset generates 100 corpus rows and 25 sanity cases
under the current `local_eval_100_v2` dataset identifier:

```bash
go run ./cmd/eval-seedgen \
  --preset local_eval_100 \
  --out tests/eval/seeds/local_eval_100 \
  --suite tests/eval/suites/local_eval_100.jsonl
```

Smoke checks validate image composition, real terminal Remember import,
knowledge mapping, recall-case execution, scoring, and artifact identity.
Report them as smoke evidence. They do not replace the deterministic 1k
comparison.

## Live Import And Retrieval Run

```text
approved corpus row
  -> production remember through MCP
  -> terminal result and canonical evidence/Relationship mapping
  -> evaluation recall cases
  -> deterministic qrel scoring
  -> baseline/candidate comparison
```

Import must report failed operations and every corpus mapping. A completed
evidence-only write does not prove the expected Relationship was accepted or
that a qrel is retrievable.

The persistent monitor is
`tests/eval/scripts/run_full_public_rag_eval_until_done.sh`.
Configure its approved `SEED`, `SUITE`, release policy, and dedicated
runtime before starting it. A deliberately ungated diagnostic run must be
labeled as such and cannot be presented as a release-gate pass.

The committed runner and monitor own accepted options and identity checks.
Consult their help and
[the evaluation harness](https://github.com/markhuangai/dense-mem/tree/main/tests/eval)
for execution details. Do not apply old ticket-specific waivers to a new change.

## Baseline, Candidate, And Comparison

Baseline and candidate runs must use the same declared evaluation identity and
record source/image, runner/scorer, provider fingerprints, contract, mapping
hash, and timing. Generic comparison is same-seed only; an explicitly validated
cohort comparison is required for different derived identities.

```bash
go run ./cmd/eval-runner --mode compare \
  --baseline-run "<ignored-baseline-directory>" \
  --candidate-run "<ignored-candidate-directory>" \
  --out "<ignored-comparison-directory>"
```

Report quality, required rank-one coverage, bad-result rates, unmapped refs,
ingestion outcomes, degradations, latency, and provider cost where measured.
Do not average away failed imports or missing scored cases.

## PR Evaluation Rule

Retrieval quality, ranking, placement accuracy, verifier/reviewer behavior,
embeddings, search-document, migration-placement, or performance changes require
the deterministic 1k evaluation/comparison unless the linked issue records an
explicit maintainer-approved waiver and replacement verification.

Documentation-only changes do not alter retrieval and do not require running
that evaluation. Public behavior still has its appropriate regression and
production-entry gates under [Development And Testing](Development-And-Testing).

## Artifact Handling And Risks

Downloaded data, generated seeds/suites, credentials, runtime databases, full
runs, and comparisons stay in ignored local paths. Commit reproducible
generators, source locks, validation/scoring logic, and compact safe evidence.

A different seed, provider, or image can make scores incomparable. Missing
mapping rows can make a weak run look good. A mocked import can bypass actual
authorization and validation. Bind artifact identity, use real production
intake, and fail incomplete evaluations visibly.
