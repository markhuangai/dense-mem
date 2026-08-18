import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import policy from "../../.github/scripts/image-release-policy.cjs";

const {
  compareContainsMain,
  decidePreviewEvent,
  decideRcPreview,
  parseSuccessfulPolicyStatus,
  selectMergedPull,
} = policy;

const baseEvent = {
  action: "synchronize",
  eventLabel: "",
  hasPreviewLabel: true,
  isFork: false,
  triggerHeadMatches: true,
  actorPermission: "write",
};

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

test("prerelease publication waits for the staging migration rehearsal", async () => {
  const workflow = await readFile(
    new URL("../../.github/workflows/release-rc.yml", import.meta.url),
    "utf8",
  );
  const rehearsal = workflowJob(workflow, "staging-migration-rehearsal");
  const prepare = workflowJob(workflow, "prepare-prerelease");

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
  assert.match(prepare, /^    needs: staging-migration-rehearsal$/m);
  assert.match(prepare, /needs\.staging-migration-rehearsal\.result == 'success'/);

  for (const jobName of [
    "resolve-production-source",
    "publish-image",
    "promote-preview-image",
    "publish-demo-image",
  ]) {
    assert.match(
      workflowJob(workflow, jobName),
      /(?:^    needs: prepare-prerelease$|^      - prepare-prerelease$)/m,
      `${jobName} must remain downstream of prepare-prerelease`,
    );
  }
});

test("published images require the populated migration gate before building", async () => {
  for (const filename of ["publish-image.yml", "publish-demo-image.yml"]) {
    const workflow = await readFile(
      new URL(`../../.github/workflows/${filename}`, import.meta.url),
      "utf8",
    );
    const gate = workflow.indexOf("run: scripts/pre-image-check.sh HEAD^");
    const build = workflow.indexOf("uses: docker/build-push-action@v7");
    assert.ok(gate >= 0, `${filename} is missing the populated migration gate`);
    assert.ok(build > gate, `${filename} builds before the populated migration gate`);
  }
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
