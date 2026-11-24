---
slug: "change-a-note"
area: "assistants"
order: 3
title: "When plans change"
summary: "Replace a saved note when the room moves, or withdraw it when practice is cancelled."
references: [["Conflicts and corrections", "/Conflicts-And-Corrections/"], ["Evidence lifecycle", "/Evidence-Model-And-Lifecycle/#lifecycle"]]
covers: ["retract_evidence", "correct", "withdraw"]
frames:
  - id: replace
    title: "Change the room"
    behavior: "Maya shares the new room using her own saved note."
    mechanics: "The assistant sends the exact replacement and the old ID. Ownership is checked and both changes commit together."
    story:
      - ["old", "Earlier plan", "$room", "quiet"]
      - ["new", "Maya’s update", "$replacement", "result"]
    flow:
      - ["old", "Owned passage", "$room", ""]
      - ["new", "Replacement", "$replacement", "gate"]
      - ["result", "Current memory", "Room 7 is findable", "result"]
    fields: ["supersedes", "recall-id"]
  - id: history
    title: "Check the earlier note"
    behavior: "Sam finds the updated room. The old passage still has a history."
    mechanics: "Current Recall excludes the superseded passage. Trace retains the authorized transition and exact source."
    story:
      - ["new", "Current answer", "Room 7", "result"]
      - ["old", "Earlier source", "$room", "quiet"]
    flow:
      - ["new", "Current Recall", "$replacement", "result"]
      - ["old", "Trace history", "$room", "quiet"]
    fields: ["trace-id", "supersedes"]
  - id: withdraw
    title: "Withdraw a plan"
    behavior: "Maya cancels practice and withdraws the note she owns."
    mechanics: "Retraction removes the owned passage from current Recall and records the lifecycle decision."
    story:
      - ["new", "Saved practice note", "Maya withdraws her note", ""]
      - ["result", "Current search", "That note is withdrawn", "quiet"]
    flow:
      - ["new", "Owner’s request", "Withdraw the selected passage", "gate"]
      - ["result", "Current Recall", "Passage no longer eligible", "result"]
    fields: ["withdraw-id", "retract-reason", "retracted"]
tools: ["remember", "retract_evidence", "trace_memory"]
---
