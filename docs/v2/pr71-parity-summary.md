# PR #71 Parity Ledger

This directory tracks the controlled replacement of PR #71 with the canonical
wiki architecture. PR #71 is evidence, not the implementation plan.

- `pr71-parity-ledger.tsv` contains one row for every file changed by PR #71
  plus wiki-required gaps absent from that branch.
- `source=pr71` rows must exactly match the git diff from `main@4293ba4` to
  PR #71 head `54b0fa5`.
- `source=wiki-gap` rows name required work that PR #71 did not implement or
  implemented in a wiki-conflicting way.
- `disposition` means `retain`, `replace`, `exclude`, or `add`.
- `owner_issue` must be one active roadmap issue from #74 through #95.

Every V2 PR must update the rows it owns before merge. If a later review finds
that a row belongs to another issue, update the ledger in that PR and explain
the dependency impact in the PR body.

Validate with:

```bash
./scripts/validate-v2-parity-ledger.sh --self-test
```

The self-test validates the real ledger, then removes one PR #71 row from a
copy and proves the validator fails on the missing-path case.
