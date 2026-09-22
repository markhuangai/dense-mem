# Vector Search And Ranking

Vector distance measures similarity in the configured embedding space.
Ranking combines retrieval candidates; it does not decide truth, ownership,
or effective support. Canonical hydration and lifecycle policy make those
decisions.

## Embeddings And Cosine Distance

Document and query vectors must use the same active embedding contract.
The contract fixes provider/model, dimensions, normalization, and format
compatibility. Search rejects incompatible dimensions or generations.

For nonzero vectors:

```text
cosine_similarity(a, b) = dot(a, b) / (norm(a) * norm(b))
cosine_distance(a, b) = 1 - cosine_similarity(a, b)
```

Lower cosine distance means a closer vector match. It is not calibrated
probability, assessor confidence, or an evidence-support score.

PostgreSQL calculates vector distance. Approximate candidate strategies and
full-precision reranking are described in
[ANN, HNSW, And Halfvec](ANN-HNSW-And-Halfvec).

## Candidate Lists

Full-text retrieval supplies lexical matches. Vector retrieval supplies
semantic-similarity candidates. Explicit Entity expansion can contribute
directly connected evidence. These lists are authorized and bounded before
fusion, then checked again during canonical hydration.

Evidence and Relationship discovery are separate frontiers. Community summaries
and Hypothesis statements do not become vector evidence or change the primary
evidence result budget.

## Reciprocal Rank Fusion

The evidence adapter combines branch rank contributions:

```text
score(id) = sum(weight / (60 + branch_rank))
full-text weight = 1
vector weight = 1
explicit Entity-expansion weight = 0.5
```

Known evidence is excluded. Equal evidence scores use the immutable evidence ID
as the stable tie-breaker.

Relationship discovery uses equal weight for lexical and vector branches and
groups equivalent authored Relationships by semantic identity. Its stable
ordering and coverage checks prevent equivalent copies from filling the
frontier.

For two branches with equal weights:

| Candidate | Lexical rank | Vector rank | RRF score |
| --- | ---: | ---: | ---: |
| A | 1 | 4 | 0.032018 |
| B | 3 | 2 | 0.032002 |

A ranks slightly ahead of B. This describes query correlation, not stronger
evidence.

## Cross-Profile Conflict Reranking

Conflict policy governs which support paths remain eligible or preferred for
the selected time view. It is separate from rank fusion. Authored disagreement
must remain visible through the bounded conflict projection rather than being
hidden by a source-count popularity score.

A resolved case can suppress a losing current support path while preserving
that evidence through another unrelated eligible path. Current and temporal
views apply the appropriate case/event state.

## Memory-Space Fusion

Private-bound recalls search shared and permitted private evidence separately.
The application fuses authorized branch results with equal-weight RRF constant
60. Equal scores prefer the private branch, then shared, then immutable ID.

This fusion does not permit cross-space Entity resolution or support. Current
derived Relationship/community/Hypothesis expansion remains on shared data.
See [Recall Ranking](Recall-Ranking).

## Runtime Control

Ranking weights and fusion logic are implemented server policy. There is no
public ranking-mode parameter, control-portal RRF toggle, or general mutable
ranking-contract API. Search-index generations separately retain compatible
candidate/index parameters.

A ranking change requires code review, regression evidence, and the applicable
deterministic retrieval comparison. Observational recall feedback does not
automatically train a model or update weights.

## Degradation

Read `search_states` and `degradations` alongside results. A vector frontier
may be unavailable while authorized lexical results remain useful. Required
canonical hydration failures must remain errors.

Do not reconstruct a fact from stale search-document text, treat an empty
candidate set as proof that no evidence exists, or claim full search quality
from a degraded result.

## Evaluation And Risks

Compare baseline and candidate using identical seed/suite identity and declared
provider/configuration fingerprints. Measure retrieval quality and cost,
including exact-vs-ANN candidate recall when the index strategy changes.

| Failure mode | Mitigation |
| --- | --- |
| Similar wording is interpreted as entailed evidence | Keep ranking separate from support and lifecycle validation. |
| Approximation omits the right candidate | Compare with an exact scoped oracle; reranking cannot restore an omitted row. |
| Duplicate authored facts fill the response | Group by semantic identity and maintain cross-frontier coverage. |
| A new vector contract is compared with old vectors | Enforce contract/dimension/source fences. |
| Feedback changes live ranking silently | Keep feedback observational and require evaluated policy changes. |
