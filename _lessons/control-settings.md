---
slug: "control-settings"
area: "control-portal"
order: 5
title: "Tune the service settings"
summary: "See what changes when an operator saves a deployment setting."
references: [["Configuration", "/Configuration/"], ["Telemetry", "/Telemetry/"], ["Optional decisions", "/Vector-Search-And-Ranking/#optional-typed-reranking"]]
covers: ["control:config", "control-config:general", "control-config:sso", "control-config:operation-logs", "control-config:recall-feedback", "control-config:telemetry-pricing", "control-config:decisions"]
frames:
  - id: general
    title: "Choose how activity is displayed"
    behavior: "An operator chooses the time zone for service activity."
    mechanics: "General settings and SSO runtime settings are saved under separate scoped settings contracts."
    story:
      - ["operator", "Control Portal · General", "Select time zone", ""]
      - ["display", "Activity display", "Use saved time zone", "result"]
    flow:
      - ["operator", "Settings request", "Validated value", ""]
      - ["display", "Runtime setting", "Current time zone or SSO origin", "result"]
    fields: ["timezone", "mcp-base"]
  - id: retention
    title: "Set what diagnostics to keep"
    behavior: "The operator sets how long to keep operation logs and configures Recall feedback."
    mechanics: "The log and feedback settings apply to their own processing and retention owners. Provider pricing is a separate settings tab."
    story:
      - ["logs", "Settings · Operation logs", "Keep for 30 days", ""]
      - ["usage", "Settings · Recall feedback", "Control collection and retention", "gate"]
    flow:
      - ["logs", "Feature settings", "Persist the retention policy", ""]
      - ["usage", "Diagnostic services", "Apply saved policy", "result"]
    fields: ["log-retention", "pricing"]
  - id: decisions
    title: "Review the optional decisions"
    behavior: "The operator checks which extra model decisions are enabled."
    mechanics: "The Decisions tab reports task switches and its configured provider. Each enabled task still follows its own bounded decision contract."
    story:
      - ["decision", "Settings · Decisions", "Retrieval relevance off", ""]
      - ["provider", "Assistant search", "Regular ranking remains available", "result"]
    flow:
      - ["decision", "Decision task policy", "Read the task switch", ""]
      - ["provider", "Recall service", "Use optional decision when configured", "gate"]
    fields: ["decision-task"]
sources: [["Control settings sections", "web/src/control/ConfigPanel.tsx#L112"]]
---
