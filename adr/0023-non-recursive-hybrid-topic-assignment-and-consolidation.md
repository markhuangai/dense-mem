# ADR 0023: Non-recursive Topic Assignment and Consolidation

- Status: Accepted
- Approval: [issue #845](https://github.com/markhuangai/dense-mem/issues/845) and its [implementation amendments](https://github.com/markhuangai/dense-mem/issues/845#issuecomment-7205682306)
- Supersedes: ADR 0020's Topic-vocabulary revalidation and coupled Evidence-equivalence clauses
- Refines: ADR 0021's Assignment validity and Topic publication rules, and ADR 0022's derived-Topic Decisions

## Decision

Eligible shared Evidence receives direct Topic membership from a bounded hybrid
candidate set. Candidate retrieval combines the current embedding contract's
Topic vectors with indexed full-text search and includes memberships requiring
repair. Remember reuses canonical Evidence embeddings. A missing Topic vector
allows lexical retrieval while maintenance backfills vectors. Manager Save
remains provider-free.

A new Topic does not invalidate prior Evidence assignments. Material Topic edits
and retirement requeue their direct active members. Source dependency
invalidation remains transitive. An automatic consolidation survivor's
alias-only revision does not invalidate its members. A current Assignment keeps
its accepted dependency fingerprint across Topic revisions while its Evidence
and target Topic remain current and eligible. A material Topic revision still
forces the affected membership through reassessment.

Maintenance batches independent assignment Decisions and validates the complete
response before publishing outcomes in isolated per-Evidence transactions. A
logical Decision call may use several wire exchanges under the provider's
limits. The Topic assignment setting also controls Decision assessments for
Topic consolidation and Evidence equivalence. A disabled stage uses structured
generation. Consolidation and equivalence require probability at least 0.95
when Decisions are enabled. Community placement uses its existing setting and
0.80 threshold.

Maintenance fairly interleaves assignment, Topic embedding backfill, Topic
consolidation, Community placement, and Evidence equivalence. A Topic is
eligible for placement when manager-authored or directly linked to at least two
active Evidence items. Consolidation preserves a manager-authored Topic in
preference to an automatically authored one, then the earliest original
publication, then the lowest Topic ID. It preserves the survivor's memberships
and placement, moves eligible memberships atomically, and validates hierarchy
limits. Rejected candidate pairs advance with their current Topic revisions.

Equivalence is independent of assignment and retains factual-context,
source-freshness, and authorization checks. Coverage and retry include its
pending and failed work. An operator can explicitly request reassessment of a
bounded set of eligible uncategorized Evidence. Ordinary failed-run retry remains
limited to failed work.

All schema upgrades follow ADR 0021's offline quiescence and backup procedure.
The new backfills and index creation are resumable. Reopening writes establishes
the forward-repair boundary.

## Risk and verification

- A merge racing with Remember or Manager Save can attach Evidence to a retired
  Topic or deadlock. Keep Remember catalog-lock-free, lock Topic names and rows
  before Evidence, and test the production paths concurrently.
- Treating alias enrichment as a material edit can requeue every survivor member.
  Track the last material revision and test marker draining and old Assignment
  fingerprints after a merge.
- A stale embedding can overwrite a newer Topic revision or cross a scope.
  Fence writes by active contract, scope, and Topic revision, and verify real
  PostgreSQL isolation across supported index tiers.
- A failed equivalence stage or ineligible Community can prevent coverage from
  converging. Test selection, status, retries, lease recovery, and Recall against
  real PostgreSQL.
