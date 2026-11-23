---
workflow: "evidence"
order: 7
anchor: "discover-shared-evidence"
legacy: "step-7"
title: "Pick up eligible shared Evidence"
chapter: "Organize"
figure: "The maintenance worker"
scene: "discover"
fallback: "The worker reads eligible shared Evidence from the current generation."
caption: "The worker can organize the current shared generation. Private Evidence stays within its own space."
question: "What happens while maintenance is paused?"
details: ["A pause stops new maintenance work while preserving reads of current assignments. Disabling ontology removes topic reads. Ordinary authorized Evidence retrieval remains available.","The worker starts from pending sources and looks for related peers using a matching meaning key or shared search terms, within batch limits. Discovery can span several turns. A source fingerprint identifies the assessed version and state, allowing a valid assessment to be reused while its inputs still match."]
refs: ["internal/ontology/postgres/maintenance_discovery.go:18","internal/ontology/postgres/maintenance_batch.go:17","internal/ontology/postgres/maintenance_batch.go:255","internal/ontology/MANAGEMENT.md:35"]
guide: "/Topics-And-Organization/"
---

The ontology is the team’s catalog of topics, memberships, and equivalent passages. A background worker builds it from eligible Evidence when maintenance is enabled and unpaused.

The worker looks within the current shared space and generation. It can compare E1 and E3 as related passages and process E2 in another batch. A private session excerpt stays outside the shared catalog.
