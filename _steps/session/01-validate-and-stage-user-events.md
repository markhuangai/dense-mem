---
workflow: "session"
order: 1
anchor: "validate-and-stage-user-events"
title: "Validate and stage user events"
chapter: "Stage"
figure: "Ingest a private session"
caption: "Accepted events are staged for one authenticated private space."
guide: "/Write-Pipeline/#private-session-intake"
question: "Inspect this step"
details: ["User events supply submitted text. Existing memory and earlier context can guide extraction. The authenticated actor fixes the target space."]
---

Submit exact user events with event identifiers and a stable request key. The server validates the intake and stages accepted events for its private destination.
