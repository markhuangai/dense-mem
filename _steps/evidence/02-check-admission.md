---
workflow: "evidence"
order: 2
anchor: "check-admission"
legacy: "step-2"
title: "Check who can write and replay"
chapter: "Remember"
figure: "Admission and replay"
scene: "admit"
fallback: "The server authorizes the shared destination and checks the request key."
caption: "The destination comes from the request. Authentication supplies the actual space and owner."
question: "What if the same key arrives twice?"
details: ["The service hashes the request and serializes attempts for that key within the authenticated team and owner. A simultaneous caller waits for the first attempt’s outcome. Changing the payload under that key produces a conflict.","The destination matters here. A read-only team member can write their own private memory. Writing into the shared space requires the appropriate shared grant. A payload field cannot grant that access."]
refs: ["internal/remember/service/service.go:163","internal/remember/service/processor/remember_processor.go:101","internal/remember/service/processor/remember_processor.go:215"]
guide: "/Request-Lifecycle-And-Security/"
---

Dense-Mem resolves the team-shared destination from the engineer’s grants. It validates the input, runs the initial security scan, and checks whether this request has already finished.

This key is new, so processing continues. If the reply gets lost after a successful commit, sending the same request and key retrieves its recorded result.
