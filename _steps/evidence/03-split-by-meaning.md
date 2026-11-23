---
workflow: "evidence"
order: 3
anchor: "split-by-meaning"
legacy: "step-3"
title: "Split the note by meaning"
chapter: "Remember"
figure: "The exact source spans"
scene: "partition"
fallback: "The server splits the original at the newline and preserves it in the first slice."
caption: "↵ marks the stored newline. The two slices join back into the exact original."
question: "Who controls the wording and boundaries?"
details: ["The model returns server-issued boundary IDs in source order. Dense-Mem checks the request identity, coverage, ordering, and nonblank slices. Every character must belong to exactly one slice.","A heading and a dependent paragraph can stay together. An item can also yield one slice. A Remember item supports up to 16,384 code points. The whole request supports at most 100 partitions."]
refs: ["internal/assessor/partition.go:17","internal/assessor/partition.go:113","internal/assessor/partition.go:165","internal/knowledge/contract/partition.go:38"]
guide: "/Write-Pipeline/"
sequences: ["ai_partition"]
---

{{ site.data.examples.atlas.topics.retention.name }} and desk hours make sense independently. The server marks allowed cut points, and the partitioning model selects the boundary between these two statements.

Dense-Mem slices the original text itself. A includes the newline at the end of the first line. B starts on the next line. Offsets count Unicode code points, and each span’s end is exclusive.
