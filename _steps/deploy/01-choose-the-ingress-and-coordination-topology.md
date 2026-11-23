---
workflow: "deploy"
order: 1
anchor: "choose-the-ingress-and-coordination-topology"
title: "Choose the ingress and coordination topology"
chapter: "Design"
figure: "Route a production deployment"
caption: "The topology determines whether coordination can remain in process."
guide: "/Public-HTTPS-And-Redis/"
question: "Inspect this step"
details: ["Single-instance deployments can use documented process-local coordination. Multi-instance deployments require shared coordination. PostgreSQL remains the durable authority."]
---

Choose an external HTTPS endpoint and the configured deployment topology. Route normal user traffic to the main listener and protect the operator control listener.
