---
workflow: "client"
order: 2
anchor: "retain-the-exact-request-and-operation-key"
title: "Retain the exact request and operation key"
chapter: "Call"
figure: "Integrate an MCP client"
caption: "Matching request identity gives a retry one durable outcome."
guide: "/Framework-Integrations/#remember-with-python"
question: "Inspect this step"
details: ["A client can display a progress token when the transport supports it. Use returned Evidence handles for follow-up calls."]
---

Build the exact payload, choose the destination from user intent, and retain a fresh operation key. Follow progress events until the terminal response arrives.
