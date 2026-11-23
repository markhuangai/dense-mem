---
workflow: "client"
order: 3
anchor: "resolve-an-unknown-outcome"
title: "Resolve an unknown outcome"
chapter: "Recover"
figure: "Integrate an MCP client"
caption: "A safe retry preserves the original request identity."
guide: "/Using-Dense-Mem/#unknown-outcomes"
question: "Inspect this step"
details: ["A changed payload requires a new operation identity. A stale topic or retired contract requires a refreshed handle or a deliberate new request."]
---

When a connection ends before the terminal response, retry with the same operation key and the same arguments when the contract allows it. Follow the response’s recovery guidance.
