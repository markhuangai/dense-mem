---
workflow: "evidence"
order: 10
anchor: "publish-memberships"
legacy: "step-10"
title: "Publish the direct memberships"
chapter: "Organize"
figure: "The current topic catalog"
scene: "publish"
fallback: "Two passages are directly assigned to retention and one to support hours."
caption: "Operations is a catalog parent. The Evidence labels sit under the topics to which they are directly assigned."
question: "What if a source changes during classification?"
details: ["A stale source or relevant dependency can block publication. Automatic work cannot overwrite a newer relevant decision. The worker records the outcome and later eligible work can reassess the affected sources.","A topic has an optional parent. The parent link organizes definitions. Selecting a parent reads its own current direct assignments. The filter experiment later shows this using all three Evidence IDs."]
source_revision: 6767dd33f5d3e2546b4afac39122db7bf01549f9
refs: ["internal/knowledge/postgres/remember_commit.go:233","internal/topics/postgres/remember_assignment.go:48","internal/topics/MANAGEMENT.md:27"]
guide: "/Topics-And-Organization/"
sequences: ["database_topic_memberships"]
---

Before publishing, Dense-Mem rechecks relevant source and Topic revisions. Remember commits direct memberships with the Evidence, search state, and replay receipt. Community placement records a broader Topic or standalone outcome under its own revision checks.

{{ site.data.examples.atlas.topics.retention.name }} now points directly to E1 and E3. {{ site.data.examples.atlas.topics.hours.name }} points to E2. Operations helps someone browse the catalog. Every factual statement in this example still comes from the original Evidence.
