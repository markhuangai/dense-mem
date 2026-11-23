---
workflow: "evidence"
order: 16
anchor: "trace-source"
legacy: "step-16"
title: "Trace E1 back to the original line"
chapter: "Follow up"
figure: "The provenance chain"
scene: "trace"
fallback: "The original item and its character offsets establish provenance."
caption: "Follow E1 through the occurrence to the submitted item and its exclusive source span."
question: "Which history can Trace show?"
details: ["Requesting content and transitions lets an authorized reader inspect exact occurrences, available source revisions, decisions, lifecycle, and search projection information. Here the handoff supplies a plain source label. A versioned source would also carry a revision identity.","The span uses zero-based Unicode code points. Its end is exclusive and includes the newline in the first partition. The stored SHA-256 covers the original UTF-8 bytes. Trace has content and event bounds and exposes omissions."]
refs: ["internal/trace/service.go:33","internal/trace/postgres/remember_partitions.go:10","internal/trace/contract/result.go:95","internal/knowledge/contract/partition.go:32"]
guide: "/Technical-Reference/#trace_memory"
sample: "trace"
sample_label: "trace_memory arguments"
sequences: ["database_trace"]
---

The assistant passes the returned E1 ID to trace_memory. Its occurrence identifies the operations handoff and the exact span that supplied the routine retention rule.

The partition entry identifies the submitted item, its index, its original hash, and the start and end of this slice. That is how a reader can check the quote against the original submission.
