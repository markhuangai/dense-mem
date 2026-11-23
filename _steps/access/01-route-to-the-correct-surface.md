---
workflow: "access"
order: 1
anchor: "route-to-the-correct-surface"
title: "Route to the correct surface"
chapter: "Ingress"
figure: "Authenticate and scope a request"
caption: "MCP, browser, and operator surfaces have distinct authentication paths."
guide: "/Request-Lifecycle-And-Security/#network-surfaces"
question: "Inspect this step"
details: ["MCP can use the supported API-key or OAuth bearer route. Browser sessions and state-changing portal calls also follow the configured session and CSRF contract."]
---

The request arrives through the relevant listener and supplies the credential expected by that surface. The protected request boundary validates the input.
