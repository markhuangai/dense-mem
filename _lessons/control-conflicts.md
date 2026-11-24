---
slug: "control-conflicts"
area: "control-portal"
order: 3
title: "Review two different answers"
summary: "Compare the original quotes and record a decision on an open conflict."
references: [["Conflicts and corrections", "/Conflicts-And-Corrections/"]]
covers: ["control:evidence-conflicts", "evidence-conflict"]
frames:
  - id: compare
    title: "Read the two sources"
    behavior: "Two authorized notes give different practice rooms. The operator opens their original quotes."
    mechanics: "The case contains distinct evidence positions, exact passages, and the version used for changes."
    story:
      - ["first", "Source A", "$room", ""]
      - ["second", "Source B", "$replacement", ""]
      - ["case", "Evidence conflicts", "Open case", "gate"]
    flow:
      - ["first", "Position A · exact quote", "$room", ""]
      - ["second", "Position B · exact quote", "$replacement", ""]
      - ["case", "Conflict case", "Version 3", "gate"]
    fields: ["failure-version", "trace-id"]
    diagram: "compare"
  - id: resolve
    title: "Record a decision"
    behavior: "The operator records a decision after reviewing both sources."
    mechanics: "Dense-Mem checks the case version and saves the decision with its history. Both source quotes remain available to authorized readers."
    story:
      - ["case", "Evidence conflicts", "Resolve after review", "gate"]
      - ["record", "Case history", "Decision recorded", "result"]
    flow:
      - ["case", "Versioned request", "Resolve with reason", "gate"]
      - ["record", "Recorded decision", "Case resolved", "result"]
    fields: ["decision", "failure-version"]
sources: [["Control conflict request fields", "internal/http/control_portal_evidence_conflicts.go#L19"]]
---
