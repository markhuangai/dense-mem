---
slug: "record-activity"
area: "workers"
order: 5
title: "Record activity in the background"
summary: "Follow the signals a request produces for operators and account holders."
references: [["Telemetry", "/Telemetry/"], ["Recall feedback", "/Recall-Feedback/"]]
covers: ["worker:credential-activity", "worker:usage-metrics", "worker:operation-log", "worker:recall-feedback", "worker:telemetry-pricing-refresh", "control:recall-feedback", "ops:monitoring"]
frames:
  - id: activity
    title: "Update a credential’s last use"
    behavior: "Maya can check when the band assistant’s credential was last used."
    mechanics: "A background writer coalesces activity observations by credential. The most recent authenticated use can appear after the request returns."
    story:
      - ["request", "Assistant", "Use the band credential", ""]
      - ["activity", "Credential details", "Last used updated", "result"]
    flow:
      - ["request", "Authenticated API-key use", "Record an observation", ""]
      - ["activity", "Activity writer", "Persist the latest timestamp", "result"]
    fields: ["last-used", "credential"]
  - id: logs
    title: "Collect operational signals"
    behavior: "The operator can see usage counts and find request entries in Logs."
    mechanics: "Operation logs and usage metrics have separate background writers and distinct outputs."
    story:
      - ["request", "Operation", "Save the rehearsal note", ""]
      - ["logs", "Control Portal", "Logs and usage", "result"]
    flow:
      - ["request", "Request observation", "Bounded record", ""]
      - ["logs", "Operation log writer", "Keep the request identity", "result"]
      - ["usage", "Usage metrics writer", "Aggregate usage signals", "gate"]
    fields: ["operation-log", "usage-requests"]
    diagram: "split"
  - id: feedback
    title: "Keep recall diagnostics"
    behavior: "An operator can inspect Recall context and feedback about whether a result helped."
    mechanics: "The Recall feedback event service persists bounded event context. Later submitted feedback can attach to that event."
    story:
      - ["room", "Previously recalled passage", "$room", ""]
      - ["feedback", "Control Portal", "Recall feedback", "result"]
    flow:
      - ["room", "Recall event", "Capture bounded context", ""]
      - ["feedback", "Background event writer", "Persist a record for review", "result"]
    fields: ["feedback-id", "feedback-used"]
  - id: pricing
    title: "Refresh optional pricing"
    behavior: "When pricing is configured, new provider calls can use the refreshed rates in cost estimates."
    mechanics: "The optional pricing refresh checks for an updated cached configuration. Missing rate or usage is represented separately from measured cost."
    story:
      - ["usage", "Provider reports usage", "240 input tokens", ""]
      - ["pricing", "Cost display", "Uses the current matching rate", "result"]
    flow:
      - ["pricing", "Saved pricing rules", "Per-million-token rate", ""]
      - ["cache", "Pricing refresh", "Read current snapshot", "gate"]
      - ["usage", "Telemetry estimate", "Combine rate with known usage", "result"]
    fields: ["pricing", "metric-tokens"]
sources: [["Worker registrations", "cmd/internal/serverapp/server.go#L465"], ["Credential activity writer", "internal/service/access/credential_activity.go#L64"], ["Optional pricing refresh", "internal/operations/telemetry_pricing.go#L26"]]
---
