# Manager ontology workflow

Team managers use the Ontology section of the team portal or the first-party
`/ui/api/ontology` interfaces. Inspection requires `read`; saving, discarding,
publishing, and rollback require `write`. Cookie-authenticated POST and PUT
requests use the existing portal CSRF token. MCP remains the memory contract.

Drafts belong to their creating authenticated owner. Each successful save or
terminal command appends an immutable revision. An unchanged operation-key
retry returns the original result; a changed request conflicts. A stale draft
remains open. Refresh its catalog, record, and source versions explicitly, then
review, save, and preview again. Published history is visible to team managers.

The closed edits are `define`, `classify`, `group_together`, `keep_separate`,
`pin_definition`, and `remove_override`. Definitions are pinned by default.
Removing the pin override explicitly unpins the definition. Existing corrections
must be replaced using their ID and version or explicitly removed. Grouping
retains the existing compatibility policy and every original source handle.

Preview shares publication validation, changes no state, and calls no provider.
Counts cover the currently eligible stored dependency set; examples contain at
most ten sources and report truncation. Definition changes may affect additional
sources during later reassessment. Oversized validation contexts fail visibly.
Private sources, foreign teams, and old shared generations are excluded.

Publication commits ontology revisions, audit, replay receipt, and draft
completion atomically. Dependency fingerprints invalidate affected organization
immediately. Existing operator-controlled maintenance reassesses it; the portal
reports team coverage without global scheduling, model, or budget information.
Rollback appends a compensating publication after checking intervening record
changes and restored source eligibility. Canonical Knowledge is never edited.

The additive draft migration creates empty tables and indexes without a canonical
backfill or table rewrite. It forces owner and shared-generation RLS. Down
refuses populated history; production recovery rolls forward.

Issue #529 records the approved historical 1k waiver and replacement verification:
the unchanged nine-case/19-source organization cohort, actual Recall comparisons,
the separately locked manager correction cohort, and the registered exclusive
`ontology_management` production scenario with desktop/mobile browser checks.
Detailed reports remain in ignored paths. Verified compact evidence belongs in
`tests/eval/baselines/`; full final-head CI and production-image E2E remain gates.
