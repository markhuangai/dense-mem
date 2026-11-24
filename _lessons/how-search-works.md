---
slug: "how-search-works"
area: "backend"
order: 2
title: "Find it with different words"
summary: "See why “Where’s practice?” can retrieve a note that says “rehearse.”"
references: [["Recall ranking", "/Recall-Ranking/"], ["Vector search and ranking", "/Vector-Search-And-Ranking/"], ["Physical vector indexing", "/ANN-HNSW-And-Halfvec/"]]
covers: ["backend:recall", "backend:vector", "backend:ranking", "backend:search-index"]
frames:
  - id: compare
    title: "Look for words and meaning"
    behavior: "Sam’s question uses different words from Maya’s note."
    mechanics: "Full-text matching and vector similarity can produce different eligible candidate lists."
    story:
      - ["query", "Sam asks", "$question", ""]
      - ["room", "Maya wrote", "$room", ""]
    flow:
      - ["query", "Query", "$question", ""]
      - ["word", "Words", "Look for overlapping terms", ""]
      - ["meaning", "Meaning", "Look for a similar idea", "gate"]
    fields: ["question", "query-limit"]
    diagram: "split"
  - id: rank
    title: "Return the best match"
    behavior: "The room note answers the question. The gear note is less useful here."
    mechanics: "Recall combines candidate rank signals, reapplies access during hydration, and returns the selected exact passage."
    story:
      - ["room", "Best match", "$room", "result"]
      - ["gear", "Another note", "$gear", "quiet"]
    flow:
      - ["meaning", "Eligible candidates", "Room and gear notes", ""]
      - ["room", "Ranked first", "$room", "result"]
      - ["gear", "Outside this result limit", "$gear", "quiet"]
    fields: ["rank", "selected-quote", "recall-id", "query-limit"]
    diagram: "compare"
tools: ["recall_memory"]
---
