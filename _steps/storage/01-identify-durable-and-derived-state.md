---
workflow: "storage"
order: 1
anchor: "identify-durable-and-derived-state"
title: "Identify durable and derived state"
chapter: "Plan"
figure: "Maintain PostgreSQL and search indexes"
caption: "Evidence and provenance have a different recovery boundary from search projections."
guide: "/Data-Model-And-Storage/"
question: "Inspect this step"
details: ["The cluster-safe schema guide covers privileged and resumable steps. Use an authorized backup and the runbook for any irreversible migration boundary."]
---

Use the storage inventory to determine what must survive a migration and which state can be rebuilt. Verify the PostgreSQL prerequisites and relevant lock behavior.
