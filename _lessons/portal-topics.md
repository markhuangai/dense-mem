---
slug: "portal-topics"
area: "user-portal"
order: 4
title: "Organize a team’s notes"
summary: "Preview a topic change in the User Portal and see what a bandmate can find."
references: [["Topics and organization", "/Topics-And-Organization/"]]
covers: ["ui:ontology", "ui:ontology-preview", "ui:ontology-publish", "topic-follow-up"]
frames:
  - id: draft
    title: "Draft an assignment"
    behavior: "A band manager wants Maya’s note filed under Rehearsals."
    mechanics: "The manager edits a draft against the current shared passage and catalog revision."
    story:
      - ["room", "Shared note", "$room", ""]
      - ["topic", "Draft topic", "$topic", "gate"]
    flow:
      - ["room", "Current shared Evidence", "$room", ""]
      - ["topic", "Manager draft", "Proposed direct membership", "gate"]
    fields: ["draft-revision", "role"]
  - id: preview
    title: "Preview and publish"
    behavior: "The manager checks the change, then publishes it."
    mechanics: "Preview runs publication checks. Publish rechecks the revisions and adds the eligible assignment and audit record together."
    story:
      - ["topic", "Preview", "One rehearsal passage", ""]
      - ["membership", "Published", "$topic", "result"]
    flow:
      - ["topic", "Draft revision", "Check source and catalog versions", "gate"]
      - ["membership", "Published catalog", "Rehearsals has a direct member", "result"]
    fields: ["draft-revision", "topic-id"]
  - id: follow
    title: "Follow the topic"
    behavior: "Sam opens Rehearsals to see matching notes filed directly under it."
    mechanics: "Recall fetches eligible direct members of the selected topic. It selects direct members only."
    story:
      - ["topic", "Related topic", "$topic", ""]
      - ["room", "In Rehearsals", "$room", "result"]
    flow:
      - ["topic", "Selected topic ID", "$topic", ""]
      - ["room", "Authorized direct member", "$room", "result"]
    fields: ["topic-filter", "topic-id"]
sources: [["User Portal topic editor", "web/src/user/OntologyPanel.tsx#L179"], ["Topic management draft contract", "internal/ontology/contract/management.go#L38"]]
---
