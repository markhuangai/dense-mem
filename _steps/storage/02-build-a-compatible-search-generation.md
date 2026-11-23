---
workflow: "storage"
order: 2
anchor: "build-a-compatible-search-generation"
title: "Build a compatible search generation"
chapter: "Build"
figure: "Maintain PostgreSQL and search indexes"
caption: "Embedding contracts and vector index generations are versioned."
guide: "/ANN-HNSW-And-Halfvec/#separate-versioned-contracts"
question: "Inspect this step"
details: ["Exact search and indexed approximate search have separate performance and readiness tradeoffs. Check the expected topology and measured coverage before changing a production default."]
---

Choose the documented index strategy and embedding dimensions together. Prepare a compatible generation and observe its progress before it becomes active.
