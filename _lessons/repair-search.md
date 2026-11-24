---
slug: "repair-search"
area: "workers"
order: 2
title: "Repair an outdated search record"
summary: "See which piece is rebuilt when search data falls behind the saved source."
references: [["Search convergence", "/Operations-Runbook/#search-convergence"], ["Search data", "/Data-Model-And-Storage/"]]
covers: ["worker:search-reconciliation", "ops:search-recovery"]
frames:
  - id: drift
    title: "Find an outdated record"
    behavior: "The operator sees one search record that needs attention in Search readiness."
    mechanics: "The maintenance pass compares expected and current documents and selects bounded work for the active search contract."
    story:
      - ["room", "Saved source", "$room", ""]
      - ["index", "Search readiness", "One outdated record", "quiet"]
    flow:
      - ["room", "Current saved passage", "$room", ""]
      - ["index", "Search projection", "Stale version or contract", "gate"]
    fields: ["convergence", "search-source-version"]
  - id: repair
    title: "Refresh the search copy"
    behavior: "Dense-Mem prepares a new search copy of the same message."
    mechanics: "The worker prepares a fresh search copy and checks the saved note again before replacing the old copy."
    story:
      - ["room", "Original message", "$room", ""]
      - ["index", "Search readiness", "Up to date after repair", "result"]
    flow:
      - ["room", "Selected source version", "$room", ""]
      - ["version", "Recheck version and contract", "Accept only current work", "gate"]
      - ["index", "Derived search record", "Current", "result"]
    fields: ["search-source-version", "convergence"]
sources: [["Reconciliation interval and lifecycle", "cmd/internal/serverapp/search_background.go#L39"], ["Reconciliation orchestration", "internal/search/reconciliation.go"]]
variant:
  id: changed
  label: "Change the source during repair"
  frames:
    repair:
      behavior: "The source changed while the worker prepared the search copy."
      mechanics: "The write guard rejects the outdated repair. The newer source remains the authority for the next pass."
      story:
        - ["room", "New current source", "$replacement", ""]
        - ["index", "Old search repair", "Rejected as outdated", "quiet"]
      flow:
        - ["room", "Selected earlier version", "$room", ""]
        - ["version", "Current source advanced", "$replacement", "gate"]
        - ["index", "Stale write", "Skipped", "result"]
      fields: ["search-source-version"]
---
