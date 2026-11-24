---
slug: "recover-a-deployment"
area: "operations"
order: 3
title: "Recover a saved deployment"
summary: "Restore a backup into an isolated environment and check the data before serving it."
references: [["Backup", "/Operations-Runbook/#backup"], ["Restore and disaster recovery", "/Operations-Runbook/#restore-and-disaster-recovery"], ["Telemetry", "/Telemetry/"]]
covers: ["ops:backup", "ops:restore", "ops:health-triage"]
frames:
  - id: backup
    title: "Capture a backup"
    behavior: "The operator keeps a copy of the band’s memory database."
    mechanics: "Back up PostgreSQL with compatible recovery metadata. This preserves durable Evidence and operational state for the restore exercise."
    story:
      - ["db", "Band database", "Saved notes and sources", ""]
      - ["backup", "Operator", "Record the backup and recovery point", "result"]
    flow:
      - ["db", "PostgreSQL primary", "Create consistent backup", ""]
      - ["backup", "Protected backup", "Keep compatible metadata", "result"]
    fields: ["routine-backup"]
  - id: restore
    title: "Test it in isolation"
    behavior: "The operator opens the restored environment and checks the rehearsal note."
    mechanics: "Verify schema, current search, source lineage, and private isolation before an authorized rollout."
    story:
      - ["backup", "Backup", "Restore in isolation", ""]
      - ["room", "Verification", "$room", "result"]
    flow:
      - ["backup", "Restore target", "Validate settings and search", ""]
      - ["room", "Trace and access checks", "Confirm the source and allowed spaces", "gate"]
      - ["restore", "Deployment", "Switch traffic after checks", "result"]
    fields: ["routine-backup", "ready-status", "trace-id"]
---
