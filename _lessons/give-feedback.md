---
slug: "give-feedback"
area: "assistants"
order: 5
title: "Did the answer help?"
summary: "Attach feedback to the result your assistant actually used."
references: [["Recall feedback", "/Recall-Feedback/"]]
covers: ["submit_recall_session_feedback"]
frames:
  - id: observe
    title: "See which result helped"
    behavior: "The assistant used Maya’s rehearsal passage to answer Sam."
    mechanics: "Recall returns an event ID. The host retains it with the result that informed the answer."
    story:
      - ["room", "Saved words", "$room", ""]
      - ["answer", "Answer", "$answer", "result"]
    flow:
      - ["room", "Recall result", "$room", ""]
      - ["event", "Receipt", "Retain recall_id", "gate"]
    fields: ["feedback-id", "selected-quote"]
  - id: record
    title: "Send the observation"
    behavior: "The assistant reports that it used the saved words."
    mechanics: "The host sends an observation linked to the Recall event. The writer records it within its feedback boundary."
    story:
      - ["answer", "What happened", "Answer used the saved passage", ""]
      - ["event", "Feedback", "Used · supported", "result"]
    flow:
      - ["event", "Matched event", "Known Recall event", ""]
      - ["feedback", "Stored observation", "Used = true", "result"]
    fields: ["feedback-id", "feedback-used", "feedback-count"]
tools: ["submit_recall_session_feedback"]
---
