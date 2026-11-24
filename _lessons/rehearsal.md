---
slug: "rehearsal"
area: "assistants"
order: 1
title: "Where’s band practice?"
summary: "A saved message helps a bandmate answer a question and check the source."
references: [["Using Dense-Mem", "/Using-Dense-Mem/"], ["Trace contract", "/Technical-Reference/#trace_memory"]]
covers: ["remember", "recall_memory", "trace_memory"]
frames:
  - id: save
    title: "Save the note"
    behavior: "Maya asks her assistant to remember both lines for the band."
    mechanics: "The assistant sends one source item and chooses the band’s shared memory."
    story:
      - ["note", "Maya · Band chat", "$note", ""]
      - ["saved", "Assistant", "Saved for the band", "result"]
    flow:
      - ["note", "Original message", "$note", ""]
      - ["space", "Checked destination", "Shared with the band", "gate"]
      - ["saved", "Save completes", "Ready to find", "result"]
    fields: ["original-content", "destination-shared", "operation"]
  - id: split
    title: "Keep the two ideas"
    behavior: "The saved message contains two things a bandmate could ask about."
    mechanics: "Dense-Mem finds a boundary between the ideas and keeps both passages in Maya’s exact words."
    story:
      - ["note", "Maya’s saved message", "$note", ""]
      - ["saved", "Saved for the band", "Both lines are available", "result"]
    flow:
      - ["note", "Original message", "$note", ""]
      - ["room", "Passage about the room", "$room", "result", "fragment-room"]
      - ["gear", "Passage about what to bring", "$gear", "quiet", "fragment-gear"]
    fields: ["end-offset", "start-offset", "original-content"]
    diagram: "split"
  - id: answer
    title: "Ask where practice is"
    behavior: "Sam’s assistant finds Maya’s room message and answers “Room 4.”"
    mechanics: "Search returns the relevant sentence from memory Sam can read. The assistant uses it to write the reply."
    story:
      - ["question", "Sam asks", "$question", ""]
      - ["room", "From Maya’s saved note", "$room", ""]
      - ["reply", "Sam’s assistant", "$answer", "result"]
    flow:
      - ["question", "Question", "$question", "gate"]
      - ["room", "Matching passage", "$room", "result"]
      - ["gear", "About gear", "$gear", "quiet"]
    flow_notes:
      room: "“Practice” and “rehearse” describe the same activity. Dense-Mem can find related wording by meaning."
    fields: ["question", "selected-quote", "recall-id"]
    diagram: "compare"
  - id: show-source
    title: "Show the original"
    behavior: "Sam checks the answer against Maya’s original note."
    mechanics: "The assistant uses the matching passage’s ID to follow it back to the saved message."
    story:
      - ["reply", "Answer", "$answer", "result"]
      - ["room", "Maya · Original message", "$note", "highlight"]
    flow:
      - ["room", "Returned passage", "$room", ""]
      - ["note", "Original message · source span", "$note", "highlight"]
    fields: ["trace-id", "source-item", "end-offset"]
sources: [["Exact split and offsets", "internal/knowledge/contract/partition.go#L45"], ["Tool input schemas", "internal/tools/registry/contract_schemas.go#L18"]]
tools: ["remember", "recall_memory", "trace_memory"]
variant:
  id: private
  label: "Try saving privately"
  frames:
    save:
      behavior: "Maya chooses to save the note just for her."
      mechanics: "Dense-Mem saves the item in the private space of Maya’s authenticated identity."
      story:
        - ["note", "Maya’s note", "$note", ""]
        - ["saved", "Assistant", "Saved just for Maya", "result"]
      flow:
        - ["note", "Original message", "$note", ""]
        - ["space", "Checked destination", "Maya’s private memory", "gate"]
        - ["saved", "Save completes", "Ready for Maya to find", "result"]
      fields: ["destination-private", "original-content", "private-operation"]
    split:
      behavior: "Both ideas are saved in Maya’s private space."
      mechanics: "The same exact split stays within the private space selected for the entire note."
      story:
        - ["note", "Maya’s saved message", "$note", ""]
        - ["saved", "Private memory", "Both lines are available to Maya", "result"]
    answer:
      behavior: "Sam’s assistant has no matching passage from this note."
      mechanics: "Sam searches spaces he can read. The private note stays in Maya’s space."
      story:
        - ["question", "Sam asks", "$question", ""]
        - ["reply", "Sam’s assistant", "I couldn’t find the practice room.", "quiet"]
      flow:
        - ["room", "Maya’s private passage", "$room", "quiet"]
        - ["question", "Sam’s search", "Only Sam’s permitted spaces", "gate"]
        - ["reply", "From this note", "No result", "result"]
      fields: ["question", "destination-private"]
    show-source:
      behavior: "Maya can still find the exact note from her own account."
      mechanics: "Maya can retrieve and trace the private passage through an authorized request in her own space."
      story:
        - ["question", "Maya asks", "$question", ""]
        - ["room", "Maya · Original message", "$note", "highlight"]
      flow:
        - ["question", "Maya’s authorized search", "$question", "gate"]
        - ["room", "Original message", "$note", "highlight"]
      fields: ["destination-private", "end-offset"]
related_lesson: {"label": "See when a note gets a topic", "url": "/learn/rehearsal-topic/"}
---
