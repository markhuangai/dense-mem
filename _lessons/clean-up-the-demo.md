---
slug: "clean-up-the-demo"
area: "workers"
order: 6
title: "Clean up the hosted demo"
summary: "Watch a temporary workspace expire and get cleaned up."
references: [["Hosted demo", "/Hosted-Demo/"]]
covers: ["worker:demo-cleanup", "profile:demo"]
frames:
  - id: start
    title: "Open a temporary workspace"
    behavior: "Maya starts a temporary demo workspace and sees an expiry time."
    mechanics: "The demo provisioner creates an isolated team and credential with a recorded expiry."
    story:
      - ["demo", "Hosted demo", "Start temporary workspace", ""]
      - ["team", "Workspace", "Expires at 15:00 UTC", "result"]
    flow:
      - ["demo", "Demo provisioner", "Create disposable team", ""]
      - ["team", "Team metadata", "Store demo expiry", "result"]
    fields: ["demo-expiry"]
  - id: expire
    title: "Clean up the expired team"
    behavior: "The workspace is disposable. After its expiry, the demo worker clears it."
    mechanics: "The demo cleanup pass selects expired demo teams in bounded batches and deletes those teams through the team service."
    story:
      - ["team", "Demo workspace", "Expired", "quiet"]
      - ["cleanup", "Cleanup", "Temporary team removed", "result"]
    flow:
      - ["team", "Demo expiry", "Reached deadline", ""]
      - ["cleanup", "Demo worker", "Delete expired demo team", "result"]
    fields: ["demo-expiry", "control-team"]
sources: [["Demo-only registration", "cmd/demo-server/main.go#L56"], ["Demo cleanup loop", "cmd/internal/demo/service/cleanup.go#L64"]]
---
