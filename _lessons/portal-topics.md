---
slug: "portal-topics"
area: "user-portal"
order: 4
title: "Organize a team’s notes"
summary: "Save a Topic definition in the User Portal and see what a bandmate can find."
references: [["Topics and organization", "/Topics-And-Organization/"]]
covers: ["ui:topics", "ui:topics-save", "topic-follow-up"]
frames:
  - id: draft
    title: "Edit a Topic"
    behavior: "The manager adds a description to Rehearsals so the band can recognize it."
    mechanics: "The editor keeps each selected record version with the pending change."
    story:
      - ["topic", "Current Topic", "$topic", ""]
      - ["topic", "Edited Topic", "$topic", "gate"]
    flow:
      - ["topic", "Current definition", "$topic", ""]
      - ["topic", "Topic edit", "Proposed description", "gate"]
    fields: ["topic-edit-version", "role"]
  - id: preview
    title: "Save and verify"
    behavior: "The manager enters the reason and selects Save."
    mechanics: "Save checks record versions and commits the catalog change and audit together. The scheduler revisits affected memberships."
    story:
      - ["topic", "Save", "Rehearsals with a description", ""]
      - ["topic", "Saved", "$topic", "result"]
    flow:
      - ["topic", "Expected version", "Check each Topic version", "gate"]
      - ["topic", "Current catalog", "Updated Topic definition", "result"]
    fields: ["topic-edit-version", "topic-id"]
  - id: follow
    title: "Follow the topic"
    behavior: "After assignment becomes current, Sam opens Rehearsals and finds Maya’s note."
    mechanics: "Recall fetches eligible direct members of the selected Topic."
    story:
      - ["topic", "Related topic", "$topic", ""]
      - ["room", "In Rehearsals", "$room", "result"]
    flow:
      - ["topic", "Selected topic ID", "$topic", ""]
      - ["room", "Authorized direct member", "$room", "result"]
    fields: ["topic-filter", "topic-id"]
source_revision: 8b94038237b7291fe5b1eb97958fa7b4cf4c3175
sources: [["User Portal Topic editor", "web/src/user/TopicsPanel.tsx#L169"], ["Topic Save handler", "internal/http/user_portal_topic_management.go#L213"]]
---
