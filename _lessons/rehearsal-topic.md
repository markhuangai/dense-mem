---
slug: "rehearsal-topic"
area: "workers"
order: 1
title: "Searchable before it has a topic"
summary: "Follow a rehearsal note saved while Topic organization was disabled."
references: [["Portal responsibilities", "/Portals-And-API-Keys/#portal-responsibilities"], ["Topic enablement and freshness", "/Topics-And-Organization/#enablement-and-freshness"], ["Topic maintenance controls", "/Operations-Runbook/#topic-maintenance"]]
covers: ["worker:topic-maintenance", "control:maintenance", "backend:topics"]
frames:
  - id: before-save
    title: "Check maintenance first"
    behavior: "Maya’s note was saved while organization was disabled. The operator enables it to catch up."
    mechanics: "The Control Portal reads the saved policy and pause state. Background work needs an enabled, unpaused window for new admissions."
    story:
      - ["control", "Control Portal · Topic maintenance", "Enabled", "result"]
      - ["topic", "User Portal · Rehearsals", "Waiting for an older note", "quiet"]
    flow:
      - ["control", "Control Portal", "Read maintenance status", ""]
      - ["worker", "Work admission", "Enabled and unpaused", "gate"]
    fields: ["maintenance-enabled", "maintenance-paused-false"]
  - id: search
    title: "Find the note now"
    behavior: "Sam finds Maya’s shared note in Search memory while the worker catches up."
    mechanics: "Remember committed the exact passage and required search data when Topic organization was disabled. New eligible shared saves can commit direct memberships in the same transaction once organization is enabled."
    story:
      - ["room", "Search memory · Band workspace", "$room", "result"]
      - ["topic", "Rehearsals", "No assignment for this note yet", "quiet"]
    flow:
      - ["room", "Remember · committed passage", "$room", ""]
      - ["search", "User Portal · Search memory", "Found the room note", "result"]
      - ["topic", "Assignment for the older note", "Still pending", "quiet"]
    fields: ["search-current", "recall-id", "sample-space"]
  - id: organize
    title: "File it under Rehearsals"
    behavior: "Later, the organizer can file the note under Rehearsals."
    mechanics: "The worker checks the note and Topic revisions before publishing direct memberships. Community placement separately decides whether Rehearsals belongs under a broader Topic."
    story:
      - ["topic", "User Portal · Rehearsals", "$topic", ""]
      - ["room", "Filed under Rehearsals", "$room", "result"]
    flow:
      - ["room", "Eligible shared Evidence", "$room", ""]
      - ["worker", "Topic assignment", "Validate membership and current inputs", "gate"]
      - ["topic", "Published direct membership", "$topic", "result"]
    fields: ["topic-id", "topic-filter", "maintenance-paused-false"]
source_revision: 174afa13fdadf1bf9d4366669066e1ee4912c47b
sources: [["Control Portal pause action", "web/src/control/TopicMaintenancePanel.tsx#L161"], ["Pause and persisted state", "internal/topics/postgres/maintenance_operations.go#L65"], ["Atomic Remember assignment", "internal/knowledge/postgres/remember_commit.go#L234"], ["Community placement", "internal/topics/postgres/community.go#L102"]]
variant:
  id: paused
  label: "Try with maintenance paused"
  frames:
    before-save:
      behavior: "The operator pauses maintenance before the scheduler picks up Maya’s older note."
      mechanics: "Pause updates the stored flag. Newly admitted batches wait. Work already underway can still finish."
      story:
        - ["control", "Control Portal · Topic maintenance", "Paused", "result"]
        - ["topic", "User Portal · Rehearsals", "Waiting for an older note", "quiet"]
      flow:
        - ["control", "Operator action", "Pause", ""]
        - ["database", "Saved maintenance state", "Paused", "gate"]
        - ["worker", "Next admission", "No new batch", "result"]
      flow_notes:
        worker: "Existing published Topics remain readable. Eligible new shared saves still receive foreground assignments. The older note waits for a later batch."
      fields: ["maintenance-key", "maintenance-paused", "maintenance-status", "maintenance-active"]
    search:
      behavior: "Sam still finds the shared note in Search memory."
      mechanics: "The pause stops new background admissions. Ordinary Recall can still find the previously committed passage."
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
