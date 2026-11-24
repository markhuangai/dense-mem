---
workflow: "evidence"
order: 9
anchor: "compare-meaning"
legacy: "step-9"
title: "Check whether meanings are equivalent"
chapter: "Organize"
figure: "An Evidence pair"
scene: "compare"
fallback: "Retention duration and the legal-hold exception answer different questions."
caption: "Two passages share a topic here. They remain separate answers to different questions."
question: "How is this different from intake reuse?"
details: ["Intake reuse chooses a canonical Evidence identity for a new occurrence. Evidence equivalence groups eligible stored Evidence with the same meaning. Each member retains its own text and provenance.","The worker checks labels, metadata, relative time, and owner-sensitive wording before recording an equivalence outcome. Ambiguous comparisons can remain unresolved."]
source_revision: 6767dd33f5d3e2546b4afac39122db7bf01549f9
refs: ["internal/topics/contract/equivalence.go:15","internal/topics/maintenance.go:313","internal/recall/recall_ontology.go:80"]
guide: "/Topics-And-Organization/"
---

The worker assesses Evidence equivalence separately from Topic assignment. E1 says how long routine snapshots last. E3 says what suspends expiry. Knowing either passage alone leaves a different question unanswered.

The pair decision is distinct. When two eligible passages do have the same meaning in compatible contexts, a published equivalence group can let Recall return one representative with alternate IDs.
