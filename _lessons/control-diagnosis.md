---
slug: "control-diagnosis"
area: "control-portal"
order: 2
title: "Investigate a failed save"
summary: "Use Remember history and Logs to locate the point where a save failed."
references: [["Remember failure triage", "/Operations-Runbook/#synchronous-remember-failure-triage"], ["Telemetry", "/Telemetry/"]]
covers: ["control:remember-attempts", "control:logs", "control:search"]
frames:
  - id: collect
    title: "Start with the reply"
    behavior: "Maya reports that saving failed and shares the request’s reference ID."
    mechanics: "Collect the public error and correlation ID. A required provider failure does not publish new memory from the failed request."
    story:
      - ["save", "Maya’s save", "Please remember my note", ""]
      - ["failure", "Assistant", "Save failed", "quiet"]
    flow:
      - ["save", "Remember operation", "Required processing", ""]
      - ["failure", "Bounded error", "Provider unavailable", "result"]
    fields: ["failure-code", "correlation"]
  - id: locate
    title: "Find the same request"
    behavior: "The operator finds the failed request in Remember history and checks its Logs."
    mechanics: "Follow the correlation ID into the operator’s record for that team and stage. Inspect available details under the private control boundary."
    story:
      - ["failure", "Remember history", "Open failed attempt", ""]
      - ["logs", "Logs", "Matching provider error", "result"]
    flow:
      - ["failure", "Terminal request identity", "Correlation ID", ""]
      - ["logs", "Operation log query", "Filter to the same request", "result"]
    fields: ["correlation", "failure-code"]
  - id: check-search
    title: "Check search readiness"
    behavior: "The operator also checks whether search is up to date."
    mechanics: "Search readiness distinguishes a foreground failure from later changes to derived search documents."
    story:
      - ["logs", "Control Portal", "Search readiness", ""]
      - ["search", "Convergence view", "Current and drifted documents", "result"]
    flow:
      - ["search", "Search projections", "Compare with active source and model contract", ""]
      - ["logs", "Control status", "Show readiness and last outcome", "result"]
    fields: ["convergence"]
sources: [["Control diagnostics navigation", "web/src/App.tsx#L294"], ["Search convergence route", "internal/http/control_portal.go#L149"]]
related_lesson: {"label": "See how search repairs outdated records", "url": "/learn/repair-search/"}
---
