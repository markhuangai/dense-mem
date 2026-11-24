---
slug: "start-a-server"
area: "operations"
order: 1
title: "Bring a server online"
summary: "See how a deployment starts, checks its dependencies, and opens the two portals."
references: [["Quick Start", "/Quick-Start/"], ["Architecture", "/Architecture/"], ["Public HTTPS and Redis", "/Public-HTTPS-And-Redis/"], ["Configuration", "/Configuration/"]]
covers: ["ops:deploy", "ops:config", "ops:redis", "ops:https", "ops:readiness", "profile:production"]
frames:
  - id: configure
    title: "Connect the dependencies"
    behavior: "An operator sets up a new Dense-Mem server for the band."
    mechanics: "Startup loads environment settings, connects to writable PostgreSQL, runs compatible migrations, and prepares providers and search."
    story:
      - ["server", "Band server", "Starting", ""]
      - ["db", "Memory database", "Preparing durable storage", "gate"]
    flow:
      - ["server", "Configuration", "Select production settings", ""]
      - ["db", "PostgreSQL", "Writable primary and active schema", "gate"]
      - ["worker", "Application", "Compose listeners and workers", ""]
    fields: ["control-address", "mcp-base"]
  - id: route
    title: "Send traffic to the right place"
    behavior: "Users open their workspace. Operators open the private deployment console."
    mechanics: "The main listener serves the User Portal and MCP. The separate control listener is routed through the administrative boundary."
    story:
      - ["public", "Band members", "User Portal and assistant", ""]
      - ["private", "Operators", "Private Control Portal", "gate"]
    flow:
      - ["public", "Main listener", "User Portal and MCP", ""]
      - ["private", "Control listener", "Private ingress", "gate"]
    fields: ["control-address", "mcp-base"]
    diagram: "compare"
  - id: ready
    title: "Wait for readiness"
    behavior: "The deployment sends traffic once the instance reports it is ready."
    mechanics: "The readiness endpoint checks required dependencies. Multi-instance operation uses shared coordination such as Redis."
    story:
      - ["server", "Band server", "Ready", "result"]
      - ["public", "Users", "Can connect", ""]
    flow:
      - ["db", "Required checks", "Operational", ""]
      - ["server", "Readiness response", "Ready", "result"]
      - ["public", "Deployment", "Enable normal routing", ""]
    fields: ["ready-status"]
sources: [["Readiness response", "internal/http/router_public.go#L58"], ["Listener settings", "internal/config/config.go#L565"]]
---
