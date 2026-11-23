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
refs: ["internal/ontology/postgres/organization.go:428", "internal/ontology/postgres/organization_freshness.go:16", "internal/ontology/publication.go:96", "internal/ontology/MANAGEMENT.md:20", "internal/ontology/contract/types.go:68"]
guide: "/Topics-And-Organization/"
sequences: ["database_ontology"]
---

Before publishing, Dense-Mem rechecks the source versions, fingerprints, relevant definitions, and overrides. It commits the validated organization and its receipt together.

{{ site.data.examples.atlas.topics.retention.name }} now points directly to E1 and E3. {{ site.data.examples.atlas.topics.hours.name }} points to E2. Operations helps someone browse the catalog. Every factual statement in this example still comes from the original Evidence.
