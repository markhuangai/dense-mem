---
slug: "portal-topics"
area: "user-portal"
order: 4
title: "Organize a team’s notes"
summary: "Preview a Topic definition in the User Portal and see what a bandmate can find."
references: [["Topics and organization", "/Topics-And-Organization/"]]
covers: ["ui:topics", "ui:topics-preview", "ui:topics-publish", "topic-follow-up"]
frames:
  - id: draft
    title: "Draft a Topic edit"
    behavior: "The manager adds a description to Rehearsals so the band can recognize it."
    mechanics: "The manager saves a personal draft against the current Topic and catalog revisions."
    story:
      - ["topic", "Current Topic", "$topic", ""]
      - ["topic", "Draft topic", "$topic", "gate"]
    flow:
      - ["topic", "Current definition", "$topic", ""]
      - ["topic", "Manager draft", "Proposed description", "gate"]
    fields: ["draft-revision", "role"]
  - id: preview
    title: "Preview and publish"
    behavior: "The manager checks the change, then publishes it."
    mechanics: "Preview runs publication checks. Publish rechecks the revisions and records the catalog update and audit together. The scheduler revisits affected memberships."
    story:
      - ["topic", "Preview", "Rehearsals with a description", ""]
      - ["topic", "Published", "$topic", "result"]
    flow:
      - ["topic", "Draft revision", "Check Topic and catalog versions", "gate"]
      - ["topic", "Published catalog", "Updated Topic definition", "result"]
    fields: ["draft-revision", "topic-id"]
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
source_revision: 174afa13fdadf1bf9d4366669066e1ee4912c47b
sources: [["User Portal Topic editor", "web/src/user/TopicsPanel.tsx#L191"], ["Topic management draft contract", "internal/topics/contract/management.go#L38"]]
---
