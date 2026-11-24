---
slug: "erase-private-memory"
area: "workers"
order: 3
title: "Erase a private memory space"
summary: "Follow an owner’s request from the initial acknowledgement to the verified outcome."
references: [["Owner-facing erasure", "/Portals-And-API-Keys/#private-memory-erasure"], ["Operator erasure controls", "/Operations-Runbook/#private-memory-erasure"], ["Configuration", "/Configuration/"]]
covers: ["worker:private-memory", "worker:private-retention", "portal-api:private-erasure", "ops:privacy", "control-api:private-holds"]
frames:
  - id: request
    title: "Ask to erase private memory"
    behavior: "Maya requests erasure of her own private memory and confirms the action."
    mechanics: "The service authorizes the exact target and saves a durable erasure operation for the worker."
    story:
      - ["space", "Maya", "Erase my private memory", ""]
      - ["operation", "Request status", "Queued", "gate"]
    flow:
      - ["space", "Authenticated target", "Exact private space", ""]
      - ["operation", "Durable operation", "Accepted for processing", "result"]
    fields: ["erase-confirm", "erase-id"]
  - id: verify
    title: "Wait for the worker"
    behavior: "Maya checks the erasure status to see when the job has finished."
    mechanics: "The worker seals the selected space against changes, checks for holds, then verifies what it erased."
    story:
      - ["operation", "Erasure status", "Checking the requested space", ""]
      - ["verified", "Owner’s status", "Completed", "result"]
    flow:
      - ["operation", "Authorized private generation", "Sealed for erasure", "gate"]
      - ["verified", "Verified purge", "Operation completed", "result"]
    fields: ["erase-id", "erase-status"]
  - id: retention
    title: "Run a retention policy"
    behavior: "An operator can configure how long eligible private memory is kept."
    mechanics: "The retention scheduler checks its settings and records a selection run. Eligible targets become erasure work under the same hold checks."
    story:
      - ["policy", "Retention setting", "30 days for this example", ""]
      - ["operation", "Eligible private memory", "Selected for erasure", "result"]
    flow:
      - ["policy", "Runtime retention policy", "Calculate age cutoff", ""]
      - ["operation", "Retention run", "Queue eligible operations", "result"]
    fields: ["retention-days", "erase-id"]
sources: [["Worker and retention scheduler", "internal/privacy/service/service.go#L269"], ["Owner’s operation status route", "internal/http/user_portal_routes.go#L70"]]
variant:
  id: legal-hold
  label: "Try with a legal hold"
  frames:
    verify:
      behavior: "The status shows that a legal hold is blocking erasure."
      mechanics: "The worker detects an applicable hold and leaves this operation blocked. Check the current status for the outcome."
      story:
        - ["operation", "Requested private space", "Legal hold applies", "gate"]
        - ["verified", "Owner’s status", "Blocked by a legal hold", "quiet"]
      flow:
        - ["operation", "Exact target", "Detect an active hold", "gate"]
        - ["verified", "Erasure status", "Purge blocked", "result"]
      fields: ["erase-id", "hold-status"]
---
