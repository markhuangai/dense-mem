---
workflow: "evidence"
order: 14
anchor: "return-evidence"
legacy: "step-14"
title: "Return the passage and a topic hint"
chapter: "Recall"
figure: "The first recall result"
scene: "return"
fallback: "Recall returns the exact rule and hints at its retention topic."
caption: "Two eligible direct members belong to the hinted topic. One of them is the E1 just returned."
question: "What else can be in the result?"
details: ["A current, eligible equivalence group can produce one representative and bounded alternate IDs. Recall can also attach conflict positions with exact quotes, search state, and explicit degradations. Long historical contexts and bounded alternate lists can report truncation.","Topic hints come from the returned Evidence and its visible equivalents. Recall ranks topics by overlap with these hits, then by additional eligible Evidence. It reports truncation when the topic limit cuts the list."]
refs: ["internal/recall/retrieval.go:94", "internal/recall/recall_ontology.go:80", "internal/recall/postgres/recall_ontology.go:246", "internal/recall/recall.go:390", "internal/recall/postgres/recall_repository.go:407"]
guide: "/Recall-Ranking/"
sequences: ["database_read"]
---

Recall loads the actual records and checks their visibility and lifecycle again. With limit 1, the caller gets E1’s exact passage, its Evidence ID, and its shared-space label.

The winning passage is a direct member of the hinted retention topic. The example includes the winner and the legal-hold passage in its eligible count because the request supplied no known IDs.
