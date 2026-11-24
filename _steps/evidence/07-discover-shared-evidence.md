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
details: ["A pause stops background admissions while enabled foreground assignment and Topic reads continue.","The scheduler discovers eligible older Evidence and indexed matches affected by Topic changes. It checks source and Topic revisions before starting provider work."]
source_revision: 6767dd33f5d3e2546b4afac39122db7bf01549f9
refs: ["internal/topics/postgres/maintenance_discovery.go:18","internal/topics/maintenance.go:192","internal/topics/MANAGEMENT.md:27"]
guide: "/Topics-And-Organization/"
---

A periodic scheduler revisits eligible Evidence and places Topics under broader Topics. It processes bounded pages and records its progress.

The worker looks within the current shared generation. E1, E2, and E3 can be reassessed as their inputs change. Private session excerpts stay in the owner’s private space.
