# Issue #660: incomplete pinned release comparison

The comparison failed during the baseline import. It establishes no release
speedup, retrieval-quality result or recall-latency result. **#660 stays open.**
The [compact evidence](baselines/issue_660_release_comparison.json) retains
identities, final counters, limitations and hashes of the ignored raw evidence.

## Result and completeness

| Result | v2.6.5 baseline | v2.7.1-rc.17 candidate |
| --- | ---: | ---: |
| Completed source documents | 1 / 991 | 0 / 991 |
| Distinct sources with durable attempts | 5 | 0 |
| Durable attempts | 20 | 0 |
| Failed durable attempts | 19 | 0 |
| Sources without a completed receipt | 990 | 991 |
| Captured/scored retrieval cases | 0 / 204 | 0 / 204 |

Baseline import started at 2026-11-01 01:19:39 UTC and exited with code 1 at
01:26:27 UTC. The native error said that Dense-Mem could not confirm the operation
was saved. The evaluator retained the failed stage and did not restart it.
Candidate capacity activation, import, ontology drain, capture and scoring were
never reached. No complete mapping or capture proof exists. Every frozen question
remains unscored; missing results are not zero quality scores.

The manifest declared admission capacity 10, but both pinned sources cap shared
advisory-lock admission at **4**. The authoritative constants are
`sharedAdvisoryLockAdmissionLimit` in
`internal/knowledge/postgres/advisory_lock_admission.go` and
`maxSharedAdmissionLimit` in
`internal/storage/postgres/lockadmission/admission.go`. Import workers and HTTP
stream capacity were 10; PostgreSQL pool capacity was 25. Those settings did not
raise the advisory-lock cap. Preflight checked the declared value against pool
capacity but failed to establish actual application admission. This run therefore
cannot substantiate the declared admission condition.

The final server logs contain 63 completed invocation events with 63 distinct
invocation IDs and 11 distinct request hashes. Of these, 43 failed at
`idempotency_lock` with `database_failure`; the remaining events include 19 commit
invocations and one assessment invocation. There were 18 classified semantic
commit timeouts: 16 at relationships, one at predicate resolution and one at
search documents. One further assessment invocation timed out. Admission failures
explain rejected admission; the underlying cause of commit timeouts is not
established. One database-wait sample found no blocking sessions, which does not
exclude earlier contention. These logs do not prove complete scheduled/dispatched
accounting or complete client-operation latency.

## Frozen identities and conditions

| Role | Release | Source |
| --- | --- | --- |
| Baseline | v2.6.5 | `49ba9d36acaf19bde5a8ddbe7552fd04621d19bd` |
| Candidate/common scorer | v2.7.1-rc.17 | `92620e862446b31e5f20f34224c4dfd5f7c640fb` |

The candidate was the newest published prerelease selected at execution start
and contains merged implementation PRs #661 and #688. Both native runners were
built from clean source clones with exact VCS stamps. The separately versioned
controller contains evaluation-only compatibility/recovery fixes; the release
runners, scorer, sources and frozen inputs were not changed during execution.

Published production image index digests:

- Baseline: `sha256:0310ef250efd978a5d7161372e9297d663fc30ab49e9cdea7c13402da6fbaaa4`.
- Candidate: `sha256:c50700ec658fee1ba9df9ce640ab3ed9d860ba00403945b203ba15f5904df2a5`.

The baseline production image retains a `v2.6.5-rc.17` version label, while its
verified source label matches stable v2.6.5. Live evaluation used native evaluation
targets, whose actual image IDs and binary hashes are in the compact evidence;
production images contain no evaluation tools.

The unchanged `public_6axis_1k_v3` contains 991 documents and 204 questions.
Its seed hash is
`0923aa14109291e5d5cf24fe419f0b6fc229823bf3dd4292e17d45447365e675`;
its suite hash is
`438a9742576ad5fa9a97c8edaa1e6b0550c740a812dfd3cf051cce68e632b17d`.
Both release-native input validations passed. Original judgments, source order,
operation keys and failed attempts remain retained.

Generation was configured as `dense-mem-testing`. The maintainer identifies its
sole backing model as `gpt-6-luna`; captured provider responses reported only
`dense-mem-testing`, with no system fingerprint or backing revision. Embeddings
used Cloudflare `@cf/baai/bge-m3`, 1,024 dimensions and `vector_hnsw`. Optional
decision providers were disabled. Candidate ontology capacity 8/8 and a
500,000-token context were requested in the manifest but never activated because
the baseline failed first. Baseline protocol was `none`; candidate protocol was
`adr0014`.

The original observation cutoff was 2026-11-02 00:54:45 UTC. It was not extended.
The run ended earlier on a required failure.

## Timing and consumption limits

The baseline import's UTC timestamps span 408.045 seconds. The complete monitor's
UTC timestamps span 422.027 seconds, while its outer monotonic timer reports
397.987 seconds. This clock disagreement is retained; it prevents a precise
elapsed-performance conclusion. Setup, import failure and historical processing
must not be combined into a release-speedup estimate.

| Captured invocation diagnostic usage | Generation | Embedding |
| --- | ---: | ---: |
| Captured exchanges | 25 | 38 |
| Exchanges with reported usage | 24 | 0 |
| Exchanges without reported usage | 1 | 38 |
| Reported input tokens | 454,116 | unavailable |
| Reported output tokens | 13,166 | unavailable |
| Reported total tokens | 467,282 | unavailable |

This is a reported subtotal from invocation diagnostics only. Attempt diagnostics
are not added again; token aliases are alternatives, not additive counters.
Missing responses/usage remain unknown. No complete consumption or billed-cost
total is claimed. No recall latency, per-axis/per-case quality, regression,
ontology distinct/repeated work or provider-overlap comparison was measured.
Native recall timing, when captured, measures a server operation rather than the
complete client operation.

## Retained T01/T02 evidence

[T01/#661](https://github.com/markhuangai/dense-mem/pull/661) measured five fresh
pairs per shape on source `0c12f681d218782371714fc9cda0e6ca041241d1` in
[Development E2E 40629690372](https://github.com/markhuangai/dense-mem/actions/runs/40629690372).
Overlapping-marker median discovery time fell from 47.482s to 2.530s, with 2,000
versus 100 freshness checks. Disjoint-marker medians were 2.476s versus 2.442s,
with 100 checks in both modes. Every sample retained canonical state and receipts
with no added provider calls. These are isolated discovery measurements; they
do not establish full-release speedup. See the retained
[protocol](issue_658_coalescing.md) and
[compact baseline](baselines/issue_658_coalescing.json).

[T02/#688](https://github.com/markhuangai/dense-mem/pull/688) reports complete live
passes for repetitions 2 and 3 of all 17 maintenance/matching cases. Repetitions
1 and 4 remain failed with request-ID faults; repetition 4 also retains an
accepted Unicode false-negative. The third clean repetition was not achieved.
The maintainer's #659-only exception does not waive #660's requirements.
Those historical runs used alias `dense-mem`, maintainer-reported `gpt-6-luna`,
temperature 0 and context 500,000; backing revision remained unknown.

The frozen eight-case/16-source matching cohort includes grammatical-only overlap
(`stop_word_only`, expected no loose match), distinct polarity/time facts
(`polarity`, `time`, expected no false consolidation), and positive domain,
exact-name/alias, short-identifier and Unicode controls. Its hash is
`28a36b121a0e2126b0d04f80e66464e8668fa20d75291f59fd5800f2d8b3b7f2`.
The [source lock](source_locks/ontology_matching_v1.json),
[synthetic baseline](baselines/ontology_matching_v1.json) and
[matching notes](issue_659_matching.md) retain their original judgments and scope.
The synthetic unorganized baseline's zero false consolidation does not measure
live matching safety. The two complete native live passes preserve source/fact
content with zero false consolidation; failed provider cases remain unassessed.

The historical 41+ hour mixed-release experiment remains separate diagnostic
evidence. Neither its duration nor zero Bad@10 on the public suite establishes a
controlled speedup or false-positive safety.

## Preparation, validation and remaining gate

Initial preparation exposed a PostgreSQL mount-parent traversal failure and a
lifecycle image-inventory recovery gap. Their original receipts remain retained.
The task-owned parent permission was corrected while retaining PostgreSQL data
permissions. The evaluator now retains metadata only for an already-recorded
image whose absence is independently established; container identity checks stay
in force. Both isolated runtimes became active without editing receipts.

Further evaluator fixes handle the exact pinned baseline's legacy shared-write
session contract, request an explicit JSON Accept response, and allow empty,
disabled native preparation without granting capture readiness. Positive and
adverse real-logic tests cover those boundaries. Production defaults are unchanged.

Independent `gpt-6.1-sol/max` plan audit round 5 was conformant before the final
local checks. Two earlier audit test-fixture findings were corrected and cleared.
The complete `go test ./internal/evalharness ./cmd/eval-runner -count=1` run passed
in 648.293 seconds; vet, formatting and whitespace checks passed. The required
lightweight decision-provider deployment helper passed all three tests; this
was not a live decision-provider evaluation. Evaluation-build checks are retained.
Final published-source Development E2E, PR review, CodeQL and full CI/E2E receipts
belong to the reporting PR; local success is not a delivery-completion claim.

## Risk

- Treating stream/pool capacity as application admission produces a false
  comparison. Verify the actual advisory-lock owner before any new run and
  obtain an approved resolution of the frozen admission mismatch.
- Changing concurrency, release code, retries or provider settings to rescue this
  run would change its experiment. Preserve this failure and use a separately
  approved fresh protocol for any replacement.
- Sparse completions, failed requests and missing usage can suggest false
  efficiency. Keep every disposition and unknown field visible; do not score
  this partial corpus or infer a release gain.

The source and evaluator evidence is reviewable, but the comparison failed its
completion/admission gates. Keep #660 open. Dependent tickets retain their human
merge gates, and no downstream result or issue closure is claimed.
