# Topics and Community placement

Team managers use **Topics** in the team portal. The first-party management
routes live under `/ui/api/topics`. Inspection requires read access. Saving,
discarding, publishing, and rollback require write access. Cookie-authenticated
writes require the portal CSRF token. MCP remains the external memory contract.

A manager can edit a Topic's name, description, aliases, and optional broader
Topic. The server checks that the parent is current and active, prevents cycles,
and bounds the hierarchy to 32 links. A parent can lie outside the current
catalog page. Historical override records remain in publication history.
Managers can remove retired actions from an open draft before reuse.

## Save, preview, publish, and roll back

Drafts belong to their authenticated creator. Save an edit against the current
catalog revision with an audit reason. Preview runs publication checks without
calling a provider or changing state. It shows up to ten source examples, plus
counts and a truncation indicator. A stale draft stays open for refresh and
review. Each accepted command appends a revision. An unchanged operation-key
retry returns the original result. Changed content with the same key conflicts.

Publication commits revisions, dependencies, audit, replay, and draft completion
together. Edits can invalidate affected memberships immediately. Topic
assignment rechecks affected eligible Evidence through the bounded background
worker. Rollback appends a compensating publication after checking intervening
revisions and validating restored state. Exact Evidence keeps its own history
and provenance.

## Topic assignment and Community placement

With organization enabled, Remember publishes validated direct memberships in
its Evidence transaction. A passage can join several Topics and can also justify
a new Topic. A genuinely unclear passage can remain uncategorized. The periodic
worker revisits eligible Evidence and places Topics under broader Topics or
records them as standalone. Evidence equivalence has a separate assessment path.
A pause stops background admissions while foreground assignment and current Topic
reads are available. Disabling organization defers assignment and Topic reads.
Ordinary Evidence remains searchable.

Recall filters on the union of selected Topics' current direct memberships.
Children do not expand that filter. Related-topic hints count authorized unseen
Evidence and report truncation. Unavailable selected Topics return
`topic_unavailable`. Private Evidence is ineligible for shared organization.

## Operator diagnosis

At the default TRACE level, protected JSON stdout carries manager publication
and provider details. Join `topic_semantic_draft_preview`,
`topic_semantic_manager_command`, `topic_semantic_manager_rejected`, and
`topic_semantic_publication_failed` to protected `topic_semantic_publication`
payload chunks by trusted team/profile, correlation, operation, draft, and
publication IDs. Where applicable, follow the run, batch, assessment, and
exchange IDs into provider request and response records. Check the manifest
count and SHA-256 before interpreting an excerpt. Missing, expired, and
truncated payloads limit the available diagnosis.

The control portal can also read retained failure captures when collection and
retention permit. It recognizes historical capture identifiers. Role, scope, and
owner mismatches reveal neither foreign records nor protected payloads.

## Storage upgrade

Startup drains or refuses incompatible maintenance writers and verifies the
physical Topic storage migration before listeners open. It preserves published
Topic IDs, memberships, equivalence, hierarchy, audit lineage, and historical
receipts. Use a verified pre-cutover PostgreSQL backup for recovery before new
writes. After new writes, recover forward with a compatible binary.

`ontology_management`, `ontology_organization`, and `ontology_upgrade` remain
registered Development E2E selectors. Retain actual PostgreSQL and production
entry-point evidence, including paused foreground assignment, stale revisions,
protected stdout, and current direct Recall.
