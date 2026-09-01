import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import policy from "../../.github/scripts/image-release-policy.cjs";

const {
  compareContainsMain,
  decideAutomaticProductionE2E,
  decideManualProductionE2E,
  decidePreviewEvent,
  decideRcPreview,
  parseSuccessfulPolicyStatus,
  selectMergedPull,
  validateProductionImageReference,
} = policy;

const baseEvent = {
  action: "synchronize",
  eventLabel: "",
  hasPreviewLabel: true,
  isFork: false,
  triggerHeadMatches: true,
  actorPermission: "write",
};

test("manual production E2E authorization is owner and main gated", () => {
  const allowed = decideManualProductionE2E({
    actor: "Z-M-Huang",
    triggeringActor: "Z-M-Huang",
    ref: "refs/heads/main",
    image: "ghcr.io/markhuangai/dense-mem:v2.6.1",
    repository: "markhuangai/dense-mem",
  });
  assert.equal(allowed.authorized, true);
  assert.equal(
    decideManualProductionE2E({
      actor: "other",
      triggeringActor: "Z-M-Huang",
      ref: "refs/heads/main",
      image: "ghcr.io/markhuangai/dense-mem:v2.6.1",
      repository: "markhuangai/dense-mem",
    }).authorized,
    false,
  );
});

test("manual production E2E rejects malformed image, trigger, and workflow inputs", () => {
  const input = {
    actor: "Z-M-Huang",
    triggeringActor: "Z-M-Huang",
    ref: "refs/heads/main",
    image: "ghcr.io/markhuangai/dense-mem:v2.6.1",
    repository: "markhuangai/dense-mem",
  };
  for (const [label, overrides, reason] of [
    ["image", { image: "ghcr.io/other/project:v2.6.1" }, "the image is outside the Dense-Mem GHCR repository"],
    ["triggering actor", { triggeringActor: "other" }, "manual production E2E is restricted to the owner"],
    ["workflow ref", { ref: "refs/heads/feature" }, "manual production E2E must use the main workflow definition"],
  ]) {
    assert.deepEqual(
      decideManualProductionE2E({ ...input, ...overrides }),
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

test("automatic production E2E authorization fences PR, head, main, label, and receipt", () => {
  const input = {
    actor: "Z-M-Huang",
    pullRequestAuthor: "Z-M-Huang",
    pullRequestNumber: 42,
    pullRequestState: "open",
    pullRequestBase: "main",
    hasPreviewLabel: true,
    currentHead: "a".repeat(40),
    expectedHead: "a".repeat(40),
    currentMain: "b".repeat(40),
    expectedMain: "b".repeat(40),
    previewRunId: "123",
    previewRunAttempt: "1",
    image: "ghcr.io/markhuangai/dense-mem:test-42",
    repository: "markhuangai/dense-mem",
    workflowRun: {
      id: 123,
      run_attempt: 1,
      head_sha: "a".repeat(40),
      event: "pull_request_target",
      status: "in_progress",
      conclusion: null,
      path: ".github/workflows/pr-test-image.yml",
      display_title: "PR test image: PR #42",
    },
    publishJob: {
      name: "Publish trusted preview",
      run_id: 123,
      run_attempt: 1,
      head_sha: "a".repeat(40),
      status: "completed",
      conclusion: "success",
    },
  };
  assert.equal(decideAutomaticProductionE2E(input).authorized, true);
  assert.equal(decideAutomaticProductionE2E({ ...input, currentHead: "c".repeat(40) }).authorized, false);
  assert.equal(decideAutomaticProductionE2E({ ...input, hasPreviewLabel: false }).authorized, false);
  assert.equal(decideAutomaticProductionE2E({ ...input, publishJob: { ...input.publishJob, conclusion: "failure" } }).authorized, false);
  assert.equal(decideAutomaticProductionE2E({ ...input, workflowRun: { ...input.workflowRun, status: "completed", conclusion: "failure" } }).authorized, false);
  assert.equal(decideAutomaticProductionE2E({ ...input, workflowRun: { ...input.workflowRun, head_sha: "c".repeat(40) } }).authorized, false);
});

test("automatic production E2E rejects every authorization boundary", () => {
  const input = {
    actor: "Z-M-Huang",
    pullRequestAuthor: "Z-M-Huang",
    pullRequestNumber: 42,
    pullRequestState: "open",
    pullRequestBase: "main",
    hasPreviewLabel: true,
    currentHead: "a".repeat(40),
    expectedHead: "a".repeat(40),
    currentMain: "b".repeat(40),
    expectedMain: "b".repeat(40),
    previewRunId: "123",
    previewRunAttempt: "1",
    image: "ghcr.io/markhuangai/dense-mem:test-42",
    repository: "markhuangai/dense-mem",
    workflowRun: {
      id: 123,
      run_attempt: 1,
      head_sha: "a".repeat(40),
      event: "pull_request_target",
      status: "completed",
      conclusion: "success",
      path: ".github/workflows/pr-test-image.yml",
      display_title: "PR test image: PR #42",
    },
    publishJob: {
      name: "Publish trusted preview",
      run_id: 123,
      run_attempt: 1,
      head_sha: "a".repeat(40),
      status: "completed",
      conclusion: "success",
    },
  };
  for (const [label, overrides, reason] of [
    ["image", { image: "docker.io/example/dense-mem:latest" }, "the image is outside the Dense-Mem GHCR repository"],
    ["actor", { actor: "other" }, "automatic production E2E is restricted to the owner PR"],
    ["PR author", { pullRequestAuthor: "other" }, "automatic production E2E is restricted to the owner PR"],
    ["PR state", { pullRequestState: "closed" }, "the pull request is not open against main"],
    ["PR base", { pullRequestBase: "feature" }, "the pull request is not open against main"],
    ["receipt format", { previewRunId: "0" }, "the preview workflow receipt is invalid"],
    ["workflow attributes", { workflowRun: { ...input.workflowRun, event: "push" } }, "the preview workflow run receipt is invalid"],
    ["workflow completion", { workflowRun: { ...input.workflowRun, status: "in_progress", conclusion: "success" } }, "the preview workflow run receipt is invalid"],
    ["publish job attributes", { publishJob: { ...input.publishJob, status: "in_progress" } }, "the preview publication job receipt is invalid"],
  ]) {
    assert.deepEqual(
      decideAutomaticProductionE2E({ ...input, ...overrides }),
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

test("OCI promotion avoids jq 1.6 reserved variable names", async () => {
  const script = await readFile(
    new URL("../../.github/scripts/oci-image.sh", import.meta.url),
    "utf8",
  );

  assert.doesNotMatch(script, /\bas \$label\b/);
});

test("production image receipts require both platform production metadata", async () => {
  const [script, dockerfile] = await Promise.all([
    readFile(new URL("../../.github/scripts/oci-image.sh", import.meta.url), "utf8"),
    readFile(new URL("../../Dockerfile", import.meta.url), "utf8"),
  ]);
  assert.match(script, /production_receipt\(\)/);
  assert.match(script, /org\.opencontainers\.image\.variant.*production/);
  assert.match(script, /production-receipt/);
  assert.doesNotMatch(dockerfile, /FROM runtime-base AS e2e/);
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

test("same-repository pushes rebuild while the preview label remains", () => {
  assert.deepEqual(decidePreviewEvent(baseEvent), {
    mode: "attempt",
    reason: "requested",
  });
});

test("fork pushes are rejected while runner isolation is unavailable", () => {
  assert.deepEqual(
    decidePreviewEvent({ ...baseEvent, isFork: true }),
    {
      mode: "skipped",
      reason: "fork_isolation_unavailable",
      removeLabel: true,
    },
  );
});

test("unlabeled fork events remain ordinary no-preview requests", () => {
  assert.deepEqual(
    decidePreviewEvent({ ...baseEvent, isFork: true, hasPreviewLabel: false }),
    { mode: "skipped", reason: "label_absent" },
  );
});

test("a maintainer-applied fork label starts an attempt", () => {
  assert.deepEqual(
    decidePreviewEvent({
      ...baseEvent,
      action: "labeled",
      eventLabel: "deploy-test-image",
      isFork: true,
      actorPermission: "maintain",
      allowForkBuilds: true,
    }),
    { mode: "attempt", reason: "requested" },
  );
});

test("an untrusted fork label is removed", () => {
  assert.deepEqual(
    decidePreviewEvent({
      ...baseEvent,
      action: "labeled",
      eventLabel: "deploy-test-image",
      isFork: true,
      actorPermission: "read",
      allowForkBuilds: true,
    }),
    {
      mode: "skipped",
      reason: "fork_reapproval_required",
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

test("deleted fork repositories resolve as unbuildable fork events", () => {
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
      isFork: true,
      actorPermission: "write",
    },
  );
});

test("RC reuse API policy requires the retained label and trusted run", () => {
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
    hasPreviewLabel: true,
    policyStatus,
    workflowRun,
  };

  assert.deepEqual(decideRcPreview(input), { eligible: true, reason: "" });
  assert.equal(decideRcPreview({ ...input, hasPreviewLabel: false }).eligible, false);
  assert.equal(
    decideRcPreview({
      ...input,
      workflowRun: { ...workflowRun, conclusion: "failure" },
    }).eligible,
    false,
  );
});
