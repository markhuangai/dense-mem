---
workflow: "evidence"
order: 8
anchor: "choose-topic"
legacy: "step-8"
title: "Assign Topics to each passage"
chapter: "Organize"
figure: "Direct Topic assignment"
scene: "classify"
fallback: "The organizer assigns the retention rule and its exception to the same topic."
caption: "A topic definition has a name and description. An assignment refers to that definition and to one Evidence source."
term:
  id: "vocabulary-definition"
  label: "Bounded vocabulary"
  definition: "The scoped shortlist of up to 20 existing Topics."
question: "What if a suitable topic does not exist?"
details: ["With optional Decisions enabled, Dense-Mem checks each candidate and asks whether relevant meaning remains uncovered.","Structured generation can match several existing Topics and propose new ones in the same reply. It supplies an exact supporting quote for each selection or proposal.","An unclear passage can remain uncategorized. It is still searchable."]
source_revision: main
refs: ["internal/topicassignment/service.go:49","internal/topicassignment/generation.go:19","internal/topicassignment/decisions.go:17"]
guide: "/Topics-And-Organization/"
sequences: ["ai_topic_assignment"]
---

For each passage, the assignment assessor receives exact text and a shortlist of existing Topics. Atlas’s shortlist includes **{{ site.data.examples.atlas.topics.retention.name }}** and **{{ site.data.examples.atlas.topics.hours.name }}**.

The proposal example illustrates a second direct Topic for E3 while retaining the existing Snapshot retention match. In the main example, E3 uses the retention Topic.
