---
workflow: "evidence"
order: 17
anchor: "answer-with-evidence"
legacy: "step-17"
title: "Answer with the rule and its exception"
chapter: "Follow up"
figure: "The host’s answer"
scene: "answer"
fallback: "The host combines the rule and its exception in the answer."
caption: "The assistant can cite E1 for duration and E3 for the exception. E2 answers a separate question."
question: "What if the policy changes later?"
details: ["An authorized owner can submit a new retention policy with the old Evidence ID in `supersedes_evidence_ids` and a fresh operation key. The old Evidence drops out of current Recall. Trace retains the link to its replacement. Stale assignments lose current eligibility.","After using a result, the host can submit voluntary feedback against its recall_id. For a portable record, export_memory_pack accepts authorized Evidence IDs and returns provenance, a content hash, and explicit omissions."]
refs: ["internal/tools/registry/contract_examples.go:71","internal/tools/registry/contract_examples.go:94","internal/knowledge/postgres/evidence_lifecycle_repository.go:227","adr/0017-evidence-only-memory-without-factual-relationships.md:18"]
guide: "/Using-Dense-Mem/"
---

The assistant can now answer the teammate in one sentence. E1 establishes the routine retention period. E3 adds the condition that changes when expiry runs.

The wording of the answer belongs to the host. Its two supporting IDs lead to the exact stored passages. The answer itself becomes memory only if someone submits it through an intake tool.
