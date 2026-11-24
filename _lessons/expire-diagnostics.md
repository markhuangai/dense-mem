---
slug: "expire-diagnostics"
area: "workers"
order: 4
title: "Clear expired diagnostics"
summary: "Follow an operator diagnostic to the scheduled cleanup after its retention period."
references: [["Telemetry", "/Telemetry/"], ["Remember failure triage", "/Operations-Runbook/#synchronous-remember-failure-triage"]]
covers: ["worker:remember-diagnostics", "ops:diagnostic-retention"]
frames:
  - id: capture
    title: "Keep an allowed diagnostic"
    behavior: "The operator can inspect an allowed diagnostic from the failed save."
    mechanics: "Private Remember diagnostics have bounded retention. A diagnostic record carries a capture time and an expiry deadline."
    story:
      - ["diag", "Remember history", "Protected diagnostic", ""]
      - ["clock", "Retention", "Available until expiry", "gate"]
    flow:
      - ["diag", "Allowed capture", "Bounded operator visibility", ""]
      - ["clock", "Diagnostic record", "Store expiry with capture", "result"]
    fields: ["diagnostic-expiration", "correlation"]
  - id: purge
    title: "Clean up after expiry"
    behavior: "Later, the operator stops seeing the expired diagnostic."
    mechanics: "The scheduled purger drains eligible expired diagnostics and respects applicable holds."
    story:
      - ["clock", "Retention deadline", "Passed", ""]
      - ["diag", "Diagnostic detail", "Expired and removed", "quiet"]
    flow:
      - ["clock", "Scheduled pass", "Select expired records", ""]
      - ["diag", "Bounded purge", "Remove eligible diagnostics", "result"]
    fields: ["diagnostic-expiration"]
sources: [["Remember diagnostic purger", "internal/knowledge/postgres/remember_attempt_repository.go#L724"]]
---
