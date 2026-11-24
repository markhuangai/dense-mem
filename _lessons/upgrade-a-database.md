---
slug: "upgrade-a-database"
area: "operations"
order: 2
title: "Upgrade a database safely"
summary: "Follow a startup upgrade while the previous notes keep their source history."
references: [["Cluster-safe PostgreSQL schema", "/PostgreSQL-Cluster-Safe-Schema/"], ["Upgrade and readiness", "/Operations-Runbook/#startup-and-readiness"], ["Evidence-only cutover", "/Operations-Runbook/#evidence-only-cutover"]]
covers: ["ops:migrations", "ops:cutover", "ops:storage", "ops:shutdown"]
frames:
  - id: lock
    title: "Coordinate the upgrade"
    behavior: "Two server instances start against the same database. One waits while the other applies the upgrade."
    mechanics: "The migration runner serializes startup schema work at the writable primary. Traffic remains gated until required checks pass."
    story:
      - ["one", "First server", "Applying schema upgrade", ""]
      - ["two", "Second server", "Waiting for the database lock", "quiet"]
    flow:
      - ["one", "Startup runner", "Acquire migration lock", "gate"]
      - ["db", "Shared database", "Apply the next version", "result"]
      - ["two", "Waiting starter", "Continue after compatible state", ""]
    fields: ["migration-id", "ready-status"]
  - id: preserve
    title: "Keep the original sources"
    behavior: "The rehearsal note and its source are still available after the upgrade."
    mechanics: "The Evidence-only cutover retains original Evidence and clears its derived organization under the explicit migration inventory. Later maintenance can rebuild topics."
    story:
      - ["room", "Original Evidence", "$room", "result"]
      - ["topic", "Derived topics", "Rebuild after startup", "quiet"]
    flow:
      - ["room", "Protected canonical data", "Retain Evidence and provenance", ""]
      - ["topic", "Derived organization", "Reset as part of the pending version", "gate"]
      - ["one", "Worker", "Rebuild from eligible shared notes", "result"]
    fields: ["migration-id", "source-item"]
sources: [["Cutover migration", "migrations/postgres/v2_8/20261110010000_evidence_only_memory.sql"], ["Runtime lifecycle", "cmd/internal/serverapp/server.go#L465"]]
---
