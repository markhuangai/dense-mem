# ADR 0006: Permanent Architecture Enforcement

- Status: Accepted
- Date: 2026-09-15
- Supersedes: ADR 0005 sections on migration bookkeeping and duplicated role policy
- Refines: ADR 0003, ADR 0004

## Context

The capability ownership graph is now fully cut over. No compatibility bridge
or dependency exception remains, and worker ownership no longer needs a ticket
expiry. Keeping those migration records and their validation paths adds a
second lifecycle model to a permanent architecture check.

The role matrix was also copied into the root JSON manifest and the loader. Two
authoritative copies can diverge while the checker still appears healthy.

## Decision

The checked inventory uses schema version 2. Capability fragments declare exact
source units, worker anchors, and database-case ownership. Source records carry
only their exact path; workers carry their exact identity and permanent
capability ownership.

The checker owns the dependency role matrix in `architecture/manifest.mjs` and
injects it into the merged manifest. Forbidden edges always fail. The inventory
has no compatibility bridges, dependency exceptions, issue completion lists,
or ticket-based worker expiry. Retired fields and schema version 1 are rejected
so old metadata cannot be silently ignored.

Fragment sections are optional when empty. A section that is present remains
strictly validated. Independent Go, browser, and worker discovery continues to
fail closed for unclassified, missing, duplicate, or stale ownership.

## Consequences

Architecture metadata contains permanent ownership facts and one dependency
policy. Capability changes update their fragment without carrying migration
state forward. CI still enforces the complete production and evaluation graph,
browser reachability, visibility, PostgreSQL direction, and worker lifecycle
anchors.

## Risk

- Pruning file records could misassign a worker in a shared package. Retain
  required and cross-capability source records, compare effective owners before
  and after the cleanup, and keep the worker ownership checks.
- Optional sections could hide an omitted package or worker. Keep independent
  discovery and reject every unclassified, missing, duplicate, or stale unit;
  only an absent section, not a malformed one, means empty.
- Removing exception handling could make a legitimate boundary impossible to
  express. The migration is complete and the current graph has zero exceptions;
  preserve the role matrix and negative transport, application, provider, and
  adapter tests so forbidden edges remain enforced.

## Verification

Run the architecture checker and conformance UAT against the schema-version-2
inventory. Verify the source and worker counts and effective ownership against
the pre-cleanup graph, and retain positive and adverse dependency, visibility,
fragment, browser, and worker tests.
