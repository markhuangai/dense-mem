import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { test } from "node:test";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import policy from "../../.github/scripts/image-release-policy.cjs";
import { actionStep, actionSteps, jobScripts, parseWorkflow, readWorkflow, runStep, stringifyWorkflow, workflowJob } from "./workflow_helpers.mjs";

const {
  compareContainsMain,
  decideAutomaticPreview,
  decideLabelApproval,
  decidePreviewEvent,
  decideRcPreview,
  parseSuccessfulPolicyStatus,
  resolvePreviewAttempt,
  selectMergedPull,
  validatePinnedProductionImageReference,
  validateProductionImageReference,
} = policy;
const execFileAsync = promisify(execFile);
const ociImageScript = fileURLToPath(new URL("../../.github/scripts/oci-image.sh", import.meta.url));

const baseEvent = {
  action: "synchronize",
  eventLabel: "",
  hasPreviewLabel: false,
  triggerHeadMatches: true,
  actorPermission: "none",
  pullRequestAuthor: "other",
  pullRequestAuthorPermission: "none",
  pullRequestState: "open",
  pullRequestBase: "main",
};

test("automatic preview builds require the owner and repository-admin permission", () => {
  const input = {
    pullRequestAuthor: "Z-M-Huang",
    pullRequestAuthorPermission: "admin",
    pullRequestState: "open",
    pullRequestBase: "main",
  };
  assert.deepEqual(decideAutomaticPreview(input), {
    authorized: true,
    reason: "owner_admin_pr",
  });
  for (const [label, overrides, reason] of [
    ["author", { pullRequestAuthor: "other" }, "automatic preview builds are restricted to the owner admin PR"],
    ["permission", { pullRequestAuthorPermission: "write" }, "automatic preview builds are restricted to the owner admin PR"],
    ["state", { pullRequestState: "closed" }, "the pull request is not open against main"],
    ["base", { pullRequestBase: "release" }, "the pull request is not open against main"],
  ]) {
    assert.deepEqual(
      decideAutomaticPreview({ ...input, ...overrides }),
      { authorized: false, reason },
      `${label} must be rejected`,
    );
  }
});

test("deploy-test-image approval is one-shot and admin-only", () => {
  const input = {
    eventLabel: "deploy-test-image",
    actorPermission: "admin",
    pullRequestState: "open",
    pullRequestBase: "main",
  };
  assert.deepEqual(decideLabelApproval(input), {
    authorized: true,
    reason: "admin_label",
  });
  for (const [label, overrides, reason] of [
    ["label", { eventLabel: "documentation" }, "irrelevant_label"],
    ["permission", { actorPermission: "maintain" }, "deploy-test-image approval requires a repository admin"],
    ["state", { pullRequestState: "closed" }, "the pull request is not open against main"],
    ["base", { pullRequestBase: "release" }, "the pull request is not open against main"],
  ]) {
    assert.deepEqual(
      decideLabelApproval({ ...input, ...overrides }),
      { authorized: false, reason },
      `${label} must be rejected`,
    );
  }
});

test("production image reference validation rejects malformed inputs directly", () => {
  for (const [image, reason] of [
    ["", "the image reference is empty or contains whitespace"],
    ["ghcr.io/other/project:v2.6.1", "the image is outside the Dense-Mem GHCR repository"],
    ["ghcr.io/markhuangai/dense-mem:bad tag", "the image tag is invalid"],
    ["ghcr.io/markhuangai/dense-mem@sha256:not-a-digest", "the image digest is invalid"],
  ]) {
    assert.deepEqual(
      validateProductionImageReference(image, "markhuangai/dense-mem"),
      { valid: false, reason },
    );
  }
});

test("production E2E image handoff requires a digest-pinned reference", () => {
  const digest = `sha256:${"a".repeat(64)}`;
  assert.deepEqual(
    validatePinnedProductionImageReference(
      "ghcr.io/markhuangai/dense-mem:test-42",
      "markhuangai/dense-mem",
    ),
    { valid: false, reason: "the E2E image must be pinned by digest" },
  );
  assert.deepEqual(
    validatePinnedProductionImageReference(
      `ghcr.io/markhuangai/dense-mem:test-42@${digest}`,
      "markhuangai/dense-mem",
    ),
    {
      valid: true,
      repository: "ghcr.io/markhuangai/dense-mem",
      reference: `ghcr.io/markhuangai/dense-mem:test-42@${digest}`,
    },
  );
});

test("automatic preview authorization rejects every authorization boundary", () => {
  const input = {
    pullRequestAuthor: "Z-M-Huang",
    pullRequestAuthorPermission: "admin",
    pullRequestState: "open",
    pullRequestBase: "main",
  };
  for (const [label, overrides, reason] of [
    ["PR image", { pullRequestAuthor: "other" }, "automatic preview builds are restricted to the owner admin PR"],
    ["PR permission", { pullRequestAuthorPermission: "write" }, "automatic preview builds are restricted to the owner admin PR"],
    ["PR state", { pullRequestState: "closed" }, "the pull request is not open against main"],
    ["PR base", { pullRequestBase: "feature" }, "the pull request is not open against main"],
  ]) {
    assert.deepEqual(
      decideAutomaticPreview({ ...input, ...overrides }),
      { authorized: false, reason },
      `${label} must be rejected`,
    );
  }
});

function assertHostedJob(workflow, name) {
  assert.equal(workflowJob(workflow, name)["runs-on"], "ubuntu-latest", name + " must use a GitHub-hosted runner");
}

function assertQemuBeforeBuildx(workflow, name) {
  const job = workflowJob(workflow, name);
  const qemu = actionStep(job, "docker/setup-qemu-action");
  const buildx = actionStep(job, "docker/setup-buildx-action");
  assert.ok(job.steps.indexOf(qemu) < job.steps.indexOf(buildx), name + " must configure QEMU before Buildx");
  assert.equal(qemu.with.platforms, "arm64");
}

function assertPythonMatrix(job, supportedVersions) {
  const matrix = job.strategy.matrix;
  assert.ok(supportedVersions.length > 0);
  assert.deepEqual(new Set(matrix.python.map(String)), new Set(supportedVersions));
  assert.deepEqual(matrix.sdk, ["latest"]);
  assert.deepEqual(matrix.exclude ?? [], []);
  const minimum = matrix.include.filter((row) => row.sdk === "minimum");
  assert.equal(minimum.length, 1);
  assert.ok(supportedVersions.includes(String(minimum[0].python)));
}

function assertSharedCI(shared, supportedVersions) {
  assert.deepEqual(new Set(Object.keys(shared.jobs)), new Set(["npm-audit", "postgres-migrations", "quality", "go-tests", "coverage", "coverage-merge", "python", "result"]));
  assert.equal(Number(shared.env.COVERAGE_THRESHOLD), 90);
  for (const job of Object.values(shared.jobs)) {
    for (const scope of [job, ...(job.steps ?? [])]) assert.ok(!Object.hasOwn(scope.env ?? {}, "COVERAGE_THRESHOLD"));
  }
  for (const name of ["npm-audit", "postgres-migrations", "quality", "go-tests", "coverage", "python"]) {
    assertHostedJob(shared, name);
    assert.equal(shared.jobs[name].needs, undefined, name + " must be independent");
  }
  const audit = jobScripts(shared.jobs["npm-audit"]);
  assert.doesNotMatch(audit, /npm ci/);
  for (const option of ["--fetch-retries=4", "--fetch-retry-factor=2", "--fetch-retry-mintimeout=1000", "--fetch-retry-maxtimeout=30000", "--fetch-timeout=30000"]) {
    assert.equal(audit.split(option).length - 1, 1, option + " must be shared by every audit retry");
  }
  assert.match(audit, /audit_with_retry\(\)/);
  assert.match(audit, /local max_attempts=4/);
  assert.match(audit, /while \(\( attempt <= max_attempts \)\)/);
  assert.match(audit, /npm \(warn\|error\) \(audit \)\?5\[0-9\]\{2\}/);
  assert.match(audit, /npm \(warn\|error\) audit network timeout/);
  assert.match(audit, /E5\[0-9\]\{2\}\|ECONNRESET\|ETIMEDOUT\|EAI_AGAIN\|ENETUNREACH\|ECONNREFUSED/);
  assert.match(audit, /status=\$\?/);
  const transientGuard = audit.indexOf("if ! grep -Eq");
  const nonTransientReturn = audit.indexOf('return "${status}"', transientGuard);
  const retryBackoff = audit.indexOf("sleep_seconds=$(", transientGuard);
  assert.ok(transientGuard !== -1 && nonTransientReturn > transientGuard && nonTransientReturn < retryBackoff);
  for (const prefix of [".lint", "web", "packages/mcp-proxy"]) {
    assert.equal(audit.split("\n").filter((line) => line.trim() === "audit_with_retry " + prefix).length, 1);
  }

  const quality = shared.jobs.quality;
  for (const command of [
    "npm ci --prefix .lint", "npm ci --prefix packages/mcp-proxy", "npm run --prefix .lint lint:lines",
    "node scripts/check-architecture.mjs", "npm run test:coverage --prefix packages/mcp-proxy",
    "node --test tests/uat/image_release_policy.test.mjs", "node --test tests/uat/prerelease_version.test.mjs",
    "node --test tests/uat/go_vulnerability_scan_policy.test.mjs", "node --test tests/uat/ai_pr_review_policy.test.mjs",
    "bash scripts/test-evaluation.sh",
  ]) runStep(quality, command);
  for (const command of ["go test -timeout=", "scripts/static-analysis.sh", "scripts/go-vulnerability-scan.sh"]) {
    runStep(shared.jobs["go-tests"], command);
  }
  const merge = shared.jobs["coverage-merge"];
  runStep(merge, "scripts/coverage-report.sh --merge-complete");
  assertHostedJob(shared, "coverage-merge");
  assert.deepEqual([merge.needs].flat(), ["coverage"]);
  const migrations = shared.jobs["postgres-migrations"];
  runStep(migrations, 'scripts/check-postgres-migrations.sh "${{ inputs.migration-base-ref }}"');
  assert.doesNotMatch(jobScripts(migrations), /go test|coverage-report\.sh/);
  assert.doesNotMatch(jobScripts(quality), /go test|coverage-report\.sh|audit_with_retry|check-postgres-migrations\.sh/);
  const input = shared.on.workflow_call.inputs["migration-base-ref"];
  assert.equal(input.required, false);
  assert.equal(input.type, "string");
  assert.equal(input.default, "origin/main");
  const python = shared.jobs.python;
  const command = runStep(python, "packages/python/scripts/matrix.sh").run;
  assert.ok(command.includes('"$RUNNER_TEMP/python-matrix"'));
  assert.ok(command.includes("${{ matrix.python }}"));
  assert.ok(command.includes("${{ matrix.sdk }}"));
  assertPythonMatrix(python, supportedVersions);
}

test("low-trust preview builds do not export an Actions cache", async () => {
  const workflow = await readWorkflow("pr-test-image.yml");
  assert.ok(Object.hasOwn(workflow.on, "pull_request_target"));
  for (const job of Object.values(workflow.jobs)) {
    for (const step of job.steps ?? []) assert.equal(step.with?.["cache-to"], undefined);
    assert.doesNotMatch(jobScripts(job), /--cache-to/);
  }
});

test("shared CI partitions slow gates independently of YAML layout and setup versions", async () => {
  const [shared, pullRequest, push] = await Promise.all([
    readWorkflow("ci-shared.yml"), readWorkflow("ci-pr.yml"), readWorkflow("ci-push.yml"),
  ]);
  const { stdout } = await execFileAsync("python3", ["-c",
    "import json,sys,tomllib; print(json.dumps(tomllib.load(open(sys.argv[1], 'rb'))['project']['classifiers']))",
    fileURLToPath(new URL("../../packages/python/pyproject.toml", import.meta.url)),
  ]);
  const supported = JSON.parse(stdout).filter((value) => /^Programming Language :: Python :: \d+\.\d+$/.test(value))
    .map((value) => value.split(" :: ").at(-1));
  assertSharedCI(shared, supported);
  const upgraded = structuredClone(shared);
  for (const job of Object.values(upgraded.jobs)) {
    for (const step of job.steps ?? []) {
      step.name = "Renamed CI step";
      if (step.uses) step.uses = step.uses.split("@")[0] + "@v99";
      if (step.with?.["go-version"]) step.with["go-version"] = "1.27.1";
      if (step.with?.["node-version"]) step.with["node-version"] = "26";
    }
  }
  assertSharedCI(parseWorkflow(stringifyWorkflow(upgraded, { indent: 4, sortKeys: true, forceQuotes: true })), supported);
  for (const mutate of [
    (w) => { delete w.jobs["go-tests"]; },
    (w) => { w.jobs.quality.steps = w.jobs.quality.steps.filter((step) => !step.run?.includes("node scripts/check-architecture.mjs")); },
    (w) => { w.jobs.python.strategy.matrix.python.pop(); },
    (w) => { w.jobs.python.strategy.matrix.python.push("9.99"); },
    (w) => { w.jobs.python.strategy.matrix.include = []; },
  ]) {
    const invalid = structuredClone(shared);
    mutate(invalid);
    assert.throws(() => assertSharedCI(invalid, supported));
  }
  const future = structuredClone(shared.jobs.python);
  future.strategy.matrix.python.push("3.15");
  future.strategy.matrix.include[0].python = "3.15";
  assertPythonMatrix(future, [...supported, "3.15"]);
  assert.equal(pullRequest.name, "CI Pull Request");
  assert.equal(push.name, "CI Push");
  for (const workflow of [pullRequest, push]) {
    assert.equal(workflow.jobs.ci.name, "CI");
    assert.equal(workflow.jobs.ci.uses, "./.github/workflows/ci-shared.yml");
  }
  assert.equal(push.jobs.ci.with["migration-base-ref"], "${{ github.event.before }}");
});

test("non-E2E image and release jobs use GitHub-hosted runners", async () => {
  const [preview, prereleaseImage, demoImage, prerelease, release, startPrerelease] =
    await Promise.all([
      readWorkflow("pr-test-image.yml"),
      readWorkflow("publish-image.yml"),
      readWorkflow("publish-demo-image.yml"),
      readWorkflow("release-rc.yml"),
      readWorkflow("release.yml"),
      readWorkflow("start-prerelease.yml"),
    ]);

  for (const name of ["resolve", "build", "publish", "report", "report-e2e", "resolve-failure"]) {
    assertHostedJob(preview, name);
  }
  assertHostedJob(prereleaseImage, "publish");
  assertHostedJob(demoImage, "publish-demo");
  for (const name of [
    "classify-release",
    "prepare-prerelease",
    "resolve-production-source",
    "promote-preview-image",
  ]) {
    assertHostedJob(prerelease, name);
  }
  for (const name of ["prepare-release", "create-release-tag", "promote-images", "create-github-release"]) {
    assertHostedJob(release, name);
  }
  assertHostedJob(startPrerelease, "start-prerelease");

  assertQemuBeforeBuildx(preview, "build");
  assertQemuBeforeBuildx(prereleaseImage, "publish");
  assertQemuBeforeBuildx(demoImage, "publish-demo");
  const upgraded = structuredClone(prereleaseImage);
  for (const step of upgraded.jobs.publish.steps) {
    step.name = "Renamed image step";
    if (step.uses) step.uses = step.uses.split("@")[0] + "@v99";
  }
  assertQemuBeforeBuildx(parseWorkflow(stringifyWorkflow(upgraded, { indent: 4, sortKeys: true })), "publish");
  upgraded.jobs.publish.steps.reverse();
  assert.throws(() => assertQemuBeforeBuildx(upgraded, "publish"));
});

test("PR preview workflow gates owner/admin and one-shot approval before digest handoff", async () => {
  const workflow = await readWorkflow("pr-test-image.yml");
  assert.deepEqual(new Set(workflow.on.pull_request_target.types), new Set(["opened", "synchronize", "reopened", "labeled"]));
  assert.equal(workflow.concurrency.group, "pr-test-image-${{ github.event.pull_request.number }}");
  assert.equal(workflow.concurrency["cancel-in-progress"], "${{ github.event.action == 'synchronize' }}");
  const scripts = Object.values(workflow.jobs).map(jobScripts).join("\n");
  assert.match(scripts, /authorPermission: process\.env\.AUTHOR_PERMISSION/);
  assert.match(scripts, /removeLabel/);
  assert.match(JSON.stringify(workflow), /Production image E2E/);
  assert.match(scripts, /digest="\$\(\.github\/scripts\/oci-image\.sh publish-preview/);
  const publish = jobScripts(workflowJob(workflow, "publish"));
  assert.match(publish, /pull\.state !== "open"/);
  assert.match(publish, /pull\.base\.ref !== "main"/);
  assert.match(publish, /pull\.head\.sha !== process\.env\.EXPECTED_HEAD/);
  assert.match(scripts, /printf 'image=%s@%s\\n'/);
  const productionE2E = workflowJob(workflow, "production-e2e");
  assert.equal(productionE2E.with.image, "${{ needs.publish.outputs.image }}");
  assert.equal(productionE2E.with.test_repository, undefined);
  assert.equal(productionE2E.with.test_revision, undefined);
  for (const obsolete of ["pull_request_author", "preview_run_id", "preview_run_attempt", "caller_workflow"]) {
    assert.doesNotMatch(JSON.stringify(workflow), new RegExp(obsolete));
  }
});

test("preview reporting comments only on successful publication and reports E2E by status", async () => {
  const workflow = await readWorkflow("pr-test-image.yml");
  const report = jobScripts(workflowJob(workflow, "report"));
  const reportE2E = jobScripts(workflowJob(workflow, "report-e2e"));
  const resolveFailure = jobScripts(workflowJob(workflow, "resolve-failure"));
  const statusCall = reportE2E.indexOf("github.rest.repos.createCommitStatus");
  const statusGuard = reportE2E.indexOf("if (imagePublished)");

  assert.match(reportE2E, /const imagePublished = process\.env\.BUILD_RESULT === "success"/);
  assert.ok(statusCall !== -1);
  assert.equal(statusGuard, -1);
  assert.equal((report.match(/github\.rest\.repos\.createCommitStatus/g) || []).length, 1);
  assert.match(report, /const succeeded = process\.env\.BUILD_RESULT === "success" &&\s+process\.env\.PUBLISH_RESULT === "success"/);
  assert.match(report, /state: succeeded \? "success" : "failure"/);
  assert.match(report, /context: process\.env\.POLICY_STATUS_CONTEXT/);
  assert.equal((reportE2E.match(/github\.rest\.repos\.createCommitStatus/g) || []).length, 1);
  assert.match(reportE2E, /const succeeded = imagePublished && \(sameRepository/);
  assert.match(reportE2E, /state: succeeded \? "success" : "failure"/);
  assert.match(reportE2E, /context: process\.env\.E2E_STATUS_CONTEXT/);
  assert.equal((resolveFailure.match(/github\.rest\.repos\.createCommitStatus/g) || []).length, 1);
  assert.match(resolveFailure, /state: "failure"/);
  assert.match(resolveFailure, /for \(const contextName of \[/);
  assert.doesNotMatch(reportE2E, /github\.rest\.issues\.createComment/);
  assert.doesNotMatch(resolveFailure, /github\.rest\.issues\.createComment/);
  assert.equal((Object.values(workflow.jobs).map(jobScripts).join("\n").match(/github\.rest\.issues\.createComment/g) || []).length, 1);
  const successStart = report.indexOf("if (succeeded) {");
  const successEnd = report.indexOf("\n}\n", successStart);
  assert.ok(successStart !== -1 && successEnd > successStart);
  const successBlock = report.slice(successStart, successEnd);
  assert.match(successBlock, /github\.rest\.issues\.createComment/);
  assert.doesNotMatch(report.slice(0, successStart), /github\.rest\.issues\.createComment/);
  assert.doesNotMatch(report.slice(successEnd), /github\.rest\.issues\.createComment/);
  assert.doesNotMatch(report, /Test image attempt failed/);
});

test("preview OCI cleanup is fenced to the current run and nonblocking", async () => {
  const workflow = await readWorkflow("pr-test-image.yml");
  const reportJob = workflowJob(workflow, "report");
  const report = jobScripts(reportJob);
  const cleanup = reportJob.steps.filter((step) => step.with?.script?.includes("github.rest.actions.deleteArtifact"));
  assert.equal(cleanup.length, 1);

  assert.equal(reportJob.permissions.actions, "write");
  assert.equal(cleanup[0].if, "always()");
  assert.equal(cleanup[0]["continue-on-error"], true);
  assert.deepEqual(new Set(reportJob.needs), new Set(["resolve", "build", "publish"]));
  assert.doesNotMatch(report, /production-e2e/);
  assert.match(report, /preview-oci-\$\{process\.env\.EXPECTED_HEAD\}-\$\{process\.env\.RUN_ATTEMPT\}/);
  assert.match(report, /listWorkflowRunArtifacts/);
  assert.match(report, /run_id: context\.runId/);
  assert.match(report, /matches = artifacts\.filter\(\(\{ name \}\) => name === artifactName\)/);
  const ambiguityGuard = report.indexOf("if (matches.length !== 1)");
  const ambiguityEnd = report.indexOf("\n  }\n", ambiguityGuard);
  const deleteCall = report.indexOf("github.rest.actions.deleteArtifact");
  assert.ok(ambiguityGuard !== -1 && ambiguityEnd > ambiguityGuard && ambiguityEnd < deleteCall);
  assert.equal((report.match(/github\.rest\.actions\.deleteArtifact/g) || []).length, 1);
  const runIdLookup = report.indexOf("run_id: context.runId");
  const exactNameFilter = report.indexOf("matches = artifacts.filter");
  assert.ok(runIdLookup !== -1 && exactNameFilter > runIdLookup && deleteCall > exactNameFilter);
  const ambiguityBlock = report.slice(ambiguityGuard, ambiguityEnd);
  assert.match(ambiguityBlock, /core\.warning/);
  assert.match(ambiguityBlock, /return;/);
  assert.doesNotMatch(ambiguityBlock, /deleteArtifact/);
  assert.match(report, /Skipping OCI handoff cleanup: found \$\{matches\.length\} artifacts named \$\{artifactName\}\./);
  assert.match(report, /deleteArtifact/);
  assert.match(report, /artifact_id: matches\[0\]\.id/);
  assert.match(report, /OCI handoff artifact cleanup failed/);
  assert.equal(Number(actionStep(workflow.jobs.build, "actions/upload-artifact").with["retention-days"]), 1);
});

test("OCI promotion avoids jq 1.6 reserved variable names", async () => {
  const script = await readFile(
    new URL("../../.github/scripts/oci-image.sh", import.meta.url),
    "utf8",
  );

  assert.doesNotMatch(script, /\bas \$label\b/);
});

test("OCI preview helpers preserve the manifest digest output contract", async () => {
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-oci-validate-"));
  const regctl = join(directory, "regctl");
  const digest = `sha256:${"a".repeat(64)}`;
  const headRevision = "b".repeat(40);
  const mainRevision = "c".repeat(40);
  const index = JSON.stringify({
    mediaType: "application/vnd.oci.image.index.v1+json",
    manifests: [
      { platform: { os: "linux", architecture: "amd64" }, digest },
      { platform: { os: "linux", architecture: "arm64" }, digest },
    ],
  });
  const labels = JSON.stringify({
    config: {
      Labels: {
        "org.opencontainers.image.version": "test-42",
        "org.opencontainers.image.revision": headRevision,
        "org.opencontainers.image.variant": "production",
        "io.dense-mem.preview.pr": "42",
        "io.dense-mem.preview.head": headRevision,
        "io.dense-mem.preview.main": mainRevision,
        "io.dense-mem.preview.run-id": "123",
        "io.dense-mem.preview.run-attempt": "1",
      },
    },
  });
  await writeFile(regctl, `#!/usr/bin/env bash
set -eo pipefail
if [[ "$1" == "manifest" && "$2" == "head" ]]; then
  if [[ "$STUB_REGCTL_HEAD_FAILURE" == "1" ]]; then
    printf '%s\n' 'stub manifest head failure' >&2
    exit 1
  fi
  if [[ "$3" == ghcr.io/* && -n "\${STUB_REGCTL_HEAD_FAILURE_STATE:-}" ]]; then
    count=0
    if [[ -f "$STUB_REGCTL_HEAD_FAILURE_STATE" ]]; then
      count="$(cat "$STUB_REGCTL_HEAD_FAILURE_STATE")"
    fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$STUB_REGCTL_HEAD_FAILURE_STATE"
    if [[ "$count" == "1" ]]; then
      printf '%s\n' 'stub transient manifest head failure' >&2
      exit 1
    fi
  fi
  if [[ "$3" == ghcr.io/* && -n "\${STUB_REGCTL_HEAD_STATE:-}" ]]; then
    count=0
    if [[ -f "$STUB_REGCTL_HEAD_STATE" ]]; then
      count="$(cat "$STUB_REGCTL_HEAD_STATE")"
    fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$STUB_REGCTL_HEAD_STATE"
    if [[ "$count" == "1" ]]; then
      printf '%s\n' "sha256:${"d".repeat(64)}"
      exit 0
    fi
  fi
  printf '%s\n' "$STUB_REGCTL_DIGEST"
  exit 0
fi
if [[ "$1" == "manifest" && "$2" == "get" ]]; then
  cat <<'JSON'
${index}
JSON
  exit 0
fi
if [[ "$1" == "image" && "$2" == "inspect" ]]; then
  cat <<'JSON'
${labels}
JSON
  exit 0
fi
if [[ "$1" == "image" && "$2" == "copy" ]]; then
  if [[ "$STUB_REGCTL_COPY_FAILURE" == "1" ]]; then
    printf '%s\n' 'stub image copy failure' >&2
    exit 1
  fi
  printf '%s\n' "$5"
  exit 0
fi
exit 2
`);
  await chmod(regctl, 0o755);

  const invoke = async (args, extraEnvironment = {}) => {
    try {
      const result = await execFileAsync(
        "bash",
        [ociImageScript, ...args],
        {
          env: {
            ...process.env,
            REGCTL_BIN: regctl,
            STUB_REGCTL_COPY_FAILURE: "0",
            STUB_REGCTL_DIGEST: digest,
            STUB_REGCTL_HEAD_FAILURE: "0",
            ...extraEnvironment,
          },
        },
      );
      return { status: 0, ...result };
    } catch (error) {
      return {
        status: error.code ?? 1,
        stdout: error.stdout ?? "",
        stderr: error.stderr ?? "",
      };
    }
  };

  try {
    const validateArgs = [
      "validate-preview",
      "ghcr.io/markhuangai/dense-mem:test-42",
      "42",
      headRevision,
      mainRevision,
      "123",
      "1",
    ];
    assert.equal((await invoke(validateArgs)).status, 0);
    const invalidDigest = await invoke(validateArgs, { STUB_REGCTL_DIGEST: "sha256:not-a-digest" });
    assert.notEqual(invalidDigest.status, 0);
    assert.match(invalidDigest.stderr, /invalid manifest digest/);
    const unavailable = await invoke(validateArgs, { STUB_REGCTL_HEAD_FAILURE: "1" });
    assert.notEqual(unavailable.status, 0);

    const publishArgs = [
      "publish-preview",
      join(directory, "layout"),
      "ghcr.io/markhuangai/dense-mem:test-42",
      "42",
      headRevision,
      mainRevision,
      "123",
      "1",
    ];
    const published = await invoke(publishArgs);
    assert.equal(published.status, 0);
    assert.equal(published.stdout, `${digest}\n`);

    const eventualHeadState = join(directory, "eventual-head-state");
    const eventuallyPublished = await invoke(publishArgs, { STUB_REGCTL_HEAD_STATE: eventualHeadState });
    assert.equal(eventuallyPublished.status, 0);
    assert.equal(eventuallyPublished.stdout, `${digest}\n`);
    assert.equal(await readFile(eventualHeadState, "utf8"), "2\n");

    const transientHeadFailureState = join(directory, "transient-head-failure-state");
    const publishedAfterTransientHeadFailure = await invoke(publishArgs, {
      STUB_REGCTL_HEAD_FAILURE_STATE: transientHeadFailureState,
    });
    assert.equal(publishedAfterTransientHeadFailure.status, 0);
    assert.equal(publishedAfterTransientHeadFailure.stdout, `${digest}\n`);
    assert.equal(await readFile(transientHeadFailureState, "utf8"), "2\n");

    const copyFailure = await invoke(publishArgs, { STUB_REGCTL_COPY_FAILURE: "1" });
    assert.notEqual(copyFailure.status, 0);
    assert.match(copyFailure.stderr, /stub image copy failure/);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("prerelease publication waits for the staging migration rehearsal", async () => {
  const [releaseWorkflow, rehearsalWorkflow, pushWorkflow] = await Promise.all([
    readWorkflow("release-rc.yml"), readWorkflow("staging-migration-rehearsal.yml"), readWorkflow("ci-push.yml"),
  ]);
  const classifier = workflowJob(releaseWorkflow, "classify-release");
  const rehearsalCall = workflowJob(releaseWorkflow, "staging-migration-rehearsal");
  const rehearsal = workflowJob(rehearsalWorkflow, "staging-migration-rehearsal");
  const prepare = workflowJob(releaseWorkflow, "prepare-prerelease");
  assert.equal(pushWorkflow.on.push["paths-ignore"], undefined);
  assert.equal(classifier["runs-on"], "ubuntu-latest");
  const checkout = actionStep(classifier, "actions/checkout");
  assert.equal(Number(checkout.with["fetch-depth"]), 0);
  assert.equal(String(checkout.with["persist-credentials"]), "false");
  assert.equal(checkout.with.ref, "${{ github.event.workflow_run.head_sha }}");
  assert.match(jobScripts(classifier), /prerelease-version\.sh should-release "\$\{HEAD_SHA\}"/);
  assert.deepEqual([rehearsalCall.needs].flat(), ["classify-release"]);
  assert.match(rehearsalCall.if, /needs\.classify-release\.outputs\.should_release == 'true'/);
  assert.equal(rehearsalCall.uses, "./.github/workflows/staging-migration-rehearsal.yml");
  assert.equal(rehearsalCall.with.revision, "${{ github.event.workflow_run.head_sha }}");
  assert.equal(rehearsalCall.secrets, "inherit");
  assert.doesNotMatch(JSON.stringify(rehearsalCall), /PGVECTOR_STAGE_DSN|sync\.sh|go test/);
  assert.deepEqual(new Set(rehearsal["runs-on"]), new Set(["home-server", "bash", "non-root"]));
  assert.equal(rehearsal["timeout-minutes"], 90);
  assert.equal(rehearsal.environment, "staging-migration");
  assert.equal(String(actionStep(rehearsal, "actions/checkout").with["persist-credentials"]), "false");
  const execution = runStep(rehearsal, "go test -tags=staging_rehearsal");
  assert.equal(execution.env.DENSE_MEM_STAGE_POSTGRES_DSN, "${{ secrets.PGVECTOR_STAGE_DSN }}");
  assert.doesNotMatch(JSON.stringify(rehearsal), /DENSE_MEM_STAGE_POSTGRES_HOST|DENSE_MEM_STAGE_POSTGRES_PORT/);
  const script = jobScripts(rehearsal);
  assert.match(script, /"\$\{HOME\}\/dense-mem-stage\/sync\.sh"/);
  assert.match(script, /flock --wait 600 9/);
  assert.match(script, /timeout --signal=TERM --kill-after=30s 20m/);
  assert.doesNotMatch(JSON.stringify(rehearsal), /\b(?:docker|testcontainers)\b/i);
  assert.match(script, /go test -tags=staging_rehearsal \.\/cmd\/internal\/migrationapp[\s\S]*-timeout=40m/);
  assert.ok(script.indexOf("timeout --signal=TERM") < script.indexOf("go test -tags=staging_rehearsal"));
  assert.deepEqual(new Set(prepare.needs), new Set(["classify-release", "staging-migration-rehearsal"]));
  assert.match(prepare.if, /needs\.classify-release\.outputs\.should_release == 'true'/);
  assert.match(prepare.if, /needs\.staging-migration-rehearsal\.result == 'success'/);
  for (const name of ["resolve-production-source", "publish-image", "promote-preview-image", "publish-demo-image"]) {
    assert.ok([workflowJob(releaseWorkflow, name).needs].flat().includes("prepare-prerelease"));
  }
});

test("staging migration rehearsal supports manual and reusable execution", async () => {
  const workflow = await readWorkflow("staging-migration-rehearsal.yml");
  const rehearsal = workflowJob(workflow, "staging-migration-rehearsal");
  for (const event of ["workflow_dispatch", "workflow_call"]) {
    assert.ok(Object.hasOwn(workflow.on, event));
  }
  assert.equal(workflow.on.workflow_call.inputs.revision.required, true);
  assert.equal(workflow.on.workflow_call.inputs.revision.type, "string");
  assert.doesNotMatch(JSON.stringify(workflow), /prepare-prerelease|publish-image/);
  for (const scope of [workflow, ...Object.values(workflow.jobs)]) {
    assert.notEqual(scope.permissions?.contents, "write");
  }
  assert.equal(actionStep(rehearsal, "actions/checkout").with.ref, "${{ inputs.revision || github.sha }}");
});

function assertStagingPolicy(workflow) {
  const authorize = workflowJob(workflow, "authorize");
  const deploy = workflowJob(workflow, "deploy-staging");
  assert.deepEqual(Object.keys(workflow.on), ["workflow_dispatch"]);
  assert.equal(workflow.on.workflow_dispatch.inputs.tag.required, true);
  assert.equal(workflow.on.workflow_dispatch.inputs.tag.type, "string");
  assert.equal(authorize["runs-on"], "ubuntu-latest");
  const script = jobScripts(authorize);
  assert.match(script, /const actor = "Z-M-Huang"/);
  assert.match(script, /context\.actor !== actor/);
  assert.match(script, /process\.env\.TRIGGERING_ACTOR !== actor/);
  assert.match(script, /refs\/heads\/main/);
  assert.match(script, /refs\/pull\/\$\{preview\[1\]\}\/head/);
  assert.match(script, /core\.setOutput\("revision", revision\)/);
  assert.match(script, /core\.setOutput\("tag", tag\)/);
  actionSteps(deploy, "actions/checkout");
  assert.doesNotMatch(JSON.stringify(workflow), /image-release-policy/);
  assert.deepEqual([deploy.needs].flat(), ["authorize"]);
  assert.ok(!Object.values(workflow.jobs).some((job) => job.uses === "./.github/workflows/staging-migration-rehearsal.yml"));
  assert.deepEqual(new Set(deploy["runs-on"]), new Set(["home-server", "bash", "non-root"]));
  assert.equal(deploy.environment, "staging-migration");
}

test("staging deployment is owner-gated and manual-only across checkout upgrades", async () => {
  const workflow = await readWorkflow("deploy-stage.yml");
  assertStagingPolicy(workflow);
  const upgraded = structuredClone(workflow);
  for (const step of upgraded.jobs["deploy-staging"].steps) {
    step.name = "Renamed deployment step";
    if (step.uses) step.uses = step.uses.split("@")[0] + "@v99";
  }
  assertStagingPolicy(parseWorkflow(stringifyWorkflow(upgraded, { indent: 4, sortKeys: true, forceQuotes: true })));
  const invalid = structuredClone(workflow);
  invalid.on.push = { branches: ["main"] };
  assert.throws(() => assertStagingPolicy(invalid));
});

test("staging deployment shares source verification and pinned healthy startup", async () => {
  const [workflow, helper, imagePolicy] = await Promise.all([
    readWorkflow("deploy-stage.yml"),
    readFile(new URL("../../.github/scripts/deploy-staging-image.sh", import.meta.url), "utf8"),
    readFile(new URL("../../.github/scripts/staging-image.sh", import.meta.url), "utf8"),
  ]);
  const deployJob = workflowJob(workflow, "deploy-staging");
  const deploy = jobScripts(deployJob);
  assert.equal(workflow.permissions.packages, "read");
  for (const scope of [workflow, ...Object.values(workflow.jobs)]) {
    for (const permission of ["actions", "pull-requests", "statuses"]) assert.equal(scope.permissions?.[permission], undefined);
  }
  assert.deepEqual([deployJob.needs].flat(), ["authorize"]);
  assert.equal(runStep(deployJob, "docker login").env.DENSE_MEM_STAGE_POSTGRES_DSN, "${{ secrets.PGVECTOR_STAGE_DSN }}");
  assert.match(deploy, /git -C \.staging-image-source rev-parse HEAD/);
  assert.match(deploy, /deploy-staging-copy\.sh/);
  assert.doesNotMatch(Object.values(workflow.jobs).map(jobScripts).join("\n"), /go test/);
  assert.match(deploy, /deployment_script="\$\{HOME\}\/dense-mem-stage\/deploy-staging-copy\.sh"/);
  assert.doesNotMatch(deploy, /stop-staging-server\.sh/);
  assert.match(deploy, /Install the host-only staging deployment script before deploying/);
  assert.match(deploy, /exec "\$\{deployment_script\}"/);
  assert.match(helper, /source .*staging-image\.sh/);
  assert.match(helper, /migration-rehearsal\.lock/);
  assert.match(helper, /flock --wait 600 9/);
  assert.match(imagePolicy, /org\.opencontainers\.image\.revision/);
  assert.match(imagePolicy, /org\.opencontainers\.image\.variant/);
  assert.match(imagePolicy, /docker pull "\$image"/);
  assert.match(imagePolicy, /export DENSE_MEM_IMAGE="\$\{image_tag\}@\$\{image_digest\}"/);
  assert.match(helper, /docker compose up -d --pull always --no-deps --wait --wait-timeout 3000 server/);
  assert.doesNotMatch(helper, /docker compose logs/);
});

test("manual staging deployment budget leaves time for image transfer and failure cleanup", async () => {
  const helper = await readFile(new URL("../../.github/scripts/deploy-staging-image.sh", import.meta.url), "utf8");
  const lockWait = Number(helper.match(/flock --wait (\d+) 9/)[1]);
  const startupWait = Number(helper.match(/--wait-timeout (\d+) server/)[1]);
  const cleanupWait = Number(helper.match(/docker compose stop --timeout (\d+) server/)[1]);
  const cleanupDeadline = helper.match(/timeout --signal=TERM --kill-after=(\d+)s (\d+)s docker compose stop --timeout \d+ server/);
  assert.ok(cleanupDeadline, "cleanup must have its own bounded process group");
  const cleanupGraceSeconds = Number(cleanupDeadline[1]);
  const cleanupSeconds = Number(cleanupDeadline[2]);
  assert.ok(cleanupSeconds > cleanupWait, "cleanup must retain the configured container stop wait");
  const job = "deploy-staging";
  const workflow = await readWorkflow("deploy-stage.yml");
  const definition = workflowJob(workflow, job);
  const execution = runStep(definition, "docker login");
  const source = execution.run;
  const deadline = source.match(/timeout --signal=TERM --kill-after=(\d+)s --verbose (\d+)m bash -euo pipefail <<'DEPLOY'/);
  assert.ok(deadline, `${job} must bound the entire login and deployment operation`);
  const graceSeconds = Number(deadline[1]);
  const operationSeconds = Number(deadline[2]) * 60;
  const preparation = definition.steps.slice(0, definition.steps.indexOf(execution));
  for (const step of [...actionSteps(definition, "actions/checkout"), actionStep(definition, "actions/setup-go")]) {
    assert.ok(preparation.includes(step), "checkout and Go setup must precede deployment");
  }
  for (const step of preparation) assert.ok(Number(step["timeout-minutes"]) > 0, "preparation must be bounded");
  const preparationSeconds = preparation.reduce((sum, step) => sum + Number(step["timeout-minutes"]) * 60, 0);
  const jobSeconds = Number(definition["timeout-minutes"]) * 60;
  assert.ok(operationSeconds > lockWait + startupWait, `${job} must retain its configured lock and startup waits`);
  assert.ok(graceSeconds > cleanupSeconds + cleanupGraceSeconds, `${job} must allow bounded candidate cleanup before forced termination`);
  assert.ok(jobSeconds > preparationSeconds + operationSeconds + graceSeconds, `${job} must reserve time outside the bounded deployment operation`);
  assert.ok(source.indexOf(deadline[0]) < source.indexOf("docker login"));
  assert.ok(source.indexOf("docker login") < source.indexOf('exec "${deployment_script}"'));
  await execFileAsync("bash", ["-n", "-c", execution.run]);
});

test("owner-admin PR pushes run automatically without an approval label", () => {
  assert.deepEqual(
    decidePreviewEvent({
      ...baseEvent,
      pullRequestAuthor: "Z-M-Huang",
      pullRequestAuthorPermission: "admin",
    }),
    { mode: "attempt", reason: "owner_admin_pr" },
  );
});

test("preview resolver falls back to GraphQL after a too-many-files REST response", async () => {
  const headSHA = "a".repeat(40);
  const error = Object.assign(new Error("Request failed"), {
    status: 422,
    response: { data: { message: "The request could not be processed because too many files changed" } },
  });
  let graphQLCalled = false;
  const resolved = await resolvePreviewAttempt({
    github: {
      rest: { pulls: { get: async () => { throw error; } } },
      graphql: async (_query, variables) => {
        graphQLCalled = true;
        assert.deepEqual(variables, { owner: "markhuangai", repo: "dense-mem", number: 42, labelCursor: null });
        return {
          repository: {
            pullRequest: {
              number: 42,
              state: "OPEN",
              baseRefName: "main",
              headRefOid: headSHA,
              author: { login: "Z-M-Huang" },
              headRepository: { nameWithOwner: "markhuangai/dense-mem" },
              labels: {
                nodes: [{ name: "deploy-test-image" }],
                pageInfo: { hasNextPage: false, endCursor: null },
              },
            },
          },
        };
      },
    },
    context: {
      repo: { owner: "markhuangai", repo: "dense-mem" },
      payload: { action: "synchronize", pull_request: { number: 42, head: { sha: headSHA } } },
    },
    actorPermission: "admin",
    authorPermission: "admin",
  });

  assert.equal(graphQLCalled, true);
  assert.deepEqual(resolved, {
    mode: "attempt",
    reason: "owner_admin_pr",
    removeLabel: true,
    pullNumber: 42,
    headSha: headSHA,
    headRepository: "markhuangai/dense-mem",
  });
});

test("preview resolver follows GraphQL label pages after a too-many-files REST response", async () => {
  const headSHA = "c".repeat(40);
  const error = Object.assign(new Error("Request failed"), {
    status: 422,
    response: { data: { message: "The request could not be processed because too many files changed" } },
  });
  const cursors = [];
  const resolved = await resolvePreviewAttempt({
    github: {
      rest: { pulls: { get: async () => { throw error; } } },
      graphql: async (query, variables) => {
        assert.match(query, /labels\(first: 100, after: \$labelCursor\)/);
        cursors.push(variables.labelCursor);
        const labels = variables.labelCursor === null
          ? {
              nodes: Array.from({ length: 100 }, (_, index) => ({ name: `other-label-${index}` })),
              pageInfo: { hasNextPage: true, endCursor: "labels-page-1" },
            }
          : {
              nodes: [{ name: "deploy-test-image" }],
              pageInfo: { hasNextPage: false, endCursor: null },
            };
        return {
          repository: {
            pullRequest: {
              number: 42,
              state: "OPEN",
              baseRefName: "main",
              headRefOid: headSHA,
              author: { login: "Z-M-Huang" },
              headRepository: { nameWithOwner: "markhuangai/dense-mem" },
              labels,
            },
          },
        };
      },
    },
    context: {
      repo: { owner: "markhuangai", repo: "dense-mem" },
      payload: { action: "synchronize", pull_request: { number: 42, head: { sha: headSHA } } },
    },
    actorPermission: "admin",
    authorPermission: "admin",
  });

  assert.deepEqual(cursors, [null, "labels-page-1"]);
  assert.deepEqual(resolved, {
    mode: "attempt",
    reason: "owner_admin_pr",
    removeLabel: true,
    pullNumber: 42,
    headSha: headSHA,
    headRepository: "markhuangai/dense-mem",
  });
});

test("preview resolver does not mask unrelated pull-request API failures", async () => {
  const error = Object.assign(new Error("service unavailable"), { status: 503 });
  let graphQLCalled = false;

  await assert.rejects(
    resolvePreviewAttempt({
      github: {
        rest: { pulls: { get: async () => { throw error; } } },
        graphql: async () => {
          graphQLCalled = true;
          return null;
        },
      },
      context: {
        repo: { owner: "markhuangai", repo: "dense-mem" },
        payload: { action: "synchronize", pull_request: { number: 42, head: { sha: "b".repeat(40) } } },
      },
      actorPermission: "admin",
      authorPermission: "admin",
    }),
    /service unavailable/,
  );
  assert.equal(graphQLCalled, false);
});

test("non-owner pushes wait for a fresh approval", () => {
  assert.deepEqual(decidePreviewEvent(baseEvent), {
    mode: "skipped",
    reason: "approval_required",
  });
});

test("an admin-applied label starts a preview attempt and is consumed", () => {
  assert.deepEqual(
    decidePreviewEvent({
      ...baseEvent,
      action: "labeled",
      eventLabel: "deploy-test-image",
      actorPermission: "admin",
      hasPreviewLabel: true,
    }),
    { mode: "attempt", reason: "admin_label", removeLabel: true },
  );
});

test("a non-admin label is removed without authorizing a build", () => {
  assert.deepEqual(
    decidePreviewEvent({
      ...baseEvent,
      action: "labeled",
      eventLabel: "deploy-test-image",
      actorPermission: "read",
      hasPreviewLabel: true,
    }),
    {
      mode: "skipped",
      reason: "deploy-test-image approval requires a repository admin",
      removeLabel: true,
    },
  );
});

test("irrelevant label events do not replace an existing policy status", () => {
  assert.deepEqual(
    decidePreviewEvent({
      ...baseEvent,
      action: "labeled",
      eventLabel: "documentation",
    }),
    { mode: "noop", reason: "irrelevant_label" },
  );
});

test("reruns for an obsolete head do not touch the current head", () => {
  assert.deepEqual(
    decidePreviewEvent({ ...baseEvent, triggerHeadMatches: false }),
    { mode: "noop", reason: "stale_event" },
  );
});

test("main containment accepts only ahead or identical comparisons", () => {
  assert.equal(compareContainsMain("ahead"), true);
  assert.equal(compareContainsMain("identical"), true);
  assert.equal(compareContainsMain("behind"), false);
  assert.equal(compareContainsMain("diverged"), false);
});

test("RC selection requires exactly one PR merged as the main commit", () => {
  const pull = {
    number: 42,
    merged_at: "2026-08-12T00:00:00Z",
    merge_commit_sha: "main-sha",
    base: { ref: "main" },
  };
  assert.equal(selectMergedPull([pull], "main-sha").pull, pull);
  assert.equal(selectMergedPull([], "main-sha").pull, null);
  assert.equal(selectMergedPull([pull, { ...pull, number: 43 }], "main-sha").pull, null);
});

test("policy receipts bind the PR, run, attempt, and run URL", () => {
  const receipt = parseSuccessfulPolicyStatus({
    statuses: [
      {
        id: 2,
        context: "PR test image policy",
        state: "success",
        description: "Published test-42 (run 123456, attempt 2).",
        target_url: "https://github.com/markhuangai/dense-mem/actions/runs/123456",
      },
    ],
    pullNumber: 42,
    serverUrl: "https://github.com",
    repository: "markhuangai/dense-mem",
  });

  assert.deepEqual(receipt.status, {
    runId: "123456",
    runAttempt: "2",
    targetUrl: "https://github.com/markhuangai/dense-mem/actions/runs/123456",
  });
});

test("a newer failed policy status invalidates an older success", () => {
  const receipt = parseSuccessfulPolicyStatus({
    statuses: [
      {
        id: 1,
        context: "PR test image policy",
        state: "success",
        description: "Published test-42 (run 123456, attempt 1).",
        target_url: "https://github.com/markhuangai/dense-mem/actions/runs/123456",
      },
      {
        id: 2,
        context: "PR test image policy",
        state: "failure",
        description: "Preview publication failed.",
        target_url: "https://github.com/markhuangai/dense-mem/actions/runs/123457",
      },
    ],
    pullNumber: 42,
    serverUrl: "https://github.com",
    repository: "markhuangai/dense-mem",
  });

  assert.equal(receipt.status, null);
  assert.match(receipt.reason, /failure/);
});

test("policy receipts reject mismatched PRs, run URLs, and descriptions", () => {
  const base = {
    id: 2,
    context: "PR test image policy",
    state: "success",
    description: "Published test-42 (run 123456, attempt 2).",
    target_url: "https://github.com/markhuangai/dense-mem/actions/runs/123456",
  };
  const input = {
    statuses: [base],
    pullNumber: 42,
    serverUrl: "https://github.com",
    repository: "markhuangai/dense-mem",
  };

  assert.equal(
    parseSuccessfulPolicyStatus({
      ...input,
      statuses: [{ ...base, description: base.description.replace("test-42", "test-43") }],
    }).status,
    null,
  );
  assert.match(
    parseSuccessfulPolicyStatus({
      ...input,
      statuses: [{ ...base, target_url: "https://example.invalid/run/123456" }],
    }).reason,
    /run URL is invalid/,
  );
  assert.equal(
    parseSuccessfulPolicyStatus({
      ...input,
      statuses: [{ ...base, description: "Published test-42." }],
    }).status,
    null,
  );
});

test("deleted pull-request repositories retain the event head for later validation", () => {
  const headSha = "a".repeat(40);
  assert.deepEqual(
    policy.resolvePullRequestEvent(
      {
        action: "synchronize",
        pull_request: {
          number: 42,
          head: { sha: headSha, repo: null },
          base: { repo: { full_name: "markhuangai/dense-mem" } },
        },
      },
      "write",
    ),
    {
      action: "synchronize",
      eventLabel: "",
      triggerHead: headSha,
      pullNumber: 42,
      actorPermission: "write",
    },
  );
});

test("RC reuse API policy requires the trusted preview run", () => {
  const pull = { number: 42 };
  const policyStatus = { runId: "123456", runAttempt: "2" };
  const workflowRun = {
    id: 123456,
    run_attempt: 2,
    event: "pull_request_target",
    conclusion: "success",
    path: ".github/workflows/pr-test-image.yml",
    display_title: "PR test image: PR #42",
  };
  const input = {
    pull,
    policyStatus,
    workflowRun,
  };

  assert.deepEqual(decideRcPreview(input), { eligible: true, reason: "" });
  assert.equal(
    decideRcPreview({
      ...input,
      workflowRun: { ...workflowRun, conclusion: "failure" },
    }).eligible,
    false,
  );
});
