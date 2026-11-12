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

## Topic parents and direct membership

The definition editor supports topics. A topic's optional `parent_id` organizes
the catalog; it does not establish factual meaning or expand Recall to descendants.
The server checks active topic parents, cycles, and the 32-link depth bound.
Catalog pages are partial: an existing parent ID remains intact even when its
topic is not in the loaded page.

Set or clear Parent definition ID, save, reload, preview, and publish through
the existing draft workflow. Rollback appends a revision restoring the prior
parent. Entity classes, predicate concepts, `parent_relation`, Subclass of, and
Subproperty of are retired under
`adr/0017-evidence-only-memory-without-factual-relationships.md`; the graph-specific
refinement in `adr/0017-automatic-ontology-refinement.md` has no current path.

Assignments classify eligible shared Evidence directly. Related-topic hints
count authorized, eligible unseen Evidence and report truncation. Explicit
`topic_ids` select the union of current direct memberships without descendant
expansion. Missing, foreign, and stale IDs share `topic_unavailable`; ordinary
Recall reports unexpected topic-reader failures as optional degradation.
Pause preserves current reads; disabling organization removes topic reads.

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

Draft tables enforce owner and shared-generation RLS. The Evidence-only migration
discards incompatible unfinished graph drafts and preserves compatible topic
drafts and Evidence lineage. Stop incompatible instances and verify the matching
backup before cutover. After destructive commit, restoration requires that backup;
after new writes, recover forward unless separately approved.

Validate the unchanged nine-case/19-source organization cohort, actual Recall
comparisons, the locked manager-correction cohort and the registered exclusive
`ontology_management` scenario with desktop/mobile browser checks.
Detailed reports remain in ignored paths. Retain verified ticket evidence in
issue/PR comments and project memory. Only a newer overall result replaces
`tests/eval/baseline.json`; full final-head CI and production-image E2E remain gates.

## Manager command diagnostics

The existing context-aware logger records DEBUG
`ontology_semantic_draft_preview`, INFO `ontology_semantic_manager_command`,
and WARN `ontology_semantic_manager_rejected`. Required storage or audit failure
uses ERROR `ontology_semantic_publication_failed`. Successful save, publication,
pin, discard, and rollback commands are observed after their surrounding
transaction and audit finish. Preview is read-only and provider-free.

Use trusted team/profile, correlation, operation, draft, and publication IDs to
join outcomes with `ontology_semantic_publication` protected payload chunks.
The payloads retain admitted edits, loaded definitions, prepared publication,
draft state, and preview results. Rejection captures include specific reasons
and expected/observed versions. Role/scope denials and unavailable foreign or
owner-scoped items expose no body or foreign-state detail.

Production keeps its TRACE default. DEBUG also retains complete diagnostic
payloads; INFO filters those details. Complete protected chunks are stdout-only,
independent of optional telemetry and database operation-log summaries. Collect
the entire operation window before teardown, then filter by correlation and
draft or operation ID. Verify each manifest, chunk index/count, and SHA-256.
An unavailable or truncated required capture is incomplete diagnostic evidence.

The disposable management E2E scenario explicitly uses DEBUG, verifies stale
draft and incompatible-parent causes from logs alone, and checks credential
canaries. Keep protected proof in ignored private storage or protected artifacts
for seven days. Ordinary operation metadata retains its configured retention,
default 30 days; stdout retention remains deployment-owned.

## Operator assessment diagnostics

Ontology assessment receipts and maintenance attempt logs retain an optional
`validation` object for rejected provider responses. Its stage, rule code and
field family are server-owned identifiers; expected and actual counts appear
only for coverage or limit checks. Strict JSON decoding and delegated definition
validation identify the rejecting boundary without copying provider field names,
references, prose or the correction prompt into diagnostics. The existing
failure code, complete-response regeneration bound and usage accounting remain
unchanged. A successful attempt omits the object, including after earlier failed
attempts in the same assessment.

Historical receipts without this object remain readable and immutable. Absence
means no validation detail was recorded; it does not imply successful validation.
Without original exchanges, historical receipts cannot establish a validation
cause or identify a model or prompt defect.

Older binaries strictly decode assessment receipts and may reject the additional
field. Drain in-flight work and stop older readers and writers before deploying
the new binary. After new receipts exist, recover by rolling forward with a
compatible reader; do not rewrite history to support an older binary. Validate
with focused, PostgreSQL and production-entry checks, independent audit and
final current-head CI/E2E. Issue-specific historical waivers do not transfer.
