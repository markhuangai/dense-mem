# Recall Experiment Results

Date: 2026-06-29

Seed: `local_eval_1k_v2`

Seed hash: `sha256:80c4a253f436a1bbf352cdf6a15151ec7bd56202b73d1f7a79d61bd6e4e9ef5a`

Baseline branch: `eval/local-eval-1k-v2-baseline`

Baseline run: `tests/eval/runs/20260628T145353Z_current_logic_baseline_retry`

## Summary

The best measured branch for the stable reusable-team eval loop is `exp/recall-vector-overfetch-reuse-fix`.

It reaches perfect measured local eval metrics on this seed, using the already-imported reusable team and `--import-seed=false`: `recall@k=1.0000`, `MRR=1.0000`, `nDCG@k=1.0000`, `bad@k=0.0000`, and `bad_rank1=0.0000`.

`bad@k` is the average count of judged-bad references in the returned top-k. `bad_rank1` is the share of cases where the top-ranked reference is judged bad. Both indicate bad output; `bad@k > 0` still matters even when `bad_rank1 = 0`, because the context can contain misleading evidence below rank 1.

## Reusable-Team Baseline

The current eval process reuses a stable imported `local_eval_1k_v2` team and does not re-import the 4,000-row corpus for ranking/search-only experiments. This avoids hour-scale imports and keeps candidate comparisons on the same stored data.

| Branch | Run | recall@k | MRR | nDCG@k | bad@k | required rank 1 | bad rank 1 | Judgment |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| `exp/recall-identifier-specificity-rerank` | `20260629T013827Z_local_eval_1k_current_logic_reuse_baseline` | 0.9730 | 0.9574 | 0.9612 | 0.1010 | 0.9500 | 0.0000 | Current logic on the reusable team; not good enough because global vector neighbors crowd out same-team candidates before tenant filtering. |
| `exp/recall-vector-overfetch-reuse-fix` | `20260629T014939Z_local_eval_1k_vector_overfetch_reuse_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good; queries more vector candidates before the existing team/status filter, then caps returned hits to the requested limit. |

Delta from reusable-team current logic to vector overfetch: `recall@k +0.0270`, `MRR +0.0426`, `nDCG@k +0.0388`, `bad@k -0.1010`.

## Research Basis

The experiments follow the common multi-stage retrieval pattern used by current RAG systems:

- Keep hybrid retrieval plus RRF as the high-recall first stage. Azure AI Search documents RRF as the fusion method for parallel hybrid/vector result lists, and Google Vector Search describes hybrid semantic plus token search with RRF as a standard quality pattern.
- Add a post-retrieval precision stage. Pinecone documents reranking as a two-stage retrieval method where initial candidates are retrieved first, then scored again for query relevance.
- Treat context cleanup as a postprocessor/compressor stage. LangChain's contextual compression and LlamaIndex node postprocessors both place filtering/reranking between retrieval and response synthesis.
- Keep MMR/diversity as a follow-up, not the first fix. Qdrant documents MMR as useful for redundant or very similar results; our baseline showed the larger problem was misleading authority/currentness/negation siblings, so targeted rerank was the lower-risk first experiment.

References:

- Azure AI Search: [RRF in hybrid search](https://learn.microsoft.com/en-us/azure/search/hybrid-search-ranking)
- Google Vector Search: [hybrid search and reranking](https://docs.cloud.google.com/gemini-enterprise-agent-platform/build/vector-search/about-hybrid-search)
- Pinecone: [rerank results](https://docs.pinecone.io/guides/search/rerank-results)
- LangChain: [contextual compression](https://www.langchain.com/blog/improving-document-retrieval-with-contextual-compression)
- LlamaIndex: [node postprocessors](https://developers.llamaindex.ai/python/framework/module_guides/querying/node_postprocessors/)
- Qdrant: [MMR search relevance](https://qdrant.tech/documentation/search/search-relevance/)

## Experiment Matrix

| Branch | Run | recall@k | MRR | nDCG@k | bad@k | required rank 1 | bad rank 1 | Judgment |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| `eval/local-eval-1k-v2-baseline` | `20260628T145353Z_current_logic_baseline_retry` | 1.0000 | 0.9040 | 0.9290 | 0.6740 | 0.8140 | 0.1810 | Baseline; not good enough |
| `exp/recall-cue-rerank` | `20260628T175757Z_cue_rerank_ident_guard` | 1.0000 | 0.9970 | 0.9978 | 0.4940 | 0.9940 | 0.0010 | Strong rank-1 fix |
| `exp/recall-currentness-rerank` | `20260628T181113Z_currentness_rerank` | 1.0000 | 0.9045 | 0.9294 | 0.4460 | 0.8150 | 0.1810 | Useful bad@k reduction |
| `exp/recall-authority-rerank` | `20260628T183921Z_authority_rerank` | 1.0000 | 0.9045 | 0.9294 | 0.5210 | 0.8150 | 0.1800 | Useful bad@k reduction |
| `exp/recall-cue-currentness-combined` | `20260628T182617Z_cue_currentness_combined` | 1.0000 | 0.9975 | 0.9982 | 0.2660 | 0.9950 | 0.0010 | Better than either alone |
| `exp/recall-cue-currentness-authority-combined` | `20260628T185425Z_cue_currentness_authority_combined` | 1.0000 | 0.9980 | 0.9985 | 0.1130 | 0.9960 | 0.0000 | Previous best |
| `exp/recall-fragment-temporal-rerank` | `20260628T211142Z_fragment_temporal_rerank_date_priority` | 1.0000 | 0.9980 | 0.9985 | 0.0990 | 0.9960 | 0.0000 | Previous best |
| `exp/recall-historical-sibling-suppression` | `20260628T212723Z_historical_sibling_suppression` | 1.0000 | 0.9985 | 0.9989 | 0.0090 | 0.9970 | 0.0000 | Previous best |
| `exp/recall-zero-score-context-filter` | `20260628T215221Z_zero_score_filter_active_currentness` | 1.0000 | 0.9985 | 0.9989 | 0.0000 | 0.9970 | 0.0000 | Previous best |
| `exp/recall-identifier-specificity-rerank` | `20260628T221954Z_unit_identifier_specificity_rerank` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Best candidate on its original imported team |
| `exp/recall-vector-overfetch-reuse-fix` | `20260629T014939Z_local_eval_1k_vector_overfetch_reuse_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Best candidate for reusable-team loop |

## Best Candidate Deltas

Against original baseline:

| Metric | Delta |
| --- | ---: |
| recall@k | +0.0000 |
| MRR | +0.0960 |
| nDCG@k | +0.0710 |
| bad@k | -0.6740 |
| bad rank 1 | -0.1810 |

Against zero-score context filter:

| Metric | Delta |
| --- | ---: |
| recall@k | +0.0000 |
| MRR | +0.0015 |
| nDCG@k | +0.0011 |
| bad@k | +0.0000 |
| bad rank 1 | +0.0000 |

## What Changed

The best branch keeps the existing hybrid semantic plus keyword RRF flow and adds content-aware post-RRF adjustments before final sorting:

1. Cue rerank: handles selection queries such as "which ... should/use" and promotes directive/canonical evidence while demoting stale or disqualifying evidence.
2. Currentness rerank: handles current/as-of/latest queries and demotes archived, obsolete, replaced, draft, copied, rollback, and proposed evidence.
3. Authority rerank: handles require/canonical/authoritative queries and promotes authoritative/signed/canonical evidence while demoting informal chat, personal checklist, transcript, and unapproved evidence.
4. Date-priority currentness rerank: carries raw fragment `created_at` and `updated_at` through semantic and keyword search, parses explicit `YYYY-MM-DD` dates in matching fragment content, and prevents undated "current/now" wording from receiving a positive lexical boost when a matching dated candidate exists.
5. Historical sibling suppression: for selection queries, detects when a matching directive answer exists and applies a stronger penalty to historic action siblings such as `Before 2026 ... used ...`.
6. Zero-score context filter: after post-RRF adjustments, drops non-positive fragment candidates when at least one positive fragment candidate exists, preventing strongly demoted stale fragments from filling remaining context slots. The branch also treats `active` queries as currentness queries so active retraction updates are reranked consistently.
7. Unit identifier specificity rerank: for timeout/job value queries, gives a small boost to fragments containing the exact identifier from the query. This fixes neighboring job collisions such as `UNT-003` outranking `UNT-013` while avoiding the broader exact-ID boost that reintroduced bad context in other slices.
8. Vector tenant overfetch: asks Neo4j's global vector index for additional candidates before the existing `team_id` and active-status filter, then caps returned semantic hits to the requested limit. This fixes same-team candidate starvation when repeated eval imports make the global vector index crowded with other teams' near neighbors.

All positive boosts require matching identifier-like tokens from the query, such as `OBS-001`, `NEG-001`, or `AUT-061`, when such identifiers exist. This guard prevents neighboring template records from receiving accidental boosts.

## Remaining Gaps

The best reusable-team branch has perfect measured metrics on this seed.

Remaining work is now about generalization beyond this synthetic seed:

| Area | Current metric | Notes |
| --- | ---: | --- |
| `unit_trap` | MRR 1.0000, bad@k 0.0000 | Scoped exact-ID boost fixed remaining neighboring-job rank misses. |
| All adversarial slices | bad@k 0.0000 | No judged-bad context remains in top-k on local_eval_1k_v2. |
| Non-synthetic workloads | Not measured | Need validation because zero-score filtering can intentionally return fewer than `limit` fragments. |

## Recommendation

Merge candidate to main should start from `exp/recall-vector-overfetch-reuse-fix`, which builds on `exp/recall-identifier-specificity-rerank`.

Next improvement experiments should target generalization:

1. Validate zero-score filtering against non-synthetic workloads, because returning fewer than `limit` fragments is intentional but changes context volume.
2. Broader temporal parsing beyond ISO `YYYY-MM-DD`, including natural language dates, before relying on date-priority rerank outside the synthetic seed.
3. Replace hard-coded cue lists with learned or configurable rerank features if future seeds expose broader language variation.
