---
workflow: "access"
order: 2
anchor: "fix-the-actor-and-its-allowed-spaces"
title: "Fix the actor and its allowed spaces"
chapter: "Actor"
figure: "Authenticate and scope a request"
caption: "The actor’s team, owner, grants, and scope come from authentication."
guide: "/Request-Lifecycle-And-Security/#memory-space-authorization"
question: "Inspect this step"
details: ["Team and profile isolation is applied in storage through transaction-local context. Hydration and mutations check authorization again at their boundary."]
---

Dense-Mem fixes the actor context before application policy runs. That context determines the eligible private and shared spaces and permitted mutations.
