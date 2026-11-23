---
workflow: "evidence"
order: 8
anchor: "choose-topic"
legacy: "step-8"
title: "Choose a topic for each passage"
chapter: "Organize"
figure: "Topic classification"
scene: "classify"
fallback: "The organizer assigns the retention rule and its exception to the same topic."
caption: "A topic definition has a name and description. An assignment refers to that definition and to one Evidence source."
question: "What if a suitable topic does not exist?"
details: ["Dense-Mem prioritizes required assignments and manager overrides, then looks for current definitions with matching names and descriptions. It sends up to 20 topic definitions per batch and trims the list to the configured vocabulary token bound.","A supplied definition carries its key, label, description, aliases, and a parent reference when that parent is also in the shortlist. The model can select a supplied topic, propose a genuinely new definition, or mark an open item ambiguous.","An existing eligible assignment or manager classification override fixes that choice. A batch whose decisions are all fixed can complete without a model call."]
refs: ["internal/ontology/postgres/organization.go:185","internal/ontology/contract/organization.go:128","internal/ontology/request.go:17","internal/ontology/assessment/provider.go:87"]
guide: "/Topics-And-Organization/"
sequences: ["ai_ontology"]
---

The organizer receives the exact passages and a shortlist of existing topics. Atlas’s shortlist includes **{{ site.data.examples.atlas.topics.retention.name }}** and **{{ site.data.examples.atlas.topics.hours.name }}**.

A batch can receive up to **20 topic definitions**, with a separate token limit. The server supplies fixed assignments. The model answers the open classification and equivalence questions.
