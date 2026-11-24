---
slug: "portal-search"
area: "user-portal"
order: 1
title: "Find a note in your workspace"
summary: "Search memory and inspect the source label and destination on a real result."
references: [["Portals and API keys", "/Portals-And-API-Keys/"], ["Recall ranking", "/Recall-Ranking/"]]
covers: ["ui:search", "ui:result-inspector"]
frames:
  - id: search
    title: "Search the band workspace"
    behavior: "Sam opens Search memory and enters “Where’s practice?”"
    mechanics: "The User Portal sends its own authenticated Recall request. Dense-Mem uses the allowed spaces of Sam’s current workspace."
    story:
      - ["query", "User Portal · Search memory", "$question", ""]
      - ["scope", "Current workspace", "Band", "gate"]
    flow:
      - ["query", "Search form", "$question", ""]
      - ["scope", "Team and permitted spaces", "Band workspace", "gate"]
    fields: ["portal-query", "workspace"]
  - id: inspect
    title: "Open a result"
    behavior: "Sam selects Maya’s rehearsal note in the results list."
    mechanics: "The result inspector displays the returned quote, source label, destination, rank, and evidence ID."
    story:
      - ["room", "Search result", "$room", "result"]
      - ["source", "Evidence details", "Band chat · Team shared", ""]
    flow:
      - ["query", "Authorized Recall", "$question", ""]
      - ["room", "Result context", "$room", "result"]
      - ["source", "Result inspector", "Source · destination · rank", ""]
    fields: ["selected-quote", "sample-space", "recall-id"]
sources: [["Search form and result inspector", "web/src/user/SearchPanel.tsx#L26"], ["Portal Recall route", "internal/http/user_portal_routes.go#L83"]]
related_lesson: {"label": "See when results get topics", "url": "/learn/rehearsal-topic/"}
---
