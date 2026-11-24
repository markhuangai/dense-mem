---
slug: "portal-connect"
area: "user-portal"
order: 2
title: "Connect your assistant"
summary: "Sign in, select a workspace, and use the address shown in Connect."
references: [["Portals and API keys", "/Portals-And-API-Keys/"], ["MCP clients", "/MCP-Clients/"], ["User Portal SSO", "/User-Portal-SSO/"]]
covers: ["ui:sign-in", "ui:workspace-picker", "ui:connect", "mcp:discovery", "integrations"]
frames:
  - id: sign-in
    title: "Sign in and choose a team"
    behavior: "Maya signs in and chooses her band’s workspace."
    mechanics: "An API key or configured SSO authenticates the browser. SSO shows teams the user is entitled to select."
    story:
      - ["identity", "Maya", "Sign in", ""]
      - ["team", "Workspace picker", "Band", "result"]
    flow:
      - ["identity", "Browser sign-in", "Resolve current identity", "gate"]
      - ["team", "Session response", "Selected team and grants", "result"]
    fields: ["workspace", "role"]
  - id: connect
    title: "Open Connect"
    behavior: "Connect shows which address Maya should put in her assistant."
    mechanics: "The portal displays the current team and MCP endpoint. The assistant uses its own supported bearer authentication."
    story:
      - ["team", "User Portal · Connect", "Band workspace", ""]
      - ["client", "Assistant settings", "MCP endpoint", "result"]
    flow:
      - ["team", "Session response", "Team and public MCP base", ""]
      - ["client", "Assistant", "Authenticated tools/list", "result"]
    fields: ["workspace", "mcp-base"]
sources: [["Connect dialog", "web/src/user/App.tsx#L403"]]
---
