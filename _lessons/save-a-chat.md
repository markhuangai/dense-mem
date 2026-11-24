---
slug: "save-a-chat"
area: "assistants"
order: 2
title: "Save something from a chat"
summary: "See what happens when your assistant submits a conversation for private memory."
references: [["Private session intake", "/Write-Pipeline/#private-session-intake"], ["Framework integrations", "/Framework-Integrations/"]]
covers: ["ingest_session", "private-session"]
frames:
  - id: submit
    title: "Send the user’s message"
    behavior: "The assistant submits what Maya wrote in this chat."
    mechanics: "A supported host submits current user events. Dense-Mem stages their exact text in Maya’s private space."
    story:
      - ["event", "Maya · Chat message", "$note", ""]
      - ["staged", "Assistant", "Submitting to private memory", "gate"]
    flow:
      - ["event", "User event", "$note", ""]
      - ["staged", "Private staging", "Original event retained", "result"]
    fields: ["session-event", "session-text"]
  - id: extract
    title: "Pick an exact passage"
    behavior: "A sentence from the chat can become something Maya can find later."
    mechanics: "The extraction model proposes a range. Dense-Mem checks the original event and slices the original wording."
    story:
      - ["event", "Maya’s message", "$note", ""]
      - ["quote", "Saved privately", "$room", "result"]
    flow:
      - ["event", "Current event", "$note", ""]
      - ["range", "Checked range", "Exact words from the user", "gate"]
      - ["quote", "Eligible excerpt", "$room", "result"]
    fields: ["session-text", "session-event"]
  - id: empty
    title: "Finish with nothing saved"
    behavior: "A chat can finish without anything new to remember."
    mechanics: "An empty extraction produces an explicit no-memory outcome."
    story:
      - ["event", "Maya", "Thanks!", ""]
      - ["staged", "Private memory", "Nothing new saved", "quiet"]
    flow:
      - ["event", "Current user event", "Thanks!", ""]
      - ["staged", "Completed result", "No memory selected", "result"]
    fields: ["session-empty"]
tools: ["ingest_session"]
---
