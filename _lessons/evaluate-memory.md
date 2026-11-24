---
slug: "evaluate-memory"
area: "operations"
order: 4
title: "Check whether search helped"
summary: "Give the system known questions and measure whether the returned sources support the answers."
references: [["Public evaluation", "/Evaluation/"], ["Development and testing", "/Development-And-Testing/"]]
covers: ["ops:evaluation", "profile:evaluation", "ops:seed-building"]
frames:
  - id: prepare
    title: "Start with the same inputs"
    behavior: "The evaluator chooses a saved set of questions and source notes."
    mechanics: "The GitHub-hosted workflow pins the production image and selected PostgreSQL seeds, then boots each seed with fresh writable storage and ontology off."
    story:
      - ["dataset", "Prepared questions", "Known source notes", ""]
      - ["run", "Evaluation", "Pinned inputs", "result"]
    flow:
      - ["dataset", "Selected dataset", "Resolve seed digest", ""]
      - ["run", "Hosted runner", "Restore fresh database", "gate"]
      - ["server", "Production server image", "Authenticated Recall and Trace", ""]
    fields: ["eval-runner"]
  - id: score
    title: "Compare results to the sources"
    behavior: "The report shows how often search surfaced a useful source."
    mechanics: "Single-call and multi-turn answer policies share the initial Recall. The evaluator scores against locked answers and source mappings."
    story:
      - ["question", "Example question", "$question", ""]
      - ["room", "Expected source", "$room", ""]
      - ["result", "Dataset report", "Recall and answer scores", "result"]
    flow:
      - ["question", "Locked question", "Run authenticated Recall", ""]
      - ["room", "Expected source mapping", "Score retrieved support", "gate"]
      - ["result", "Dataset report", "Record score and completion", "result"]
    fields: ["eval-result", "recall-id"]
sources: [["Public evaluation workflow", ".github/workflows/evaluation.yml#L105"], ["Evaluation guide", "tests/eval/README.md"]]
---
