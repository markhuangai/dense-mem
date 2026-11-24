---
slug: "a-lost-reply"
area: "backend"
order: 4
title: "What if the reply is lost?"
summary: "Follow a save that did finish when its connection closed before the answer arrived."
references: [["Memory write pipeline", "/Write-Pipeline/"], ["MCP clients", "/MCP-Clients/"]]
covers: ["backend:replay", "backend:progress", "backend:errors"]
frames:
  - id: lost
    title: "The connection drops"
    behavior: "Maya sees no final confirmation even though the save committed."
    mechanics: "A progress message describes a stage. The application may have committed and retained its terminal result even if delivery fails."
    story:
      - ["note", "Maya asks to save", "$note", ""]
      - ["receipt", "Connection", "Closed before confirmation", "quiet"]
    flow:
      - ["note", "Dense-Mem", "Completed write", ""]
      - ["receipt", "Final receipt", "Saved by request key", "result"]
      - ["connection", "Connection", "Response missed", "quiet"]
    fields: ["operation", "receipt", "correlation"]
  - id: retry
    title: "Send the same request again"
    behavior: "The assistant checks by resending the same save and gets its original outcome."
    mechanics: "Remember looks up the authenticated operation and matching request hash. A matching retry returns the prior receipt."
    story:
      - ["note", "Assistant retries", "Same note and key", ""]
      - ["receipt", "Recovered outcome", "Saved", "result"]
    flow:
      - ["note", "Unchanged request", "Retained key and exact request", ""]
      - ["connection", "Replay check", "Same identity and request hash", "gate"]
      - ["receipt", "Terminal outcome", "Original result", "result"]
    fields: ["replay-state", "receipt"]
tools: ["remember"]
---
