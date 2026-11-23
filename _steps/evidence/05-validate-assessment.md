---
workflow: "evidence"
order: 5
anchor: "validate-assessment"
legacy: "step-5"
title: "Validate the whole assessment"
chapter: "Remember"
figure: "The intake decision"
scene: "assess"
fallback: "Both slices become new Evidence. The legal-hold passage remains independent."
caption: "The example yields two new passages. A policy and its legal-hold exception can coexist."
question: "What happens to incomplete or conflicting answers?"
details: ["If a response omits a required item or names an unknown candidate, Dense-Mem asks for a complete replacement within the retry bound. Exhausting that bound fails the write before semantic publication.","When admitted Evidence genuinely disagrees, a conflict can preserve the competing positions with exact citations. Server policy checks scope and ownership for lifecycle actions. A model’s preference cannot grant permission to overwrite someone else’s Evidence."]
refs: ["internal/remember/service/submission_assessment_synchronous.go:209","internal/assessor/assessor.go:190","internal/assessor/conflict_validation.go:69","internal/remember/service/submission_assessment_commit_input.go:87"]
guide: "/Write-Pipeline/"
sequences: ["ai_assessment"]
---

The assessor proposes structured decisions for the submitted slices and the candidates it was given. Here, A and B are both new and the legal-hold passage supplies a compatible exception.

The server checks the entire response, including required decisions, allowed IDs, and any quoted conflict positions. An invalid answer triggers bounded regeneration of the whole response.
