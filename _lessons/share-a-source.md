---
slug: "share-a-source"
area: "assistants"
order: 4
title: "Share a saved source"
summary: "Send a copy with its source and a way to check that the copy stayed intact."
references: [["Memory packs", "/Memory-Packs/"]]
covers: ["export_memory_pack"]
frames:
  - id: select
    title: "Choose what to share"
    behavior: "Maya picks the rehearsal passage she is allowed to export."
    mechanics: "The assistant passes the selected Evidence IDs. Dense-Mem checks each target’s visibility."
    story:
      - ["room", "Maya selects", "$room", "result"]
      - ["pack", "Export", "A copy with its source", ""]
    flow:
      - ["room", "Selected passage", "$room", ""]
      - ["pack", "Access check", "Only authorized records", "gate"]
    fields: ["export-ids"]
  - id: assemble
    title: "Keep the source attached"
    behavior: "The copy carries the words and where they came from."
    mechanics: "Dense-Mem packages Evidence and bounded provenance. Omitted material is reported with reasons."
    story:
      - ["room", "Exact passage", "$room", ""]
      - ["pack", "Memory pack", "Passage, source, and omissions", "result"]
    flow:
      - ["room", "Authorized Evidence", "$room", ""]
      - ["source", "Provenance", "$source", ""]
      - ["pack", "Pack output", "Canonical JSON", "result"]
    fields: ["export-ids", "source-item"]
  - id: verify
    title: "Check the copy"
    behavior: "The recipient can check whether the exported content changed."
    mechanics: "The pack includes a content digest calculated from the canonical form."
    story:
      - ["pack", "Received copy", "Rehearsal note", ""]
      - ["check", "Integrity check", "Compare the content fingerprint", "result"]
    flow:
      - ["pack", "Canonical pack", "Same content bytes", ""]
      - ["check", "SHA-256 digest", "Compare with the reported digest", "result"]
    fields: ["pack-hash"]
tools: ["export_memory_pack"]
---
