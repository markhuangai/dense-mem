---
workflow: "evidence"
order: 13
anchor: "combine-ranks"
legacy: "step-13"
title: "Combine the ranked lists"
chapter: "Recall"
figure: "Reciprocal rank fusion"
scene: "rank"
fallback: "The first text and vector hit earns two reciprocal-rank contributions."
caption: "Switch search modes to recalculate the example. These scores describe order within the shared space."
question: "What changes if the vector is unavailable?"
details: ["Choose Words only in the diagram. With the same eligible corpus, E1 still has a lexical match. E3 and E2 have no matching text hit and leave this candidate list. A failed query embedding produces an explicit optional degradation.","With optional decision reranking enabled, eligible hydrated candidates can be rescored for relevance before the final limit. A failed optional rerank retains the original authorized ranking and reports the degradation. Multiple allowed spaces have a further rank-fusion step."]
refs: ["internal/recall/retrieval.go:147","internal/recall/recall.go:215","internal/recall/decisions.go:95","internal/recall/recall_spaces.go:135"]
guide: "/Recall-Ranking/"
---

Each text or vector hit contributes 1 ÷ (60 + its position in that list). E1 is first twice, so its score is 1/61 + 1/61. E3 receives 1/62 from the vector list.

Recall orders candidates by the sum. The score measures ranking support from the search channels. It cannot tell a reader whether the policy itself is true.
