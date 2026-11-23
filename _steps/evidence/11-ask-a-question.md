---
workflow: "evidence"
order: 11
anchor: "ask-a-question"
legacy: "step-11"
title: "Turn a question into a recall request"
chapter: "Recall"
figure: "The caller’s query"
scene: "ask"
fallback: "The host submits the search query and result limits."
caption: "The host chooses the search text and a one-result limit for this first pass."
question: "How are time and visibility handled?"
details: ["Recall accepts a query, current topic IDs, or both. On a multi-space query it searches each authorized space separately, then combines the resulting ranks. Explicit topic filters use the shared branch only.","valid_at concerns real-world Evidence time. known_at concerns what the system knew at a given time. Ontology organization is current-only. This example uses current Evidence, a working embedding provider, and no optional decision reranker."]
refs: ["internal/recall/recall.go:85","internal/recall/recall_spaces.go:50","internal/recall/postgres/recall_ontology.go:66"]
guide: "/Technical-Reference/#recall_memory"
sample: "recall"
sample_label: "recall_memory arguments"
---

A teammate asks how long Atlas keeps routine backups. The assistant calls recall_memory with the words “{{ site.data.examples.atlas.subject.query }}” and asks for one Evidence result plus up to two topic hints.

Recall resolves the caller’s allowed spaces and the active search configuration. It embeds the query once when possible. Ordinary search can consult the caller’s private and shared spaces. Here the private space is empty.
