---
workflow: "evidence"
order: 6
anchor: "commit-evidence"
legacy: "step-6"
title: "Commit Evidence, memberships, and search data"
chapter: "Remember"
figure: "The PostgreSQL transaction"
scene: "commit"
fallback: "One committed transaction publishes the two Evidence IDs and their provenance."
caption: "The completed receipt marks the point when E1 and E2 exist as searchable shared Evidence."
question: "What if an embedding or database write fails?"
details: ["Embedding calls run before the final write transaction. The commit checks mutation authority, duplicate decisions, and the active embedding configuration again. It writes the original item, partition mapping, assessment history, applicable lifecycle or conflict records, eligible direct Topic memberships, search documents, vectors, and terminal receipt together.","A required embedding failure or a failed commit publishes zero new Evidence from this request. A failure record may still explain the attempt. Progress notifications, including waits and retries, acknowledge a stage only. The terminal reply reports the outcome."]
source_revision: 6767dd33f5d3e2546b4afac39122db7bf01549f9
refs: ["internal/remember/service/processor/remember_preparation.go:147", "internal/knowledge/postgres/remember_commit.go:44", "internal/knowledge/postgres/remember_commit.go:171", "internal/remember/service/progress.go:10", "migrations/postgres/v2_8/20261113020000_remember_partitions.sql:16"]
guide: "/Data-Model-And-Storage/"
sequences: ["database_commit"]
---

Dense-Mem prepares the required search embeddings. One PostgreSQL transaction then saves the accepted passages, a record of each appearance in the source (an occurrence), and the data needed to search them.

A becomes E1 and B becomes E2. Their receipt can now be replayed. Recall can find E1 and E2 immediately. With organization enabled, their direct Topic memberships are already available. The next steps show how Dense-Mem assigns Topics and revisits them later.
