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
details: ["Intake reuse chooses a canonical Evidence identity for a new occurrence. Ontology equivalence organizes already stored Evidence identities. Each member retains its own text and provenance.","The organizer checks context such as labels, metadata, relative time, and owner-sensitive wording. A keep-separate override or an incompatible context constrains the pair decision. An ambiguous comparison can be recorded explicitly."]
refs: ["internal/ontology/request.go:128","internal/ontology/contract/equivalence.go:15","internal/ontology/publication.go:125","internal/recall/recall_ontology.go:80"]
guide: "/Topics-And-Organization/"
---

The organizer also compares eligible pairs. E1 says how long routine snapshots last. E3 says what suspends expiry. Knowing either passage alone leaves a different question unanswered.

The pair decision is distinct. When two eligible passages do have the same meaning in compatible contexts, a published equivalence group can let Recall return one representative with alternate IDs.
