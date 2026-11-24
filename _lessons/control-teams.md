---
slug: "control-teams"
area: "control-portal"
order: 1
title: "Set up a team"
summary: "Follow an operator from a new team to a scoped credential and its workspace."
references: [["Portals and API keys", "/Portals-And-API-Keys/"], ["Request lifecycle and security", "/Request-Lifecycle-And-Security/"], ["Configuration", "/Configuration/"]]
covers: ["control:all-teams", "control:overview", "control:credentials", "control:settings"]
frames:
  - id: create
    title: "Create the team"
    behavior: "An operator makes a private workspace for the band in the Control Portal."
    mechanics: "The separate operator interface creates a team and records its identity. The team ID is used in its administrative URLs."
    story:
      - ["operator", "Control Portal · All teams", "Create Band", ""]
      - ["team", "New team", "Band", "result"]
    flow:
      - ["operator", "Operator authorization", "Control session", "gate"]
      - ["team", "Team record", "Stable server-issued ID", "result"]
    fields: ["control-team"]
  - id: grant
    title: "Choose credential access"
    behavior: "The operator creates the band assistant’s credential with the access it needs."
    mechanics: "Credential creation fixes role and grants. The server resolves the private space and shared access from the authenticated credential."
    story:
      - ["team", "Band workspace", "Credentials", ""]
      - ["key", "Band assistant", "Selected read and write grants", "result"]
    flow:
      - ["team", "Selected team", "Scope the creation route", ""]
      - ["key", "Credential record", "Identity and effective access", "result"]
    fields: ["credential", "private-read", "control-team"]
sources: [["Control Portal workspaces", "web/src/App.tsx#L283"], ["Control credential routes", "internal/http/control_portal.go#L177"]]
---
