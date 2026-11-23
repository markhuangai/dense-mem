---
workflow: "evidence"
order: 1
anchor: "send-the-note"
legacy: "step-1"
title: "Send the original note"
chapter: "Remember"
figure: "The submitted item"
scene: "submit"
fallback: "One source item moves into Remember."
caption: "One item contains two independent statements and the line break between them."
question: "How would a private chat enter instead?"
details: ["The request below uses a plain source label. Dense-Mem also accepts a source key and revision token for a versioned document. The example uses short labels E1, E2, and E3 for illustrative Evidence UUIDs.","For a chat, ingest_session stages the incoming user events in the caller’s private space. Extraction selects exact excerpts from the current events and preserves their original character offsets. Earlier events can supply context. An empty selection completes with no_memory.","Selected session excerpts go through assessment and required embeddings before private publication. The owner can recall them. The shared ontology worker reads only eligible team-shared Evidence."]
refs: ["internal/tools/registry/contract_examples.go:71","internal/remember/service/service.go:163","internal/session/service/service.go:55","internal/session/service/service.go:162"]
guide: "/Using-Dense-Mem/"
sample: "remember"
sample_label: "remember arguments"
---

An engineer asks an assistant to remember an operations handoff for the Atlas team. The assistant sends both lines together in one remember call.

It supplies the exact text, a source label, the team_shared destination, and a fresh operation key. A later reader can ask about either line.
