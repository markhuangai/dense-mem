import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { test } from "node:test";
import { promisify } from "node:util";
import policy from "../../.github/scripts/image-release-policy.cjs";

const {
  compareContainsMain,
  decideAutomaticPreview,
  decideLabelApproval,
  decidePreviewEvent,
  decideRcPreview,
  parseSuccessfulPolicyStatus,
  selectMergedPull,
  validatePinnedProductionImageReference,
  validateProductionImageReference,
} = policy;
const execFileAsync = promisify(execFile);

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

function workflowJob(workflow, name) {
  const marker = `  ${name}:`;
  const start = workflow.indexOf(marker);
  assert.notEqual(start, -1, `workflow job ${name} is missing`);

  const remainder = workflow.slice(start + marker.length);
  const nextJob = remainder.search(/\n  [a-z0-9-]+:\n/);
  return nextJob === -1 ? remainder : remainder.slice(0, nextJob);
}

test("low-trust preview builds do not export an Actions cache", async () => {
  const workflow = await readFile(
    new URL("../../.github/workflows/pr-test-image.yml", import.meta.url),
    "utf8",
  );

  assert.match(workflow, /^\s+pull_request_target:/m);
  assert.doesNotMatch(workflow, /^\s+cache-to:/m);
});

test("PR preview workflow gates owner/admin and one-shot approval before digest handoff", async () => {
  const workflow = await readFile(
    new URL("../../.github/workflows/pr-test-image.yml", import.meta.url),
    "utf8",
  );
  assert.match(workflow, /types: \[opened, synchronize, reopened, labeled\]/);
  assert.doesNotMatch(workflow, /unlabeled/);
  assert.match(workflow, /group: pr-test-image-\$\{\{ github\.event\.pull_request\.number \}\}/);
  assert.match(workflow, /cancel-in-progress: \$\{\{ github\.event\.action == 'synchronize' \}\}/);
  assert.match(workflow, /authorPermission: process\.env\.AUTHOR_PERMISSION/);
  assert.match(workflow, /removeLabel/);
  assert.match(workflow, /Production image E2E/);
  assert.match(workflow, /digest="\$\(\.github\/scripts\/oci-image\.sh publish-preview/);
  assert.match(workflow, /printf 'image=%s@%s\\n'/);
  const productionE2E = workflowJob(workflow, "production-e2e");
  assert.match(productionE2E, /image: \$\{\{ needs\.publish\.outputs\.image \}\}/);
  assert.doesNotMatch(productionE2E, /test_repository|test_revision/);
  for (const obsolete of [
    "pull_request_author",
    "preview_run_id",
    "preview_run_attempt",
    "caller_workflow",
  ]) {
    assert.doesNotMatch(workflow, new RegExp(obsolete));
  }
});

test("OCI promotion avoids jq 1.6 reserved variable names", async () => {
  const script = await readFile(
    new URL("../../.github/scripts/oci-image.sh", import.meta.url),
    "utf8",
  );

  assert.doesNotMatch(script, /\bas \$label\b/);
});

test("prerelease publication waits for the staging migration rehearsal", async () => {
  const [releaseWorkflow, rehearsalWorkflow, pushWorkflow] = await Promise.all([
    readFile(
      new URL("../../.github/workflows/release-rc.yml", import.meta.url),
      "utf8",
    ),
    readFile(
      new URL(
        "../../.github/workflows/staging-migration-rehearsal.yml",
        import.meta.url,
      ),
      "utf8",
    ),
    readFile(
      new URL("../../.github/workflows/ci-push.yml", import.meta.url),
      "utf8",
    ),
  ]);
  const classifier = workflowJob(releaseWorkflow, "classify-release");
  const rehearsalCall = workflowJob(
    releaseWorkflow,
    "staging-migration-rehearsal",
  );
  const rehearsal = workflowJob(
    rehearsalWorkflow,
    "staging-migration-rehearsal",
  );
  const prepare = workflowJob(releaseWorkflow, "prepare-prerelease");

  assert.doesNotMatch(pushWorkflow, /paths-ignore/);
  assert.match(classifier, /^    runs-on: docker-runner$/m);
  assert.match(classifier, /^          fetch-depth: 0$/m);
  assert.match(classifier, /^          persist-credentials: false$/m);
  assert.match(
    classifier,
    /^          ref: \$\{\{ github\.event\.workflow_run\.head_sha \}\}$/m,
  );
  assert.match(
    classifier,
    /prerelease-version\.sh should-release "\$\{HEAD_SHA\}"/,
  );
  assert.match(
    rehearsalCall,
    /^    needs: classify-release$[\s\S]*needs\.classify-release\.outputs\.should_release == 'true'/m,
  );
  assert.match(
    rehearsalCall,
    /^    uses: \.\/\.github\/workflows\/staging-migration-rehearsal\.yml$/m,
  );
  assert.match(
    rehearsalCall,
    /^      revision: \$\{\{ github\.event\.workflow_run\.head_sha \}\}$/m,
  );
  assert.match(rehearsalCall, /^    secrets: inherit$/m);
  assert.doesNotMatch(rehearsalCall, /PGVECTOR_STAGE_DSN|sync\.sh|go test/);
  assert.match(rehearsal, /^    runs-on: \[home-server, bash, non-root\]$/m);
  assert.match(rehearsal, /^    timeout-minutes: 75$/m);
  assert.match(rehearsal, /^    environment: staging-migration$/m);
  assert.match(rehearsal, /persist-credentials: false/);
  assert.match(
    rehearsal,
    /^          DENSE_MEM_STAGE_POSTGRES_DSN: \$\{\{ secrets\.PGVECTOR_STAGE_DSN \}\}$/m,
  );
  assert.doesNotMatch(rehearsal, /DENSE_MEM_STAGE_POSTGRES_HOST/);
  assert.doesNotMatch(rehearsal, /DENSE_MEM_STAGE_POSTGRES_PORT/);
  assert.match(rehearsal, /"\$\{HOME\}\/dense-mem-stage\/sync\.sh"/);
  assert.match(rehearsal, /flock --wait 600 9/);
  assert.match(rehearsal, /timeout --signal=TERM --kill-after=30s 10m/);
  assert.doesNotMatch(rehearsal, /\b(?:docker|testcontainers)\b/i);
  assert.match(
    rehearsal,
    /go test -tags=staging_rehearsal \.\/cmd\/internal\/migrationapp[\s\S]*-timeout=40m/,
  );
  assert.ok(
    rehearsal.indexOf("timeout --signal=TERM") <
      rehearsal.indexOf("go test -tags=staging_rehearsal"),
    "the production copy must finish restoring before migrations run",
  );
  assert.match(
    prepare,
    /^    needs:\n      - classify-release\n      - staging-migration-rehearsal$/m,
  );
  assert.match(prepare, /needs\.classify-release\.outputs\.should_release == 'true'/);
  assert.match(prepare, /needs\.staging-migration-rehearsal\.result == 'success'/);

  for (const jobName of [
    "resolve-production-source",
    "publish-image",
    "promote-preview-image",
    "publish-demo-image",
  ]) {
    assert.match(
      workflowJob(releaseWorkflow, jobName),
      /(?:^    needs: prepare-prerelease$|^      - prepare-prerelease$)/m,
      `${jobName} must remain downstream of prepare-prerelease`,
    );
  }
});

test("staging migration rehearsal supports manual and reusable execution", async () => {
  const workflow = await readFile(
    new URL(
      "../../.github/workflows/staging-migration-rehearsal.yml",
      import.meta.url,
    ),
    "utf8",
  );
  const rehearsal = workflowJob(workflow, "staging-migration-rehearsal");

  assert.match(workflow, /^  workflow_dispatch:$/m);
  assert.match(workflow, /^  workflow_call:$/m);
  assert.match(
    workflow,
    /^      revision:\n        description: "Commit, branch, or tag to rehearse\."\n        required: true\n        type: string$/m,
  );
  assert.doesNotMatch(workflow, /prepare-prerelease|publish-image|contents: write/);
  assert.match(
    rehearsal,
    /^          ref: \$\{\{ inputs\.revision \|\| github\.sha \}\}$/m,
  );
});

test("staging deployment is owner-gated and manual-only", async () => {
  const workflow = await readFile(
    new URL("../../.github/workflows/deploy-stage.yml", import.meta.url),
    "utf8",
  );
  const authorize = workflowJob(workflow, "authorize");
  const rehearsal = workflowJob(workflow, "staging-migration-rehearsal");
  const deploy = workflowJob(workflow, "deploy-staging");

  assert.match(workflow, /^  workflow_dispatch:\n    inputs:/m);
  assert.match(
    workflow,
    /^      tag:\n        description: "Dense-Mem image tag to deploy\."\n        required: true\n        type: string$/m,
  );
  assert.doesNotMatch(
    workflow,
    /^  (?:pull_request|pull_request_target|push|workflow_call|workflow_run):/m,
  );
  assert.match(authorize, /^    runs-on: docker-runner$/m);
  assert.match(authorize, /const actor = "Z-M-Huang"/);
  assert.match(authorize, /context\.actor !== actor/);
  assert.match(authorize, /process\.env\.TRIGGERING_ACTOR !== actor/);
  assert.match(authorize, /refs\/heads\/main/);
  assert.match(authorize, /refs\/pull\/\$\{preview\[1\]\}\/head/);
  assert.match(authorize, /core\.setOutput\("revision", revision\)/);
  assert.match(authorize, /core\.setOutput\("tag", tag\)/);
  assert.doesNotMatch(workflow, /actions\/checkout|image-release-policy/);
  assert.match(rehearsal, /^    needs: authorize$/m);
  assert.match(
    rehearsal,
    /^    uses: \.\/\.github\/workflows\/staging-migration-rehearsal\.yml$/m,
  );
  assert.match(
    rehearsal,
    /^      revision: \$\{\{ needs\.authorize\.outputs\.revision \}\}$/m,
  );
  assert.match(rehearsal, /^    secrets: inherit$/m);
  assert.match(
    deploy,
    /^    needs: \[authorize, staging-migration-rehearsal\]$/m,
  );
  assert.match(deploy, /^    runs-on: \[home-server, bash, non-root\]$/m);
  assert.match(deploy, /^    environment: staging$/m);
});

test("staging deployment rehearses before an always-pull healthy startup", async () => {
  const workflow = await readFile(
    new URL("../../.github/workflows/deploy-stage.yml", import.meta.url),
    "utf8",
  );
  const rehearsal = workflowJob(workflow, "staging-migration-rehearsal");
  const deploy = workflowJob(workflow, "deploy-staging");

  assert.match(workflow, /^  packages: read$/m);
  assert.doesNotMatch(workflow, /^  (?:actions|pull-requests|statuses): read$/m);
  assert.match(rehearsal, /^    needs: authorize$/m);
  assert.match(deploy, /needs\.staging-migration-rehearsal\.result == 'success'/);
  assert.doesNotMatch(workflow, /sync_script|sync\.sh|go test/);
  assert.match(deploy, /migration-rehearsal\.lock/);
  assert.match(deploy, /flock --wait 600 9/);
  assert.match(
    deploy,
    /IMAGE_TAG: \$\{\{ needs\.authorize\.outputs\.tag \}\}/,
  );
  assert.match(
    deploy,
    /DENSE_MEM_IMAGE="\$\{IMAGE_TAG\}"[\s\\]*docker compose up -d/,
  );
  assert.doesNotMatch(deploy, /DENSE_MEM_IMAGE="ghcr\.io\/markhuangai\/dense-mem:/);
  assert.doesNotMatch(deploy, /DENSE_MEM_IMAGE_TAG/);
  assert.match(deploy, /docker compose up -d[\s\\]*--pull always/);
  assert.match(deploy, /--pull always[\s\\]*--no-deps/);
  assert.match(deploy, /--wait[\s\\]*--wait-timeout 3000/);
  assert.doesNotMatch(deploy, /EXPECTED_REVISION|org\.opencontainers\.image\.revision/);
  assert.doesNotMatch(deploy, /docker (?:image )?pull|docker compose pull|--pull never/);
  assert.doesNotMatch(deploy, /docker compose logs/);
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
