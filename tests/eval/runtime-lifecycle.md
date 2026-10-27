# Local evaluation runtime retirement

Pausing ontology maintenance preserves a runtime for later use. It does not
retire its containers, network or storage. Retire a superseded runtime explicitly
before starting its replacement. These commands change only local Docker
resources; they do not change ontology policy or relax capture readiness.

## Start and resume

Use a private runtime directory with writable database/cache bind mounts only in
strict child directories. Configuration, credentials, captures and reports must
remain outside those disposable bind directories. Supply the usual evaluation
environment, including `DENSE_MEM_EVAL_ENV_FILE` when the Compose example needs
an alternative private environment file.

```bash
scripts/eval-local.sh --mode runtime --runtime-action start \
  --runtime-dir tests/eval/runs/comparison-candidate/runtime \
  --compose-project comparison_candidate --new-runtime
```

`--compose-file` selects a prepared Compose definition. The default is
`examples/docker-compose.evaluation.yml`. `V1_COMPOSE_DATA_DIR` is set to the
absolute runtime directory for Compose. The first start requires either
`--new-runtime` for an independent run or `--supersedes` for a replacement.
`--compose-up` on an ordinary evaluator invocation delegates to the same owner;
pass its runtime arguments alongside the existing seed/suite arguments.

Resume with the same runtime directory, without `--new-runtime` or
`--supersedes`. Existing containers retain their IDs; stopped containers can
restart, but changed identities cannot be silently recreated. Interrupted
preparation retains a `preparing` receipt and can resume with the same Compose
source. A recorded `retiring` or `retired` runtime cannot resume.

Startup refuses writable named volumes or bind paths already mounted by another
runtime. Generated Compose overrides bind containers, networks, named volumes
and build images to the persisted runtime ID. Recovery requires those labels or
previously recorded exact identities; a reused project name is insufficient.
Completed builds remain inventoried even if container creation subsequently fails.
When the command runs inside a development container, its own enclosing container
is excluded from foreign bind-storage checks only after matching the kernel
boot ID and PID/mount namespaces. A matching hostname alone never grants that
exception. Docker and the command must see the same runtime bind paths.

The lifecycle companion `runtime-resources.json` records exact resource and
storage identities. Keep it and `runtime-compose.json`; do not copy them onto a
different runtime or edit them to bypass a failed check. The command prints only
the runtime ID, lifecycle state and receipt path, never resolved configuration
or Docker inspection environment values.

## Explicitly enroll prepared runtimes

```bash
scripts/eval-local.sh --mode runtime --runtime-action record \
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

For a paired run, use the enrolled runtime root as `runtime.state_dir`. The
monitor checks lifecycle state under its container locks and registers its output
lock for retirement. Existing unregistered manifests retain their previous resume
behavior; existing capture/configuration hashes are not rewritten.

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

## Retire, verify and replace

```bash
scripts/eval-local.sh --mode runtime --runtime-action retire \
  --runtime-dir tests/eval/runs/old-candidate/runtime \
  --retention-plan tests/eval/runs/old-candidate/retention.json

scripts/eval-local.sh --mode runtime --runtime-action verify \
  --runtime-dir tests/eval/runs/old-candidate/runtime

scripts/eval-local.sh --mode runtime --runtime-action start \
  --runtime-dir tests/eval/runs/new-candidate/runtime \
  --compose-project new_candidate \
  --supersedes tests/eval/runs/old-candidate/runtime
```

Retirement refuses active monitor locks, validates the archive before deletion,
stops servers before their storage services, and sends SIGTERM without Docker's
automatic SIGKILL escalation. `--stop-timeout` bounds observation (default 1m,
maximum 5m). A timeout leaves retirement incomplete and replacement blocked.
Diagnose the failed resource, then repeat the same retirement command and plan.
No global prune, age-based selection or forced removal is used.

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
survive. The completed retirement receipt is required and reverified before a
declared replacement starts. Raw Docker commands remain outside this lifecycle;
use the guarded entrypoint for release comparisons.
