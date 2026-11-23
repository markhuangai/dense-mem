---
workflow: "storage"
order: 3
anchor: "check-readiness-and-retrieval-after-the-change"
title: "Check readiness and retrieval after the change"
chapter: "Activate"
figure: "Maintain PostgreSQL and search indexes"
caption: "The running service reports when eligible search state has converged."
guide: "/Operations-Runbook/#search-convergence"
question: "Inspect this step"
details: ["A pending or failed projection can change search coverage. Rebuild and replay through the documented operational path while retaining canonical Evidence."]
---

Validate the active generation and supported search path using the runbook. Compare results with the expected Evidence and check degraded states.
