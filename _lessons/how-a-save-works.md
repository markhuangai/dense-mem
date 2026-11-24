---
slug: "how-a-save-works"
area: "backend"
order: 1
title: "When is a note saved?"
summary: "Follow the exact words from a save request through the checks and the final receipt."
references: [["Memory write pipeline", "/Write-Pipeline/"], ["Data model and storage", "/Data-Model-And-Storage/"], ["Atlas walkthrough", "/walkthroughs/atlas/"]]
covers: ["backend:remember", "backend:partition", "backend:assessment", "backend:atomic-commit", "backend:embeddings"]
frames:
  - id: receive
    title: "Receive the original"
    behavior: "Maya asks her assistant to keep both lines of her message."
    mechanics: "After admission, the server offers allowed cut positions to the partition model. The original stays the authority for what can be saved."
    story:
      - ["note", "Maya · Band chat", "$note", ""]
      - ["saved", "Assistant", "Saved for the band", "result"]
    flow:
      - ["note", "Original item", "$note", ""]
      - ["candidate", "Offered boundary", "Between the two ideas", "gate"]
    fields: ["original-content", "boundary", "operation"]
  - id: validate
    title: "Check the suggested split"
    behavior: "The two ideas can be found separately, in Maya’s exact words."
    mechanics: "The server validates every proposed boundary, checks the whole assessment, and prepares required search data before commit."
    story:
      - ["note", "Original item", "$note", ""]
      - ["room", "Room passage", "$room", "result"]
      - ["gear", "Gear passage", "$gear", ""]
    flow:
      - ["note", "Original item", "$note", ""]
      - ["candidate", "Validated split", "Exact slices of the source", "gate"]
      - ["room", "Accepted passage", "$room", "result"]
    fields: ["boundary", "start-offset", "end-offset"]
  - id: commit
    title: "Publish both passages"
    behavior: "Sam can find the new room note once the save completes."
    mechanics: "PostgreSQL publishes the accepted passages, source links, search data, and terminal receipt together."
    story:
      - ["room", "Maya’s room note", "$room", ""]
      - ["result", "Assistant", "Saved and ready to find", "result"]
    flow:
      - ["room", "Evidence and provenance", "$room", ""]
      - ["search", "Required search data", "Ready for Recall", ""]
      - ["result", "One durable commit", "Completed receipt", "result"]
    fields: ["receipt", "search-current", "source-item"]
sources: [["Exact span validation", "internal/knowledge/contract/partition.go#L45"], ["Atomic write", "internal/knowledge/postgres/remember_commit.go#L44"]]
tools: ["remember"]
variant:
  id: invalid-split
  label: "Try an invalid split"
  frames:
    validate:
      behavior: "The suggested split is rejected."
      mechanics: "An unknown boundary invalidates the whole response. A bounded retry requests a complete replacement."
      story:
        - ["note", "Original item", "$note", ""]
        - ["candidate", "Check failed", "The proposed split is invalid", "gate"]
      flow:
        - ["note", "Allowed positions", "Server-issued IDs", ""]
        - ["candidate", "Model proposal", "Unknown ID", "gate"]
        - ["result", "Validation", "Regenerate complete response", "result"]
      fields: ["rejected-boundary", "boundary"]
    commit:
      behavior: "If the split still fails, the assistant reports a failed save."
      mechanics: "No accepted passage or search record commits for this request."
      story:
        - ["room", "Original item", "$note", ""]
        - ["result", "Assistant", "No new memory was saved", "quiet"]
      flow:
        - ["candidate", "Invalid replacement", "Retry limit reached", "gate"]
        - ["result", "New Records", "Zero accepted passages", "result"]
      fields: ["rejected-boundary", "original-content"]
---
