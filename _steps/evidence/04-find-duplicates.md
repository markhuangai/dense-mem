---
workflow: "evidence"
order: 4
anchor: "find-duplicates"
legacy: "step-4"
title: "Look for existing Evidence"
chapter: "Remember"
figure: "Duplicate candidates"
scene: "duplicates"
fallback: "Dense-Mem compares the new slice with the existing legal-hold passage."
caption: "E3 already exists in Atlas’s shared memory. A and B are still proposed slices."
question: "When can an existing ID be reused?"
details: ["An embedding is a list of numbers representing a passage’s meaning. For ordinary intake, eligible exact duplicates can be resolved directly. For other eligible slices, the server embeds unique content as needed and consults a bounded list of nearby candidates. The assessment can choose reuse only from that list.","A reused canonical ID can acquire another occurrence with the new source and exact wording. Explicit force-insert and lifecycle-bearing inputs, including versioned-source submissions, have their own new-occurrence rules.","E3 has the plain source label “Retention policy” in this example. Reading it for comparison does not submit it again."]
refs: ["internal/knowledge/postgres/evidence_duplicate.go:49","internal/knowledge/postgres/evidence_duplicate.go:110","internal/remember/service/submission_assessment_commit_input.go:87","internal/knowledge/postgres/evidence_duplicate.go:652"]
guide: "/Evidence-Model-And-Lifecycle/"
---

The team already has a passage about legal holds. Dense-Mem checks eligible exact matches and can use embeddings to find passages worth comparing with each new slice.

E3 is a plausible comparison for A. It describes an exception to expiry. A supplies the ordinary duration. Both pieces of information are useful.
