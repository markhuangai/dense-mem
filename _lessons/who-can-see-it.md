---
slug: "who-can-see-it"
area: "backend"
order: 3
title: "Who can see your note?"
summary: "Follow the same question through two different memory spaces."
references: [["Request lifecycle and security", "/Request-Lifecycle-And-Security/"], ["Architecture", "/Architecture/"], ["Portals and API keys", "/Portals-And-API-Keys/"]]
covers: ["backend:access", "backend:rls", "backend:space-authorization", "backend:coordination"]
frames:
  - id: identify
    title: "Resolve who is asking"
    behavior: "Sam signs in to the band’s workspace."
    mechanics: "The server derives the identity, team and grants from authentication before it prepares the memory operation."
    story:
      - ["person", "Sam", "Band member", ""]
      - ["space", "Workspace", "Band", "result"]
    flow:
      - ["person", "Verified identity", "Selected team and role", "gate"]
      - ["space", "Application request", "Fixed authorization context", "result"]
    fields: ["authenticated-team", "role"]
  - id: scope
    title: "Search the allowed spaces"
    behavior: "Sam can find the band’s shared note and any eligible private note of his own."
    mechanics: "The storage query scopes candidates by authorized team and spaces. Hydration rechecks access before returning the passage."
    story:
      - ["space", "Band’s shared memory", "$room", "result"]
      - ["private", "Maya’s private memory", "Outside Sam’s search", "quiet"]
    flow:
      - ["space", "Authenticated spaces", "Apply team and space filtering", "gate"]
      - ["private", "Foreign private space", "Excluded from candidate selection", "quiet"]
      - ["room", "Authorized result", "$room", "result"]
    fields: ["authenticated-team", "sample-space", "destination-private"]
    diagram: "compare"
sources: [["User Portal authentication and Recall route", "internal/http/user_portal_routes.go#L55"]]
related_lesson: {"label": "Try shared and private versions", "url": "/learn/rehearsal/"}
---
