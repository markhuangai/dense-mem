---
slug: "portal-credentials"
area: "user-portal"
order: 3
title: "Manage access for your team"
summary: "See which actions belong to a member and which belong to a team manager."
references: [["Portals and API keys", "/Portals-And-API-Keys/"], ["Request lifecycle and security", "/Request-Lifecycle-And-Security/"]]
covers: ["ui:team", "ui:my-credentials", "ui:credential-rotation", "ui:team-settings", "ui:team-audit"]
frames:
  - id: member
    title: "Use My credentials"
    behavior: "An SSO member can see credentials they own in the selected team."
    mechanics: "The browser lists only the credentials allowed for the selected identity and team."
    story:
      - ["identity", "Maya · SSO member", "My credentials", ""]
      - ["key", "Owned credential", "Band assistant", "result"]
    flow:
      - ["identity", "Selected membership", "Check ownership", "gate"]
      - ["key", "Credential record", "Stable ID and effective access", "result"]
    fields: ["credential", "workspace"]
  - id: manager
    title: "Open team management"
    behavior: "A manager can inspect team settings and manage credentials where their access allows it."
    mechanics: "Team management actions check the role and grant. The available actions depend on the authenticated actor."
    story:
      - ["identity", "Band manager", "Team", ""]
      - ["team", "Team controls", "Settings and credentials", "result"]
    flow:
      - ["identity", "Membership role", "Manager", "gate"]
      - ["team", "Team routes", "Check current grants", "result"]
    fields: ["role", "credential"]
  - id: rotate
    title: "Replace an old key"
    behavior: "Maya updates her assistant to use the replacement key."
    mechanics: "Rotation replaces the secret. The resource keeps the stable credential ID and its private memory."
    story:
      - ["key", "Selected credential", "Band assistant", ""]
      - ["secret", "Replacement", "Update the assistant’s key", "result"]
    flow:
      - ["key", "Stable credential", "Same owner and private space", ""]
      - ["secret", "Rotation response", "New secret shown once", "result"]
    fields: ["credential"]
sources: [["User Portal role checks", "web/src/user/App.tsx#L264"], ["Team and credential routes", "internal/http/user_portal_routes.go#L68"]]
---
