---
workflow: "evidence"
order: 12
anchor: "find-candidates"
legacy: "step-12"
title: "Search by words and by meaning"
chapter: "Recall"
figure: "Two candidate lists"
scene: "channels"
fallback: "Full-text and vector branches produce their own ordered candidates."
caption: "The displayed vector order is illustrative. In a real call it depends on the embeddings and index."
question: "Where can Topics help at this point?"
details: ["The shared branch can also match a current topic’s normalized key or unambiguous alias and introduce direct members as candidates. Topic discovery by itself adds no rank-fusion points. An approximate vector search can check newly discovered candidates against the query and rank them by distance.","Our “routine snapshots” query has no matching key or alias in the example catalog. The topic hint that appears later comes from the winning Evidence’s direct membership. Eligibility filters apply to candidates before their IDs leave storage."]
source_revision: main
refs: ["internal/recall/postgres/recall_repository.go:31","internal/recall/postgres/recall_repository.go:164","internal/topics/contract/recall.go:82","internal/recall/postgres/recall_ontology_scoring.go:18"]
guide: "/Vector-Search-And-Ranking/"
sequences: ["ai_query_vector"]
---

PostgreSQL full-text search finds E1 because its search document contains both query terms. pgvector compares the query vector with eligible Evidence vectors, so passages with different wording can also surface.

Smaller cosine distance means a closer vector match. In this example E1 ranks first in both lists. E3 ranks second by vector similarity, and E2 trails it. Recall compares these positions next.
