---
slug: "rehearsal-topic"
area: "workers"
order: 1
title: "Searchable before it has a topic"
summary: "See the User Portal, Control Portal, and topic worker follow the same rehearsal note."
references: [["Portal responsibilities", "/Portals-And-API-Keys/#portal-responsibilities"], ["Topic enablement and freshness", "/Topics-And-Organization/#enablement-and-freshness"], ["Ontology maintenance controls", "/Operations-Runbook/#ontology-maintenance"]]
covers: ["worker:ontology-maintenance", "control:maintenance", "backend:ontology"]
frames:
  - id: before-save
    title: "Check maintenance first"
    behavior: "The operator can see whether the background organizer is running."
    mechanics: "The separate Control Portal reads the saved policy and pause state. Maintenance must be enabled, unpaused, and within its work window to admit a new batch."
    story:
      - ["control", "Control Portal · Ontology maintenance", "Enabled", "result"]
      - ["topic", "User Portal · Rehearsals", "Waiting for Maya’s note", "quiet"]
    flow:
      - ["control", "Control Portal", "Read maintenance status", ""]
      - ["worker", "Work admission", "Enabled and unpaused", "gate"]
    fields: ["maintenance-enabled", "maintenance-paused-false"]
  - id: search
    title: "Find the note now"
    behavior: "Maya saves her shared note. Sam finds it in Search memory before it has a topic."
    mechanics: "Remember commits the exact passage and required search data. Topic assignment is a separate publication."
    story:
      - ["room", "Search memory · Band workspace", "$room", "result"]
      - ["topic", "Rehearsals", "No assignment for this note yet", "quiet"]
    flow:
      - ["room", "Remember · committed passage", "$room", ""]
      - ["search", "User Portal · Search memory", "Found the room note", "result"]
      - ["topic", "New topic assignment", "Still pending", "quiet"]
    fields: ["search-current", "recall-id", "sample-space"]
  - id: organize
    title: "File it under Rehearsals"
    behavior: "Later, the organizer can file the note under Rehearsals."
    mechanics: "Before filing the shared note, the worker checks that the note and the team’s filing rules are still current."
    story:
      - ["topic", "User Portal · Rehearsals", "$topic", ""]
      - ["room", "Filed under Rehearsals", "$room", "result"]
    flow:
      - ["room", "Eligible shared Evidence", "$room", ""]
      - ["worker", "Topic worker", "Validate placement and current inputs", "gate"]
      - ["topic", "Published direct membership", "$topic", "result"]
    fields: ["topic-id", "topic-filter", "maintenance-paused-false"]
sources: [["Control Portal pause action", "web/src/control/OntologyMaintenancePanel.tsx#L71"], ["Pause command and persisted state", "internal/ontology/postgres/maintenance_operations.go#L65"], ["Next batch admission check", "internal/ontology/postgres/maintenance_batch.go#L124"], ["Status projection", "internal/ontology/postgres/maintenance_operations.go#L190"]]
variant:
  id: paused
  label: "Try with maintenance paused"
  frames:
    before-save:
      behavior: "The operator presses Pause before Maya saves."
      mechanics: "Pause updates the stored flag. Newly admitted batches wait. Work already underway can still finish."
      story:
        - ["control", "Control Portal · Ontology maintenance", "Paused", "result"]
        - ["topic", "User Portal · Rehearsals", "Waiting for Maya’s note", "quiet"]
      flow:
        - ["control", "Operator action", "Pause", ""]
        - ["database", "Saved maintenance state", "Paused", "gate"]
        - ["worker", "Next admission", "No new batch", "result"]
      flow_notes:
        worker: "Existing published topics can still be read while maintenance is paused. New assignments wait for a later batch."
      fields: ["maintenance-key", "maintenance-paused", "maintenance-status", "maintenance-active"]
    search:
      behavior: "Sam still finds the shared note in Search memory."
      mechanics: "The pause affects admission of topic work. Remember and ordinary Recall still see the committed passage."
      story:
        - ["room", "Search memory · Band workspace", "$room", "result"]
        - ["topic", "Rehearsals", "Waiting for topic maintenance", "quiet"]
      flow:
        - ["room", "Committed passage", "$room", ""]
        - ["search", "User Portal · Search memory", "Found the room note", "result"]
        - ["worker", "Topic worker", "Paused", "quiet"]
      fields: ["search-current", "recall-id", "maintenance-paused"]
    organize:
      behavior: "The new assignment waits. Sam can still ask where practice is."
      mechanics: "The next batch checks the saved pause flag before it can take work. Published topics that remain eligible can still be read."
      story:
        - ["room", "Search memory · Band workspace", "$room", "result"]
        - ["topic", "Rehearsals", "Assignment still waiting", "quiet"]
      flow:
        - ["database", "Stored pause flag", "Paused", "gate"]
        - ["worker", "Worker · next batch", "Admission waits", "quiet"]
        - ["room", "Ordinary Recall", "$room", "result"]
      fields: ["maintenance-paused", "maintenance-status", "maintenance-pending", "maintenance-active"]
---
