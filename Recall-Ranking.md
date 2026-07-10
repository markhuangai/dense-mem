# Recall Ranking

Dense-Mem recall produces one primary ranked list containing active facts,
validated claims, and source fragments. Clarifications and related dream
hypotheses are assembled separately.

See [Technical Reference](Technical-Reference#recall-endpoint) for request
parameters, [Evaluation](Evaluation) for runner commands, and
[Telemetry](Telemetry#online-recall-feedback) for online feedback.

## Ranking Pipeline

```text
query
  -> semantic fragment search ----\
  -> keyword fragment search ------> reciprocal-rank fusion
                                      -> query-aware fragment adjustments
  -> active fact search -----------\
  -> validated claim search --------> hydrate and filter typed memories
                                      -> merge by effective tier
                                      -> temporal order within a tier
                                      -> tier-specific score
                                      -> top K
                                      -> optional community fill
```

The semantic and keyword fragment branches run concurrently. The semantic
branch embeds the query and searches stored fragment embeddings. The keyword
branch searches fragment content and recall text. Recall fails closed if either
required fragment branch fails.

Each branch normally requests ten times the final limit. A legacy
digit-oriented identifier detector raises small candidate pools to at least 50
rows. Branch results are then filtered by profile, fragment type, and requested
time window before fusion. The larger identifier floor does not activate for
every hard or ranking anchor; see [Current Limitations](#current-limitations).

Fragment candidates are merged with reciprocal-rank fusion (RRF):

```text
base_score =
  1 / (60 + semantic_rank)
  + 1 / (60 + keyword_rank)
```

A branch contributes nothing when a fragment is absent from that branch. RRF
combines branch order rather than raw cosine and BM25 scores, which avoids
normalizing two different score scales.

After fusion, Dense-Mem applies deterministic query-aware adjustments:

| Adjustment | Behavior |
| --- | --- |
| Identifier specificity | Adds `0.004` for each matching ranking anchor. |
| Currentness | Rewards matching current or active cues and penalizes stale or expired alternatives for currentness queries. |
| Selection cues | Rewards matching directives and penalizes disqualified or historical alternatives for “which ... should/use” queries. |
| Authority | Rewards authoritative or canonical evidence and suppresses informal alternatives for source-of-truth or required-policy queries. |

The adjusted value is the fragment's `final_score`. Adjustments can reduce a
score to zero; when positive candidates exist, non-positive fragments are
removed before final ranking.

Active facts and validated claims are searched separately through their recall
indexes. Candidates are hydrated and checked again against the profile, time
window, and query. Active facts use truth score for within-tier ordering.
Validated claims use extraction confidence multiplied by the configured claim
weight. A failure in optional fact or claim enrichment is logged and skipped;
fragment recall still completes.

## Effective Tiers

Dense-Mem sorts by effective tier before comparing scores.

| Effective tier | Candidate |
| --- | --- |
| `0.5` | Source fragment for an explicit source, note, or evidence query. |
| `0.75` | Source fragment matching a ranking anchor, unless the query asks for authority. |
| `1` | Authoritative active fact. |
| `1.25` | Active fact with overlay or conflict state. |
| `1.5` | Validated claim. |
| `2` | Other source fragment. |

The `0.5` and `0.75` values are internal sort overrides. A returned source
fragment still reports its public memory tier as `2`.

Within the same effective tier, currentness queries can use temporal order.
When temporal order does not decide the result, Dense-Mem compares the
tier-specific score.

Community expansion is opt-in. It fills unused result slots after direct recall
and does not replace a complete direct result list.

## Identifier Anchor Sets

For source fragments, broad extraction does not by itself trigger hard gating
or tier promotion. Typed fact and claim admission reuses broad extraction
separately. Candidate overfetch uses an older detector rather than any of the
three anchor sets below.

| Set | Used for | Examples |
| --- | --- | --- |
| Hard-gate anchors | Remove source fragments that do not contain every required exact anchor. | PR or issue references, 7-to-40-character hexadecimal-looking tokens, versions, scopes such as `feedback:read`, underscore tool names, recognized source/config filenames, and project-like paths. |
| Ranking anchors | Add a small exact-match score adjustment and give a matching fragment effective tier `0.75`. | All hard-gate anchors, context-qualified IDs such as `job UNT-013` or `issue DM-412`, and selected filenames such as `thumbs.db` or `smsvchost.exe`. |
| Strong anchors | Admit hydrated fact and claim candidates when a query contains identifier-like text. | A broader exact set that includes slash, colon, underscore, dotted, version, hash, and letter-number forms. |

Every hard-gate anchor is also a ranking anchor. One source-fragment candidate
must contain every hard-gate anchor in the query; matches are not combined
across several candidates. Each matching ranking anchor adds `0.004` to the
fragment score. Any ranking-anchor match also activates effective tier `0.75`
unless the query asks for an authoritative or required source.

Context-qualified ranking IDs require both an entity word and an ID-shaped
value. Supported entity words are `account`, `bug`, `issue`, `job`, `project`,
`queue`, `service`, `task`, and `ticket`.

Source-fragment regression tests keep natural-language forms such as
`MicroRNA`, `mRNAs`, `miRNA`, `SHP-2`, `U.S.`, and `singer/songwriter` out of
hard and ranking anchors. These examples are not a complete grammar. Typed fact
and claim matching still uses broad extracted tokens plus strong anchors.

## Current Limitations

The current anchor rules have five important limitations:

- Source-fragment hard gates use AND semantics. One candidate must contain
  every hard anchor; evidence split across fragments cannot satisfy the gate
  collectively.
- Identifier overfetch uses an older digit-oriented detector rather than
  `HardGateAnchors` or `RankingAnchors`. Exact filtering or promotion can
  therefore activate without the larger candidate pool.
- The hexadecimal detector accepts any standalone 7-to-40-character token
  containing only hexadecimal characters. An ordinary all-letter word such as
  `defaced` can be classified as an anchor.
- Typed fact and claim admission is broader than source-fragment ranking.
  Identifier-like natural language excluded by `RankingAnchors` can still
  require exact identifier overlap in typed candidates.
- A bare ID such as `PROJ-123` is not a ranking anchor unless a hard-anchor rule
  recognizes it or a supported entity word qualifies it.

Treat these as implementation constraints when diagnosing misses. The current
rules do not guarantee that every identifier-shaped query receives the larger
candidate pool or that matching evidence can satisfy a gate across multiple
rows.

## Related Dreams

`recall_memory` retrieves related dreams after building the primary result
list. Dream hypotheses are filtered and reranked separately using their source
overlap with primary hits, identifier and lexical overlap, likelihood,
confidence, and status.

Related dreams remain in `related_dreams`; they do not become facts or enter
the primary ranked list. See [Dreaming](Dreaming) for lifecycle and resolution
behavior.

## Recall Feedback Is Record Only

When online recall feedback is enabled, `recall_memory` can return a
`recall_event`. Dense-Mem stores bounded references to the returned results.
After answering the user, the host can submit a judgment with
`submit_recall_session_feedback`.

The current feedback path records investigation data in
`recall_feedback_events` and records bounded metrics. It does not automatically:

- rewrite a memory
- change a fact, claim, fragment, or dream
- update an embedding or search index
- alter a ranking weight
- train a model

A low or medium judgment changes recall behavior only after the feedback is
turned into a labeled case, a code or configuration change is made, and that
change passes evaluation.

`dream_feedback` on a recall event is also record-only. Use
`resolve_dream_feedback` when confirmed evidence should change a dream's status
or enter the normal memory pipeline.

## Reading Evaluation Traces

`recall_traces.jsonl` contains normalized references plus the raw evaluation
tool response.

| Field | Meaning |
| --- | --- |
| `ranked_refs` | Final normalized primary results with type, ID, and rank. |
| `raw.ranked_refs[].tier` | Public memory tier. It does not expose internal `0.5` or `0.75` overrides. |
| `raw.ranked_refs[].semantic_rank` | One-based fragment rank from semantic search; `0` means absent. |
| `raw.ranked_refs[].keyword_rank` | One-based fragment rank from keyword search; `0` means absent. |
| `raw.ranked_refs[].final_score` | Post-adjustment fused score for a fragment. |
| `raw.ranked_refs[].score` | Fragment final score, fact truth score, or weighted claim extraction confidence. |
| `context_refs` | Normalized assembled-context order. |
| `context_evidence_refs` | Supporting fragment refs attached to typed context items. |
| `dream_refs` | Separately ranked dream refs when the case requests dreams. |
| `latency_ms` | Duration of the primary recall invocation. |
| `context_block_chars` | Size of the assembled context block. |

For example, a fragment at semantic rank `4` and keyword rank `2` has this base
score before query-aware adjustments:

```text
1 / (60 + 4) + 1 / (60 + 2) = 0.031754...
```

The evaluation tool invokes primary recall once for `ranked_refs`; context
assembly invokes recall again for `context_refs`. Treat the two lists as
separate observations rather than assuming byte-for-byte equality.

`retrieval_scores.jsonl` records per-case Recall@K, MRR, nDCG@K, judged-bad
refs, first-required rank, and missing required refs. It records parallel
context, evidence, and dream fields when those axes are available.

## Evaluation Semantics And Gates

Evaluation has two important normalization behaviors:

- When a returned ref and a qrel target have different types but share one
  unambiguous source-document mapping, the scorer can project the returned ref
  to the qrel's target type. This measures source relevance across memory tiers;
  it does not prove that the raw result had the requested fact or claim type.
- When evidence qrels target only source fragments and
  `context_evidence_refs` is empty, the scorer falls back to fragment entries in
  `context_refs`. On a fragment-only seed, evidence metrics can therefore mirror
  context retrieval instead of measuring a separate fact-to-claim-to-fragment
  lineage path.

Use the same scorer revision for baseline and candidate runs. Inspect raw trace
types when validating tier behavior, and use typed cases with explicit evidence
qrels when validating lineage.

The comparison command requires matching seed hashes and reports candidate
deltas for ranked, context, evidence, and dream metrics. A negative delta does
not fail comparison mode automatically. The runner supports absolute minimum
and maximum gates for recall, rank 1, judged-bad results, context, evidence, and
dreams. A configured context, evidence, or dream gate fails closed when no
cases were scored for that axis.

For ranking changes, use both:

1. a broad non-regression suite for general retrieval
2. a targeted suite for the changed failure mode

The generated `feedback_recall_hardcases_v1` suite covers exact identifiers,
metadata recall text, authority, branch/run identifiers, and related-dream
selection. Run it alongside a broad suite rather than using it as a replacement.

For a valid paired comparison, keep the seed hash, scorer revision, import
method, database lifecycle, and runtime configuration the same. Review
per-slice and per-case changes in addition to aggregate averages. See
[Evaluation](Evaluation) for commands, artifacts, and gate flags.
