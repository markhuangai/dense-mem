# Recall Experiment Results

Date: 2026-06-29

Seed: `local_eval_1k_v2`

Seed hash: `sha256:80c4a253f436a1bbf352cdf6a15151ec7bd56202b73d1f7a79d61bd6e4e9ef5a`

Baseline branch: `eval/local-eval-1k-v2-baseline`

Baseline run: `tests/eval/runs/20260628T145353Z_current_logic_baseline_retry`

## Summary

The current combined branch is `exp/eval-context-ref-scoring`.

It reaches perfect measured local eval metrics on this seed using the already-imported reusable team and `--import-seed=false`: `recall@k=1.0000`, `MRR=1.0000`, `nDCG@k=1.0000`, `bad@k=0.0000`, and `bad_rank1=0.0000`. The latest run also scores assembled context explicitly: `context_scored=1000`, `context_recall@k=1.0000`, `context_bad@k=0.0000`, and `context_bad_rank1=0.0000`. It also passes the focused live `local_tiered_v1` fact/claim seed after sending temporal scope through verifier and context assembly paths.

The latest `local_eval_1k_v2` run for this branch is still a no-import regression check against the stable imported team. It does not fully exercise verifier/write-path behavior because those records were imported before the verifier-scope branch. A full 1k write-path measurement would require re-importing `local_eval_1k_v2`.

`bad@k` is the average count of judged-bad references in the returned ranked top-k. `bad_rank1` is the share of cases where the top-ranked reference is judged bad. Both indicate bad ranked retrieval. `context_bad@k` and `context_bad_rank1` apply the same judgment to `context_refs`, so they answer whether judged-bad material reached assembled context. `bad@k > 0` or `context_bad@k > 0` still matters even when the matching rank-1 rate is zero, because misleading evidence can sit below rank 1.

## Reusable-Team Baseline

The current eval process reuses a stable imported `local_eval_1k_v2` team and does not re-import the 4,000-row corpus for ranking/search-only experiments. This avoids hour-scale imports and keeps candidate comparisons on the same stored data.

| Branch | Run | recall@k | MRR | nDCG@k | bad@k | required rank 1 | bad rank 1 | Judgment |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| `exp/recall-identifier-specificity-rerank` | `20260629T013827Z_local_eval_1k_current_logic_reuse_baseline` | 0.9730 | 0.9574 | 0.9612 | 0.1010 | 0.9500 | 0.0000 | Current logic on the reusable team; not good enough because global vector neighbors crowd out same-team candidates before tenant filtering. |
| `exp/recall-vector-overfetch-reuse-fix` | `20260629T014939Z_local_eval_1k_vector_overfetch_reuse_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good; queries more vector candidates before the existing team/status filter, then caps returned hits to the requested limit. |
| `exp/recall-tier-fact-claim-vector-overfetch` | `20260629T020934Z_local_eval_1k_vector_tier_fact_claim_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good; preserves vector-overfetch fragment metrics while adding typed fact/claim eval and tier currentness logic. |
| `exp/recall-tier-fact-claim-vector-overfetch` | `20260629T_temporal_metadata_no_identifier_filter_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good; preserves reusable 1k metrics while adding fragment temporal metadata filtering. |
| `exp/recall-evidence-intent-fragments` | `20260629T_evidence_source_tight_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good; preserves reusable 1k metrics while adding evidence-source fragment intent and stricter typed eval import validation. |
| `exp/recall-relative-temporal-cues` | `20260629T_relative_temporal_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good; preserves reusable 1k metrics while adding relative temporal phrase support for fragment currentness. |
| `exp/recall-month-name-temporal-cues` | `20260629T_month_name_temporal_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good; preserves reusable 1k metrics while adding month-name date support for fragment currentness. |
| `exp/recall-typed-content-temporal-cues` | `20260629T_typed_content_temporal_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good; preserves reusable 1k metrics while adding typed fact/claim content-date currentness fallback. |
| `exp/recall-typed-evidence-temporal-cues` | `20260629T_typed_evidence_temporal_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good; preserves reusable 1k metrics while adding supporting-fragment date fallback for typed facts and claims. |
| `exp/recall-temporal-verifier-context` | `20260629T_temporal_verifier_context_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good regression check; preserves reusable 1k metrics while adding verifier temporal scope. This is not a full write-path 1k measurement because the stable corpus was already imported. |
| `exp/recall-context-valid-window-scope` | `20260629T_context_valid_window_scope_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good regression check; preserves reusable 1k metrics while adding temporal scope to context assembly. This is not a full write-path 1k measurement because the stable corpus was already imported. |
| `exp/eval-context-ref-scoring` | `20260629T_eval_context_ref_scoring_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good scoring harness change; preserves ranked metrics and adds context metrics. Context-scored cases: 1000; `context_recall@k=1.0000`, `context_bad@k=0.0000`, `context_bad_rank1=0.0000`. |

Delta from reusable-team current logic to vector overfetch: `recall@k +0.0270`, `MRR +0.0426`, `nDCG@k +0.0388`, `bad@k -0.1010`.

Delta from vector overfetch to tier fact/claim branch on `local_eval_1k_v2`: all measured deltas are `0.0000`.

## Fact/Claim Tier Coverage

The `local_eval_1k_v2` suite mostly measures fragment retrieval. `local_tiered_v1` adds a focused typed coverage surface for active facts, validated claims, cross-tier source-evidence intent, typed content-date fallback, typed evidence-date fallback, and fragment natural-language temporal currentness. Current expanded seed hash: `sha256:6131d4ae0f597a265cf46dc306779ef5c5137051f8a10284da973924d05242a5`.

| Branch | Run | recall@k | MRR | nDCG@k | bad@k | required rank 1 | bad rank 1 | Judgment |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| `exp/recall-tier-fact-claim-vector-overfetch` | `20260629T020842Z_local_tiered_vector_fact_claim_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good initial coverage for fact-vs-fact and claim-vs-claim currentness ordering. |
| `exp/recall-tier-fact-claim-vector-overfetch` | `20260629T022642Z_local_tiered_expanded_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good expanded coverage for same-tier currentness, fact-over-fragment, claim-over-fragment, and cross-identifier fact filtering. |
| `exp/recall-tier-fact-claim-vector-overfetch` | `20260629T_temporal_metadata_no_identifier_filter_local_tiered_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good temporal coverage for `valid_at` fact/claim windows and fragment metadata windows without broad fragment identifier filtering. |
| `exp/recall-evidence-intent-fragments` | `20260629T_evidence_source_tight2_local_tiered_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good evidence-source coverage: source-note query returns the raw fragment even when a derived fact and claim exist. |
| `exp/recall-relative-temporal-cues` | `20260629T_relative_temporal_local_tiered_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good relative temporal coverage: a "yesterday" fragment update outranks an undated fragment that says current. |
| `exp/recall-month-name-temporal-cues` | `20260629T_month_name_temporal_local_tiered_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good month-name temporal coverage: a `June 27, 2026` fragment update outranks an undated fragment that says current. |
| `exp/recall-typed-content-temporal-cues` | `20260629T_typed_content_temporal_local_tiered_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good typed content-date coverage: facts and claims with no `valid_from` rank by dates present in their triple text before falling back to `recorded_at`. |
| `exp/recall-typed-evidence-temporal-cues` | `20260629T_typed_evidence_temporal_local_tiered_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good typed evidence-date coverage: facts and claims with no `valid_from` or triple date rank by dates in supporting source fragments before falling back to `recorded_at`. |
| `exp/recall-temporal-verifier-context` | `20260629T_temporal_verifier_context_local_tiered_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good live import coverage: claim verification receives `valid_from` / `valid_to`, so later updates outside a claim's validity window do not incorrectly contradict the scoped claim. |
| `exp/recall-context-valid-window-scope` | `20260629T_context_valid_window_scope_local_tiered_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good live import and context coverage on corrected seed hash `6131d4ae...`: windowed old fact/claim rows include `valid_to`, and `eval_run_recall_case` context assembly passes `valid_at` / `known_at` into recall. |
| `exp/eval-context-ref-scoring` | `20260629T_eval_context_ref_scoring_local_tiered_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Good scoring harness change; context gates passed on 14/14 cases with `context_recall@k=1.0000`, `context_bad@k=0.0000`, and `context_bad_rank1=0.0000`. |

This branch also fixes the eval harness so qrels can resolve `source_doc_id` as a fragment, claim, or fact, fixes optional `valid_from` / `valid_to` persistence for claim creation and fact promotion, and fails typed live eval imports when required refs are not mapped after import.

Before `exp/recall-temporal-verifier-context`, the updated verifier model failed the focused valid-window import with `local_tiered_v1_fact_valid_window_old` unmapped after import. The old claim was marked `disputed` because the verifier received only `subject predicate object` plus supporting fragment text; it did not receive the claim's time bounds.

During the lazy evidence hydration experiment, run `20260629T_lazy_evidence_temporal_hydration_local_tiered_candidate` regressed to `recall@k=0.9286` because `local_tiered_v1_claim_valid_window_old` remained a `candidate`: the fixture described a claim that was valid before the 2026-06-27 update but provided only `valid_from`. That branch was discarded. The seed now gives the old windowed fact and claim explicit `valid_to=2026-06-27T00:00:00Z`.

The same investigation found that `eval_run_recall_case` passed `valid_at` / `known_at` to ranked recall but not to context assembly. Before the fix, the claim valid-window trace could rank the old claim while `context_refs` contained the future bad claim. `exp/recall-context-valid-window-scope` fixes that mismatch.

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
| `exp/recall-tier-fact-claim-vector-overfetch` | `20260629T020934Z_local_eval_1k_vector_tier_fact_claim_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Best candidate for fragments plus focused fact/claim coverage |
| `exp/recall-tier-fact-claim-vector-overfetch` | `20260629T_temporal_metadata_no_identifier_filter_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Best candidate with fragment temporal metadata filtering |
| `exp/recall-evidence-intent-fragments` | `20260629T_evidence_source_tight_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Best candidate with evidence-source fragment intent and typed eval required-ref validation |
| `exp/recall-relative-temporal-cues` | `20260629T_relative_temporal_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Best candidate with initial relative temporal phrase support |
| `exp/recall-month-name-temporal-cues` | `20260629T_month_name_temporal_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Best candidate with relative plus month-name temporal phrase support |
| `exp/recall-typed-content-temporal-cues` | `20260629T_typed_content_temporal_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Best candidate with typed fact/claim content-date fallback |
| `exp/recall-typed-evidence-temporal-cues` | `20260629T_typed_evidence_temporal_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Best candidate with typed supporting-evidence date fallback |
| `exp/recall-temporal-verifier-context` | `20260629T_temporal_verifier_context_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Best combined branch; 1k no-import regression only for the verifier/write-path change |
| `exp/recall-context-valid-window-scope` | `20260629T_context_valid_window_scope_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Best combined branch with context temporal scope; 1k no-import regression only |
| `exp/eval-context-ref-scoring` | `20260629T_eval_context_ref_scoring_local_eval_1k_candidate` | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | Best combined branch plus explicit context scoring; `context_bad@k=0.0000` on 1000 context-scored cases |

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
9. Tier fact/claim currentness: for currentness queries, active facts and validated claims sort within their tier by newest `valid_from`, falling back to `recorded_at`. Tier search now uses the internal overfetch size before final truncation, and hydrated fact/claim hits must match explicit query identifiers before tier precedence can place them above other results.
10. Fragment temporal metadata filtering: fragment searchers now decode current `metadata_json` as well as legacy `metadata`, and recall filters fragment candidates with structured `valid_from` / `valid_to` metadata when `valid_at` is supplied.
11. ISO date identifier guard: ISO date tokens are excluded from identifier matching so `as of 2026-06-22` queries match the entity ID rather than requiring the content to repeat the query date. A broader fragment identifier filter was tested and rejected because it raised reusable 1k `bad@k` by surfacing same-identifier decoys below the correct rank-1 result.
12. Evidence-source fragment intent: source-note/evidence/document/note queries temporarily sort fragment candidates above derived facts and claims while preserving public tier values. This is intentionally narrower than plain `source` so `source of truth` authority queries still prefer active facts.
13. Typed eval import validation: live eval imports now fail before scoring when typed claim import returns an error or when a required qrel ref is unmapped after import. Bad refs may remain unmapped when promotion policy rejects them.
14. Relative temporal fragment cues: currentness rerank now treats `today`, `yesterday`, `N days ago`, and `last week` as content dates anchored to the fragment's own timestamp. This lets a relative dated update outrank an undated fragment that merely says current.
15. Month-name temporal fragment cues: currentness rerank now parses dates such as `June 27, 2026`, `Jun 27`, and `27 June 2026`, using the fragment timestamp's year when the content omits a year. This handles dated updates that do not use ISO date format.
16. Typed fact/claim content-date fallback: currentness sorting for active facts and validated claims now uses `valid_from` first, then parses temporal dates from the hydrated triple text, then falls back to `recorded_at`. Community expansion uses the same helper, so direct and expanded recall sort typed hits consistently.
17. Typed fact/claim evidence-date fallback: when `valid_from` and triple text do not contain a date, currentness sorting batches and reads supporting fragments from fact/claim evidence, parses temporal dates from those fragment contents, then falls back to `recorded_at`. The response still strips evidence unless `include_evidence` is requested.
18. Verifier temporal scope: claim verification now passes `valid_from` / `valid_to` into the verifier request, and the OpenAI verifier includes those bounds in the JSON payload. This prevents later or earlier evidence outside a claim's stated validity window from being treated as a contradiction for the scoped claim.
19. Context temporal scope: `assemble_context` accepts `valid_at` / `known_at`, context assembly forwards them to recall, and `eval_run_recall_case` uses the same temporal window for `ranked_refs` and `context_refs`.
20. Context scoring: eval summaries keep existing ranked metrics unchanged and add `context_*` metrics/gates when traces include `context_refs`, so final assembled context can fail independently from ranked retrieval.

All positive boosts require matching identifier-like tokens from the query, such as `OBS-001`, `NEG-001`, or `AUT-061`, when such identifiers exist. This guard prevents neighboring template records from receiving accidental boosts.

## Remaining Gaps

The best reusable-team branch has perfect measured metrics on this seed.

Remaining work is now about generalization beyond this synthetic seed:

| Area | Current metric | Notes |
| --- | ---: | --- |
| `unit_trap` | MRR 1.0000, bad@k 0.0000 | Scoped exact-ID boost fixed remaining neighboring-job rank misses. |
| All adversarial slices | bad@k 0.0000; context_bad@k 0.0000 | No judged-bad ranked refs or assembled context refs remain in top-k on local_eval_1k_v2. |
| Non-synthetic workloads | Not measured | Need validation because zero-score filtering can intentionally return fewer than `limit` fragments. |
| Tiered facts/claims/fragments | Expanded `local_tiered_v1` live candidate: recall@k 1.0000, required rank 1 1.0000, bad@k 0.0000 | Fourteen-case seed now covers same-tier fact/claim currentness, typed fact/claim content-date fallback without `valid_from`, typed supporting-evidence date fallback, fact-over-fragment, claim-over-fragment, cross-identifier fact filtering, bounded `valid_at` temporal windows, evidence-source fragment intent, relative temporal fragment currentness, month-name temporal fragment currentness, verifier temporal scope, and matching temporal scope for eval context assembly. Keep expanding before treating it as broad acceptance coverage. |
| Typed source text dates | Partially measured | Supporting fragment dates are now measured for typed fact/claim currentness. Recall still cannot recover dates if extraction/import drops both typed temporal fields and evidence links, or if the relevant source date is outside the hydrated supporting fragments. |
| Write-path 1k coverage | Not fully measured on this branch | The 1k run for `exp/eval-context-ref-scoring` reused the stable imported team. A full write-path measurement requires re-importing `local_eval_1k_v2`, because the verifier only runs during import/promotion. |

## Recommendation

Merge candidate to main should start from `exp/eval-context-ref-scoring`, which builds on `exp/recall-context-valid-window-scope` and `exp/recall-temporal-verifier-context`.

Next improvement experiments should target generalization:

1. Validate zero-score filtering against non-synthetic workloads, because returning fewer than `limit` fragments is intentional but changes context volume.
2. Measure typed evidence-date fallback latency on non-synthetic or larger typed workloads, because it adds supporting fragment hydration for typed currentness hits.
3. Decide whether claim/fact extraction should preserve source dates as `valid_from`, triple text, or structured metadata; recall now has an evidence fallback, but typed temporal fields remain cheaper and more explicit.
4. Run a full `local_eval_1k_v2` import for `exp/recall-temporal-verifier-context` when the hour-scale cost is worth measuring write-path behavior across the full synthetic corpus.
5. Broader temporal parsing beyond relative phrases and month names, including weekdays, ambiguous numeric dates, and non-English date forms, before relying on natural-language date priority outside the synthetic seed.
6. Keep expanding `local_tiered_v1` now that live typed fact/claim/fragment scoring is stable.
7. Replace hard-coded cue lists with learned or configurable rerank features if future seeds expose broader language variation.
