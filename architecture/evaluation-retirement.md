# Historical evaluation runtime retirement

`cmd/eval-retire` preserves guarded historical resource enrollment, retirement
and verification. It cannot start or resume an evaluation. Historical deletion
requires direct approval of an exact inventory; this replacement does not retire
any historical runtime. Even `verify` acquires locks and is a mutation.

## Explicitly enroll prepared runtimes

```bash
go run ./cmd/eval-retire --action record \
  --runtime-dir tests/eval/runs/existing/runtime \
  --compose-project existing_project
```

Enrollment records the selected project's actual resources without stopping
them. Writable binds must be child directories of that runtime root. Existing
images are borrowed and remain protected. External volumes and networks remain
external. Anonymous volumes are associated with their exact container mounts.
Unknown ownership is not inferred from age, a name prefix or paused maintenance.

A terminal validation fixture can be included with repeatable
`--fixture-container FULL_CONTAINER_ID`. It must be exited and carry the label
`io.dense-mem.eval.runtime=RUNTIME_ID` from this receipt. An unrelated or running
Testcontainers fixture cannot be enrolled. Keep historical ownership evidence
and use a separately reviewed manual procedure for fixtures lacking that proof.

## Prepare retention evidence

Create a private JSON retention plan outside disposable storage; the command
rejects a plan located inside a directory that retirement would delete. Every evidence
category must contain exact file paths and SHA-256 values, or an explicit reason
why that category is unavailable. A complete result requires captures; failed or
superseded experiments use `incomplete` and retain their failure evidence.

```json
{
  "schema_version": "dense-mem.eval.runtime.v1",
  "archive_dir": "/absolute/path/comparison-archive",
  "outcome": "incomplete",
  "evidence": [
    {"kind": "frozen_inputs", "files": [{"path": "/absolute/path/seed.json", "sha256": "sha256:REPLACE_WITH_EXACT_HASH"}]},
    {"kind": "provenance", "files": [{"path": "/absolute/path/run_config.json", "sha256": "sha256:REPLACE_WITH_EXACT_HASH"}]},
    {"kind": "captures", "unavailable": "Candidate ended before capture; baseline capture is retained by its own run."},
    {"kind": "failures", "files": [{"path": "/absolute/path/failure.json", "sha256": "sha256:REPLACE_WITH_EXACT_HASH"}]},
    {"kind": "accounting", "files": [{"path": "/absolute/path/usage.json", "sha256": "sha256:REPLACE_WITH_EXACT_HASH"}]},
    {"kind": "private_audit", "files": [{"path": "/absolute/path/private-audit-input.json", "sha256": "sha256:REPLACE_WITH_EXACT_HASH"}]}
  ],
  "protected_runtimes": ["/absolute/path/active-baseline/runtime"],
  "pending_runtime_dependencies": []
}
```

Include all seed/suite sidecars, native captures, provenance, retained attempts,
failure logs, accounting, and private inputs needed by the comparison's consumers.
The command verifies the declared bytes; the operator owns declaring a complete
set for that experiment. Export required database-backed report/audit inputs
before retirement. Any unresolved `pending_runtime_dependencies` entry blocks
teardown. Unknown usage remains explicit evidence, not a fabricated zero.

The archive must be separate from retired and protected runtime roots. It uses
0700 directories and 0600 files. Originals remain in place. Archive files are
content-addressed and verified against `retention.json`; corrupted files are
never overwritten during recovery. Keep private audit inputs until their consumers
finish. This command does not automatically delete archives or credentials.

## Retire and verify

```bash
go run ./cmd/eval-retire --action retire \
  --runtime-dir tests/eval/runs/old-candidate/runtime \
  --retention-plan tests/eval/runs/old-candidate/retention.json

go run ./cmd/eval-retire --action verify \
  --runtime-dir tests/eval/runs/old-candidate/runtime

```

Retirement refuses active monitor locks, validates the archive before deletion,
stops servers before their storage services, and sends SIGTERM without Docker's
automatic SIGKILL escalation. `--stop-timeout` bounds observation (default 1m,
maximum 5m). A timeout leaves retirement incomplete and replacement blocked.
Diagnose the failed resource, then repeat the same retirement command and plan.
No global prune, age-based selection or forced removal is used.

If a registered monitor output directory was moved or removed, retirement can continue once all recorded container locks are
available. The monitor takes those locks outside its output directory before
registering, so a live monitor still blocks either operation even when its output
lock was unlinked. Existing output directories retain their usual lock checks;
other filesystem errors and missing container identities still block recovery.
The command does not recreate the output directory or remove its receipt entry.
Retirement still requires every declared source and retained archive to validate;
moving or deleting required evidence does not make it optional.

Only recorded resources are removed. Other container references protect images,
networks, volumes and overlapping bind storage. Shared resources remain with an
explicit retained disposition. New tags also protect images. Bind contents are
removed by a tracked helper using the recorded storage image, with no network,
only the exact data bind, and a read-only root filesystem. The helper uses no
Docker socket and does not follow links or cross filesystems during deletion.
Custom storage images therefore need `/bin/sh` and `find` with `-xdev` and
`-delete`; missing tools produce a visible cleanup failure.

Completion requires readback of resource absence, intact source/archive hashes,
and unchanged protected container IDs, start times, mounts, restart counts and
health. Runtime directories, lifecycle receipts, archives and retained evidence
survive. Keep and reverify the completed retirement receipt. This maintenance command
cannot start or resume evaluations. All current evaluation runs use runners.
