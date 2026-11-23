---
workflow: "packs"
order: 2
anchor: "collect-provenance-and-calculate-the-hash"
title: "Collect provenance and calculate the hash"
chapter: "Build"
figure: "Export a memory pack"
caption: "The export binds the canonical JSON to its content hash."
guide: "/Technical-Reference/#export_memory_pack"
question: "Inspect this step"
details: ["Verify the returned content SHA-256 and counts. Use explicit omission information when judging how complete the material is."]
---

The server authorizes and assembles the bounded artifact, including identities, provenance, lifecycle, and any documented omissions.
