---
slug: "control-identity"
area: "control-portal"
order: 4
title: "Connect sign-in and access"
summary: "Map a sign-in provider to team access and inspect security settings."
references: [["User Portal SSO", "/User-Portal-SSO/"], ["Request lifecycle and security", "/Request-Lifecycle-And-Security/"], ["Configuration", "/Configuration/"]]
covers: ["control:sso", "control:security", "directory-provisioning", "control-sso:providers", "control-sso:mappings", "control-sso:directory"]
frames:
  - id: provider
    title: "Configure team sign-in"
    behavior: "An operator adds the band’s sign-in provider and maps who can join the team."
    mechanics: "Provider settings and role mappings resolve each user’s entitled memberships. Directory-managed assignments follow their own provisioning rules."
    story:
      - ["provider", "Control Portal · Identity & access", "Band sign-in", ""]
      - ["team", "Entitlements", "Band member or manager", "result"]
    flow:
      - ["provider", "Configured identity provider", "Authenticate the user", "gate"]
      - ["team", "Role mapping", "Resolve authorized team access", "result"]
    fields: ["provider", "role", "workspace"]
  - id: security
    title: "Review an IP ban"
    behavior: "An operator opens IP bans to check a blocked address."
    mechanics: "Security applies recorded bans at the request boundary. A protected diagnostic can be inspected under the control authorization."
    story:
      - ["ip", "Control Portal · IP bans", "192.0.2.42", ""]
      - ["scope", "Security rule", "Requests from this address blocked", "result"]
    flow:
      - ["ip", "Ban record", "Recorded network address", ""]
      - ["scope", "Security middleware", "Enforce current policy", "gate"]
    fields: ["ban-address", "correlation"]
sources: [["Control Portal administration", "web/src/App.tsx#L298"]]
---
