---
workflow: "evidence"
order: 15
anchor: "follow-topic"
legacy: "step-15"
title: "Follow the topic to the exception"
chapter: "Follow up"
figure: "Try a direct-membership filter"
scene: "follow"
fallback: "Excluding E1 leaves E3 in the retention topic."
caption: "Each filter uses known_evidence_ids = [E1]. Compare a child topic, its parent, and both children."
question: "What goes into the follow-up call?"
details: ["The query is optional when topic IDs are present. In ordinary use, the caller copies the current topic ID from a previous hint and supplies the Evidence IDs it already has. The selector also exposes two catalog alternatives to make the membership rule visible.","A valid current topic with zero direct members can return an empty result. A missing, stale, or inaccessible explicit topic returns topic_unavailable. The filter never silently broadens into a general search. With known eligible members of an equivalence group, duplicate group results can also be suppressed."]
source_revision: 6767dd33f5d3e2546b4afac39122db7bf01549f9
refs: ["internal/recall/postgres/recall_ontology.go:167","internal/topics/postgres/topics.go:15","internal/recall/recall_ontology.go:38","internal/recall/recall.go:404"]
guide: "/Topics-And-Organization/"
sample: "follow"
sample_label: "recall_memory arguments for the selected filter"
---

The assistant follows {{ site.data.examples.atlas.topics.retention.name }} in a second recall_memory call and supplies E1 as already seen. E3 is the remaining direct member, so the caller discovers the legal-hold exception.

Try the parent by itself. It has no direct members. Select both child topics to take their union, exclude the known passage, and return the other direct members.
