# Recall Experiment Results

Date: 2026-06-28

Seed: `local_eval_1k_v2`

Seed hash: `sha256:80c4a253f436a1bbf352cdf6a15151ec7bd56202b73d1f7a79d61bd6e4e9ef5a`

Baseline branch: `eval/local-eval-1k-v2-baseline`

Baseline run: `tests/eval/runs/20260628T145353Z_current_logic_baseline_retry`

## Summary

The best measured branch is `exp/recall-cue-currentness-authority-combined` at commit `13f1ae8`.

It preserves perfect `recall@k=1.0000`, improves rank-1 quality, and reduces judged-bad context from `bad@k=0.6740` to `0.1130`.

## Experiment Matrix

| Branch | Run | recall@k | MRR | nDCG@k | bad@k | required rank 1 | bad rank 1 | Judgment |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| `eval/local-eval-1k-v2-baseline` | `20260628T145353Z_current_logic_baseline_retry` | 1.0000 | 0.9040 | 0.9290 | 0.6740 | 0.8140 | 0.1810 | Baseline; not good enough |
| `exp/recall-cue-rerank` | `20260628T175757Z_cue_rerank_ident_guard` | 1.0000 | 0.9970 | 0.9978 | 0.4940 | 0.9940 | 0.0010 | Strong rank-1 fix |
| `exp/recall-currentness-rerank` | `20260628T181113Z_currentness_rerank` | 1.0000 | 0.9045 | 0.9294 | 0.4460 | 0.8150 | 0.1810 | Useful bad@k reduction |
| `exp/recall-authority-rerank` | `20260628T183921Z_authority_rerank` | 1.0000 | 0.9045 | 0.9294 | 0.5210 | 0.8150 | 0.1800 | Useful bad@k reduction |
| `exp/recall-cue-currentness-combined` | `20260628T182617Z_cue_currentness_combined` | 1.0000 | 0.9975 | 0.9982 | 0.2660 | 0.9950 | 0.0010 | Better than either alone |
| `exp/recall-cue-currentness-authority-combined` | `20260628T185425Z_cue_currentness_authority_combined` | 1.0000 | 0.9980 | 0.9985 | 0.1130 | 0.9960 | 0.0000 | Best current candidate |

## Best Candidate Deltas

Against original baseline:

| Metric | Delta |
| --- | ---: |
| recall@k | +0.0000 |
| MRR | +0.0940 |
| nDCG@k | +0.0695 |
| bad@k | -0.5610 |
| bad rank 1 | -0.1810 |

Against cue+currentness:

| Metric | Delta |
| --- | ---: |
| recall@k | +0.0000 |
| MRR | +0.0005 |
| nDCG@k | +0.0004 |
| bad@k | -0.1530 |
| bad rank 1 | -0.0010 |

## What Changed

The best branch keeps the existing hybrid semantic plus keyword RRF flow and adds content-aware post-RRF adjustments before final sorting:

1. Cue rerank: handles selection queries such as "which ... should/use" and promotes directive/canonical evidence while demoting stale or disqualifying evidence.
2. Currentness rerank: handles current/as-of/latest queries and demotes archived, obsolete, replaced, draft, copied, rollback, and proposed evidence.
3. Authority rerank: handles require/canonical/authoritative queries and promotes authoritative/signed/canonical evidence while demoting informal chat, personal checklist, transcript, and unapproved evidence.

All positive boosts require matching identifier-like tokens from the query, such as `OBS-001`, `NEG-001`, or `AUT-061`, when such identifiers exist. This guard prevents neighboring template records from receiving accidental boosts.

## Remaining Gaps

The best branch still has `bad@k=0.1130`.

Residual bad evidence is concentrated in:

| Slice | Avg bad@k | Notes |
| --- | ---: | --- |
| `explicit_negation` | 1.0000 | Required record is rank 1, but one misleading negation sibling remains in top-k. |
| `obsolete_correction` | 0.2556 | Required record is rank 1, but some stale correction records remain in context. |
| `unit_trap` | 0.0000 | No bad context, but MRR is still below perfect due rank-1 misses. |
| `retraction` | 0.0000 | No bad context, but MRR is still below perfect due rank-1 misses. |

## Recommendation

Merge candidate to main should start from `exp/recall-cue-currentness-authority-combined`, not the isolated branches.

Next improvement experiments should target context suppression after rank 1 is correct:

1. Explicit-negation sibling suppression for queries where the required answer is rank 1 but a negated sibling remains in top-k.
2. A safer context-window filter that can remove low-authority/stale hard negatives after the required item is selected.
3. Unit/retraction rank polish, because those are now rank-quality issues without bad context contamination.
