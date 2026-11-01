# Shared release evaluation for #660 and #668

**Result: incomplete; keep both issues open.** Both releases completed 991 imports
and 204 scored cases, but candidate ontology coverage failed, overall nDCG@10 did
not improve, and adverse-cohort safety remains unmeasured. The
[compact evidence](baselines/issues_660_668_shared_evaluation.json) records the
measured results, exact identities and artifact hashes. The protocol below was
frozen before execution; the observed outcomes follow it.

The maintainer approved one fresh v2.6.5 → v2.7.1-rc.17 campaign and one combined
delivery. This replaces #668's first-parent comparison and repeated public-corpus
imports. The result measures whole releases; existing focused regressions isolate
#690's ranking mechanism. The earlier [failed run](issue_660_release_comparison.md)
and historical captures remain unchanged and are not successful benchmarks.

## Frozen conditions

Use the unchanged public v3 cohort: 991 documents, 204 cases, original judgments
and operation identities. Seed SHA-256 is
`0923aa14109291e5d5cf24fe419f0b6fc229823bf3dd4292e17d45447365e675`;
suite SHA-256 is
`438a9742576ad5fa9a97c8edaa1e6b0550c740a812dfd3cf051cce68e632b17d`.
Baseline source is `49ba9d36acaf19bde5a8ddbe7552fd04621d19bd`; candidate and
common scorer source is `92620e862446b31e5f20f34224c4dfd5f7c640fb`, containing
#661, #688 and #690. Reverify the newest published qualifying prerelease before
freezing the new runtime manifest. Record published image digests separately
from actual native evaluation image IDs and clean native runner hashes/stamps.

Use `dense-mem-testing`; `gpt-6-luna` is the maintainer-provided backing identity.
Capture the provider-reported model separately; revision is unknown unless
reported. Use Cloudflare `@cf/baai/bge-m3`, 1,024 dimensions, `vector_hnsw`.
Optional decision tasks remain disabled. Live `AI_DECISION` and reranker
quality/cost are explicitly deferred, including their former acceptance gate.

Both native advisory-lock limits are 4. Import concurrency is 4, PostgreSQL pool
capacity is 25, and both new services use `LOG_LEVEL=info` with identical provider
settings. Keep ontology INFO measurements and every error. Trace logging overhead
is only a hypothesis. Neither released server nor its 160-second assessment and
10-second commit limits may be changed by this delivery.

## Qualification before the campaign

Prepare separate fresh disposable runtimes using the [runtime lifecycle](runtime-lifecycle.md).
Keep the previous runtimes and evidence. Provision a fresh team and credential
per runtime, verify authenticated ownership and shared space, and retain native
container/session/tool checks. The exact pinned baseline uses its native
`shared_only` credential binding and body-only request hash. New releases retain
explicit `target_space`; the harness does not patch either released server.

Before candidate probe ingestion, run the existing `pair_capacity` stage against
the qualification manifest: pause native ontology, enable it with model
`dense-mem-testing`, concurrency 8/8 and context 500,000, and wait until that exact
policy is active. Keep it enabled and paused for the entire probe; do not resume
maintenance. Baseline retains `none`. Use `pair_inspect_runtime`, `pair_snapshot`
and `claim-runtime` before ingestion to prove native identity and an empty scope.
Candidate verification deliberately rejects disabled or unpaused status; this
prerequisite supplies native projection coverage without running maintenance.

Freeze these five source IDs, in this order, using their original full inputs:

1. `public_6axis_v1:beir_standard:doc:14924526`
2. `public_6axis_v1:beir_standard:doc:2251426`
3. `public_6axis_v1:beir_standard:doc:22543403`
4. `public_6axis_v1:beir_standard:doc:306006`
5. `public_6axis_v1:beir_standard:doc:30655442`

Store that array in an ignored JSON file and use a qualification-only paired
manifest and deadline, distinct from the later campaign. After independent
plan-audit and focused checks, run each side once:

```bash
"$PAIR_RUNNER" --mode paired --pair-config "$qualification_config" \
  --pair-action probe --pair-side "$side" --pair-input "$five_source_ids" \
  --pair-directory "$new_probe_output"
```

`probe` makes one client submission for the first document, then four concurrent
submissions only if the first completed. It never retries a client submission and
refuses an existing output directory. It retains responses, operation identities,
UTC labels and monotonic client durations. Native bounded provider regeneration
remains enabled. Client completion is provisional: collect database snapshots
through `pair_snapshot` and run `--pair-action verify-probe` with that snapshot as
`--pair-input` and the original probe output as `--pair-directory`. This validates
the five receipts against the five original corpus items. Require one completed attempt per source,
exact content hashes, live evidence/occurrences, correct team/owner/space/generation,
zero foreign/unclaimed evidence and converged current search. Check all retained
invocation/attempt logs for admission or commit failures. A client pass alone
does not qualify either release.

If either qualification fails, stop further model work. Correlate MCP submission,
provider exchange, application phase, database activity and native attempt IDs.
Preserve failures. Correct only evaluation configuration/harness defects within
scope. Release code or production-timeout changes require revised approval.

## One public campaign after both qualifications pass

Create fresh full-campaign runtimes and a new manifest; never import the public
corpus into the qualification databases. Start one 24-hour observation budget
after qualification. Baseline uses native `none` readiness with no ontology.
Candidate uses `adr0014`, ontology 8/8 and context 500,000. Run the existing paired
entry point once:

```bash
PAIR_CONFIG="$campaign_config" PAIR_RUNNER="$PAIR_RUNNER" \
  bash tests/eval/scripts/run_full_public_rag_eval_until_done.sh
```

Retain native retry history, verified resume and all failures. Never restart
failed semantic work, change operation keys, extend the cutoff or change the
experiment mid-run. Drain, pause while still enabled, verify readiness and freeze
before primary capture. Verify the original immutable proofs before common
scoring. These same primary imports/captures/scores serve both issues.

After preserving the candidate primary capture, record its canonical, source and
ontology-state fingerprints and effective decision settings. Disable ontology
through native controls while retaining the drained state, and capture a separate
204-case control with the native runner, primary source mapping and a new output
directory, without `--import-seed`. Use the common scorer on these traces. Compare
canonical/source fingerprints and ontology content before/after, excluding only
the expected enable-setting transition. Restore enabled/paused state and verify
its original primary proof remains unchanged. Label this control as removing
grouping and discovery together. Baseline already supplies its no-ontology state.

Keep the separate frozen 132-source/24-case [adverse cohort](recall_accuracy_v1.md)
and source lock unchanged. Generate only into a new ignored directory, verify
the committed lock, and evaluate the same pinned releases in separate fresh
cohort runtimes using the paired entry point and common scorer within the campaign
budget. Keep original judgments, explicit negatives, mappings and branch traces;
do not mix these sources into the public databases. Compare every case's Bad@10
and bad rank-one outcome; the public suite's absence of explicit negatives cannot
replace this check. All six registered PostgreSQL shards also retain the frozen
cohort's deterministic mechanism/isolation controls.

## Reporting and completion

Publish one evidence report with separate conclusions. Require 991 completed
imports and 204 scored cases on each release, zero unmapped references and native
readiness before either successful conclusion. Reconcile common-scored aggregates
with every case and axis. #668 additionally needs improved overall nDCG@10, no
Recall@10/MRR loss, no new adverse-cohort Bad@10 or bad rank-one, and explanations
for every case regression. A complete unfavorable result is not an improvement.

For #660 report stage timings, distinct versus repeated work, provider/application
failures, useful throughput, recall latency and available usage. Stage durations
use monotonic clocks tied to the host boot; UTC timestamps are labels. Historical
UTC-only stages have unavailable elapsed time. Native Recall timing covers the
server operation, not the full client operation. Report unknown usage as unknown,
do not double count diagnostics, and separate historical 41+ hours. Record host
load, model drift, independently generated semantic states and missing prices as
limitations. Give a stage ETA only after a meaningful successful sample.

Run local evaluator/CLI and evaluation-build regressions, changed-area static
checks and the lightweight decision-provider deployment helper. After a conformant
audit and local gates, push normally and dispatch exact-commit Development E2E
from trusted main: all six PostgreSQL shards plus `ontology_organization` and
`space_aware_recall`, 90-minute observation, no automatic retry. Retain source/image,
selection/result and run-owned cleanup receipts. Then open one non-draft PR,
complete current-head review/CodeQL, request its own full validation after
`reviews_clean`, and require full CI/production-image E2E `automation_clean`.

Post completeness, metrics, regressions, limitations, hashes and validation links
to both issues. Use closing references only for passing completion criteria.
Incomplete/failed issues remain open. Human merge precedes #680's artifact audit;
#681 and #682 remain downstream. Preserve generated evidence in ignored paths.

## Observed outcomes, 2026-11-01

The five-document qualification passed on both releases: five client submissions,
five completed durable attempts, exact evidence and ownership, current search,
and zero admission or commit failures. One source ran alone, then four concurrently.
Both native admission limits were verified as four. Full imports used fresh
databases, concurrency four, INFO logging and pool capacity 25. The failed earlier
concurrency-ten attempt is retained separately: it had 24 generation HTTP 200
responses, 38 embedding HTTP 200 responses and one unanswered generation call,
alongside Dense-Mem admission/commit failures. Provider success did not establish
successful ingestion. Trace overhead was not established as their cause.

The shared campaign ended with exit 1 after its complete captures and scoring:
baseline readiness was `ready=true, complete=true`; candidate readiness was
`ready=true, complete=false`. Candidate was drained, paused and search-converged,
but failed ontology/projection coverage. No deadline extension, semantic restart,
manual repair, released-code edit or timeout-policy change rescued the run.
The fixed observation cutoff remained `2026-11-02T02:56:21.130988Z`.

### Processing and consumption (#660)

| Measured stage (monotonic seconds) | v2.6.5 | v2.7.1-rc.17 |
| --- | ---: | ---: |
| Import | 5,052.493 | 4,637.185 |
| Drain | 4.714 | 8,132.754 |
| Recall capture | 54.598 | 103.746 |
| Common scoring | 1.720 | 1.778 |

Import time was 84.21 versus 77.29 minutes, with useful throughput of 11.77 versus
12.82 completed sources/minute. This is an observed import-stage difference,
not an end-to-end speedup: candidate additionally spent 135.55 minutes draining,
including waiting for native search reconciliation. One superseded relationship
retained a current search projection; the hourly native 256-document cursor
eventually reached it. There was no manual repair. A sampled database observation
found no blockers; it does not prove the absence of contention throughout the run.
The historical 41+ hours used mixed releases/settings and is not the controlled
baseline. UTC timestamps label events; stage durations use host monotonic clocks.

| Ingestion work | v2.6.5 | v2.7.1-rc.17 |
| --- | ---: | ---: |
| Distinct completed sources | 991 | 991 |
| Durable attempts | 1,035 | 1,011 |
| Failed attempts, all recovered natively | 44 | 20 |
| Sources requiring multiple attempts | 42 | 19 |
| Generation exchanges / HTTP 200 | 1,223 / 1,194 | 1,158 / 1,147 |
| Embedding exchanges / HTTP 200 | 1,704 / 1,701 | 1,765 / 1,765 |
| Known generation-token subtotal | 61,759,414 | 59,932,826 |
| Generation exchanges with unknown usage | 29 | 11 |

Baseline failures were 29 assessment timeouts, 12 invalid assessment responses,
two embedding timeouts and one embedding-unavailable result. Candidate failures
were 11 assessment timeouts and nine invalid responses. Full imports had no
admission or commit failures. Baseline invocation diagnostics classify 31 as
cancelled and 13 as failed; their corresponding durable attempts classify all
44 as failed. Candidate has 11 cancelled and nine failed invocations, corresponding
to 20 failed attempts. These are different accounting boundaries.

Provider exchanges are counted once by invocation ID and sequence number; attempt
diagnostics are not added again. Baseline metadata labels every assessor capture
`unavailable`, although 1,194 retain parseable response bodies and usage. The
report uses those retained bodies and discloses the metadata inconsistency.
Embedding-token usage, missing-response usage, prices and monetary totals are
unavailable. Known ingestion-token subtotals exclude ontology consumption.

Candidate ontology ended with **3,004 organized / 3,651 eligible sources**,
417 failed and 230 ambiguous, with no pending work or active batches,
preparations or projections. Three additional unavailable sources were ineligible
and are outside that denominator. Failed-source reasons were 316
`provider_response_invalid`, 79 `stale_input`, 14 `provider_unavailable` and eight
`commit_conflict`; all 230 ambiguous sources had `classification_ambiguous`.

There were 1,964 immutable assessment receipts: 1,135 model assessments and 829
native deterministic paths without provider dispatch. The latter are not presumed
cache hits. Model assessments used 1,322 provider turns, including 187 extra
turns above one per assessment: 1,096 valid responses, 224 validation failures
and two request timeouts. Final assessment outcomes differ: 1,065 completed,
37 invalid, 23 stale, eight commit conflicts and two unavailable. Native paired
SQL repeats the final assessment outcome on each turn; those labels are not
individual HTTP outcomes. The known ontology-token subtotal is 9,773,763, with
two turns' usage unknown.

The retained receipts contain 26,542 source appearances across 3,651 distinct
source/version/fingerprint tuples. These are receipt context inputs, not provider
prompt-item counts. INFO discovery structs were redacted, so marker enumeration,
freshness and requeue counters remain unavailable. Scalar checkpoint sums retain
1,008,121.48 ms discovery and 1,757,075.15 ms preparation; preparation includes
discovery, and the checkpoint does not cover the entire later search wait.

Ontology HTTP p50/p95/p99 was 3,672 / 12,375 / 14,583 ms, with two 600,000 ms
timeouts. Its boundary is `httpClient.Do` through bounded full response-body read,
before JSON decoding and validation. Admission mean/p95 was 9.01 / 32 ms;
reconciliation mean/p95 was 9.93 / 21 ms; recorded provider queue time was zero.
Summed concurrent durations do not represent elapsed wall time.

### Retrieval quality (#668)

| Metric | v2.6.5 | v2.7.1-rc.17 |
| --- | ---: | ---: |
| Completed imports / scored cases | 991 / 204 | 991 / 204 |
| Unmapped source references | 0 | 0 |
| Recall@10 | 0.893790850 | 0.893545752 |
| MRR | 0.853409975 | 0.853409975 |
| nDCG@10 | 0.834013892 | 0.833855480 |
| Required rank-one rate | 0.808823529 | 0.808823529 |
| Mean Bad@10 / bad rank-one rate | 0 / 0 | 0 / 0 |
| Server Recall mean (ms) | 194.23 | 438.22 |
| Server Recall p50 / p95 / p99 (ms) | 166 / 357 / 583 | 404 / 568 / 915 |

Recall timing measures the native server operation, not a complete client
operation. Server Remember p50/p95/p99 was 13,613 / 31,088 / 163,815 ms versus
14,764 / 32,378 / 163,292 ms. Complete-client timings are available for the
qualification only, not the full campaign. Single sequential captures, host load,
provider drift and independently generated semantic states limit attribution.

| Axis (case count) | Baseline Recall / MRR / nDCG | Candidate Recall / MRR / nDCG |
| --- | --- | --- |
| SciFact (25) | 1 / 0.825000 / 0.868021 | same |
| MS MARCO (84) | 1 / 0.984524 / 0.988307 | same |
| HotpotQA (13) | 0.923077 / 1 / 0.940484 | same |
| MuSiQue (27) | 0.851852 / 0.981481 / 0.824373 | same |
| QASPER (46) | 0.637681 / 0.516753 / 0.511783 | 0.636594 / 0.516753 / 0.511080 |
| LongMemEval oracle (9) | 1 / 0.833333 / 0.821570 | same |

All aggregates, every slice, branch availability and all 204 case identities were
independently reconciled with retained per-case rows. Evidence-branch metrics
exist for 95 cases; context and dream branches have no scored cases. Their native
zero placeholders are unavailable measurements. Baseline has one nine-result
case with one relevant reference, so its shortened cutoff changes no @10 value.
No frozen judgments or scorer were changed.

Exactly one case changes quality metrics:
`public_6axis_v1:qasper:dev:1605.06083:6d4400f45bd97b812e946b8a682b018826e841f1`.
The relevant result stays at rank two, with MRR 0.5. Five original gold sources
map to four baseline canonical evidence IDs but five candidate IDs. Baseline
Remember maps `gold:1:0` and `gold:4:0` to canonical evidence
`1b65a50a-fa91-4828-bfc7-62d1a5bcaf30`, despite different original content hashes;
candidate preserves distinct canonical IDs. Native responses and exact occurrences
retain the original hashes. The common scorer therefore normalizes by four versus
five relevant canonical references: case Recall 0.25 → 0.20, nDCG
0.24630238874073 → 0.21398626473452756. This explains the entire aggregate change;
it does not demonstrate a ranking-position regression or isolate #690.

The ontology-disabled control reused the candidate's drained state and mapping,
without importing again. All 204 case-quality metrics match the candidate primary
exactly. Source, canonical and 12 ontology/community table fingerprints stayed
unchanged; the original primary proof remained
`28fdbf8a42bb9acbadb2309bb53284c4e8366eb4ece5c60fa4ba73f9cf44daef`.
Enabled/paused state was restored and all optional decision tasks stayed disabled.
The control removes grouping and discovery together. Its server Recall mean was
232.98 ms, p50/p95/p99 189 / 463 / 748 ms. One on→off sequence cannot establish
a causal latency benefit. It does not cure incomplete primary ontology coverage.

### Adverse safety and ticket disposition

The frozen 132-source/24-case adverse cohort passed both native model-free input
validations and all seven source-lock hashes. Its baseline import then failed:

```text
mcp tools/call remember returned -32602: evidence[0].labels must be an array
```

The v2.6.5 native importer serializes omitted optional labels as JSON null; its
MCP rejects null. Current rc.17 already omits empty labels. Database diagnostics
verify zero durable attempts, zero invocation diagnostics and zero provider
exchanges on both fresh adverse runtimes; candidate import never began. Adverse
Bad@10 and bad rank-one are **unmeasured**, not zero or passing. Patching the old
native runner, substituting another importer or modifying frozen seed bytes would
change approved provenance; none was done. A compatible-importer experiment needs
a revised protocol. The public suite has no explicit negatives and cannot replace
this safety check.

**#660 remains open:** measured import improvement does not establish complete
release performance; candidate coverage failed and several work/usage measurements
are unavailable. **#668 remains open:** nDCG did not improve, Recall declined,
coverage is incomplete and adverse safety is unmeasured. Live reranker quality
and cost remain deferred. This delivery reports an incomplete evaluation rather
than closing either issue. Human merge still gates #680, #681 and #682.

### Evidence and validation boundary

The compact JSON records exact source revisions, actual evaluation image IDs,
published production platform digests, clean native runner/scorer hashes, stage
receipts, quality/usage reconciliations and retained artifact hashes. Evaluation
images are separate from published production images. The baseline published
v2.6.5 image carries version label `v2.6.5-rc.17`; its source revision was verified.
Generated corpus, captures, database state, credentials and raw responses remain
ignored local evidence. The earlier failed report remains unchanged.

Local evaluator/CLI suites, evaluation-build regressions, request-hash owner
tests, vet, architecture checks and three lightweight decision-deployment helper
tests passed. They validate the harness, not the failed evaluation acceptance.
Publication, exact-head Development E2E, review and final CI evidence are recorded
separately in the combined PR and both issue updates; this evaluation artifact
does not claim those later gates passed.
