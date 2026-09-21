import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import policy from "../../.github/scripts/pr-image-cleanup.cjs";

const digest = (character) => {
  const seed = character.charCodeAt(0).toString(16);
  return `sha256:${(seed + seed[seed.length - 1].repeat(64)).slice(0, 64)}`;
};

test("preview tags and ownership labels are strict", () => {
  assert.equal(policy.testPrFromTag("test-42"), 42);
  assert.equal(policy.testPrFromTag("test-0"), null);
  assert.equal(policy.testPrFromTag("test-42-extra"), null);
  assert.equal(policy.labelsPreviewPr({
    "io.dense-mem.preview.pr": "42",
    "io.dense-mem.preview.head": "a".repeat(40),
    "io.dense-mem.preview.main": "b".repeat(40),
    "io.dense-mem.preview.run-id": "123",
    "io.dense-mem.preview.run-attempt": "1",
  }), 42);
  assert.equal(policy.labelsPreviewPr({ "io.dense-mem.preview.pr": "42" }), null);
});

test("package version identity validation fails closed", () => {
  assert.throws(
    () => policy.normalizeVersion({ id: "42", name: digest("a") }),
    /package version has an invalid id or digest/,
  );
  assert.throws(
    () => policy.normalizeVersion({ id: 42, name: "not-a-digest" }),
    /package version has an invalid id or digest/,
  );
});

test("closed unmerged PRs are eligible while merged PRs wait for an explicit release result", () => {
  const closed = { state: "closed", merged_at: null };
  assert.deepEqual(policy.cleanupEligibility({ pull: closed, release: null }), {
    eligible: true,
    reason: "closed without merge",
  });

  const merged = {
    state: "closed",
    merged_at: "2026-09-20T10:00:00Z",
    merge_commit_sha: "a".repeat(40),
  };
  assert.match(policy.cleanupEligibility({ pull: merged, release: null }).reason, /waiting/);
  const run = {
    name: "Release prerelease",
    head_sha: "f".repeat(40),
    display_title: `Release prerelease: ${merged.merge_commit_sha}`,
    conclusion: "success",
  };
  const jobs = [
    { name: "Classify release changes", conclusion: "success" },
    { name: "Prepare prerelease", conclusion: "success" },
    { name: "Promote preview image", conclusion: "success" },
  ];
  assert.deepEqual(policy.releaseOutcome({ run, jobs, mergeCommitSha: merged.merge_commit_sha }), {
    eligible: true,
    reason: "prerelease publication completed",
  });
  assert.equal(policy.releaseTargetSha(run), merged.merge_commit_sha);
  assert.equal(policy.cleanupEligibility({ pull: merged, release: { run, jobs } }).eligible, true);
  assert.deepEqual(policy.cleanupEligibility({
    pull: merged,
    release: { run: { ...run, conclusion: "failure" }, jobs: [] },
    releasedImage: true,
  }), {
    eligible: true,
    reason: "verified prerelease image metadata",
  });
});

test("released image metadata permits cleanup when the release receipt is not available", () => {
  const pull = {
    state: "closed",
    merged_at: "2026-09-20T10:00:00Z",
    merge_commit_sha: "a".repeat(40),
  };
  assert.deepEqual(policy.cleanupEligibility({ pull, release: null, releasedImage: true }), {
    eligible: true,
    reason: "verified prerelease image metadata",
  });
  assert.deepEqual(policy.cleanupEligibility({ pull, release: null, allowLegacy: true }), {
    eligible: true,
    reason: "explicit legacy cleanup override",
  });
  assert.equal(policy.cleanupEligibility({
    pull,
    release: {
      run: {
        name: "Release prerelease",
        display_title: `Release prerelease: ${pull.merge_commit_sha}`,
        conclusion: "failure",
      },
      jobs: [],
    },
    allowLegacy: true,
  }).eligible, false);
});

test("released-image detection requires a matching revision and prerelease tag", () => {
  const revision = "a".repeat(40);
  assert.equal(policy.hasReleasedImage([
    { imageRevision: revision, tags: ["v2.6.4-rc.1"] },
  ], revision), true);
  assert.equal(policy.hasReleasedImage([
    { imageRevision: "b".repeat(40), tags: ["v2.6.4-rc.1"] },
  ], revision), false);
  assert.equal(policy.hasReleasedImage([
    { imageRevision: revision, tags: ["v2.6.4"] },
  ], revision), false);
});

test("cleanup target resolution is fenced to the intended trigger", async () => {
  const pull = { number: 42, state: "closed", merged_at: null };
  const api = {
    pull: async (number) => number === 42 ? pull : null,
  };
  const direct = await policy.resolveTargets(api, { pull_request: { number: 42 } }, [], {
    GITHUB_EVENT_NAME: "pull_request_target",
  });
  assert.deepEqual(direct.map(({ number }) => number), [42]);

  const mergeSha = "c".repeat(40);
  const releaseRun = {
    id: 7,
    name: "Release prerelease",
    display_title: `Release prerelease: ${mergeSha}`,
  };
  const workflowApi = {
    associatedPulls: async () => [{ number: 42, merged_at: "2026-09-20T10:00:00Z", merge_commit_sha: mergeSha }],
    pull: async () => ({ ...pull, number: 42, merged_at: "2026-09-20T10:00:00Z", merge_commit_sha: mergeSha }),
    jobs: async () => [],
  };
  const workflow = await policy.resolveTargets(workflowApi, { workflow_run: releaseRun }, [], {
    GITHUB_EVENT_NAME: "workflow_run",
  });
  assert.deepEqual(workflow.map(({ number }) => number), [42]);
  assert.equal(workflow[0].release.run, releaseRun);

  for (const associatedPulls of [[], [
    { number: 42, merged_at: "2026-09-20T10:00:00Z", merge_commit_sha: mergeSha },
    { number: 43, merged_at: "2026-09-20T10:00:00Z", merge_commit_sha: mergeSha },
  ]]) {
    const ambiguous = await policy.resolveTargets({
      ...workflowApi,
      associatedPulls: async () => associatedPulls,
    }, { workflow_run: releaseRun }, [], { GITHUB_EVENT_NAME: "workflow_run" });
    assert.deepEqual(ambiguous, []);
  }

  await assert.rejects(
    policy.resolveTargets(api, {}, [], { CLEANUP_PR_NUMBER: "not-a-number" }),
    /CLEANUP_PR_NUMBER must be a positive integer/,
  );

  const manualRelease = {
    id: 8,
    name: "Release prerelease",
    display_title: `Release prerelease: ${mergeSha}`,
  };
  const manual = await policy.resolveTargets({
    pull: workflowApi.pull,
    releaseRuns: async () => [manualRelease],
    jobs: async () => [],
  }, {}, [], { CLEANUP_PR_NUMBER: "42" });
  assert.deepEqual(manual.map(({ number }) => number), [42]);
  assert.equal(manual[0].release.run, manualRelease);

  const sweep = await policy.resolveTargets({
    pull: async (number) => number === 44 ? null : { ...pull, number },
    releaseRuns: async () => [],
  }, {}, [
    { tags: ["test-42"], previewPr: null },
    { tags: [], previewPr: 43 },
    { tags: ["test-44"], previewPr: null },
  ], { CLEANUP_MANUAL_SWEEP: "true" });
  assert.deepEqual(sweep.map(({ number, pull: resolved }) => [number, Boolean(resolved)]), [
    [42, true],
    [43, true],
    [44, false],
  ]);
});

test("cleanup revalidation rejects a reopened PR and an incomplete release", () => {
  assert.throws(
    () => policy.validateCleanupState({ pull: { state: "open", merged_at: null }, release: null }),
    /pull request changed state before cleanup/,
  );
  const pull = {
    state: "closed",
    merged_at: "2026-09-20T10:00:00Z",
    merge_commit_sha: "b".repeat(40),
  };
  assert.throws(
    () => policy.validateCleanupState({
      pull,
      release: {
        run: { name: "Release prerelease", head_sha: pull.merge_commit_sha, display_title: `Release prerelease: ${pull.merge_commit_sha}`, conclusion: "failure" },
        jobs: [],
      },
    }),
    /cleanup eligibility changed before deletion/,
  );
  const eligible = policy.validateCleanupState({
    pull: {
      state: "closed",
      merged_at: "2026-09-20T10:00:00Z",
      merge_commit_sha: "e".repeat(40),
    },
    release: {
      run: {
        name: "Release prerelease",
        display_title: `Release prerelease: ${"e".repeat(40)}`,
        conclusion: "success",
      },
      jobs: [
        { name: "Classify release changes", conclusion: "success" },
        { name: "Promote preview image", conclusion: "success" },
      ],
    },
  });
  assert.deepEqual(eligible, { eligible: true, reason: "prerelease publication completed" });
});

test("release failure and incomplete no-release decisions retain the preview", () => {
  const sha = "c".repeat(40);
  const run = { name: "Release prerelease", head_sha: sha, display_title: `Release prerelease: ${sha}`, conclusion: "success" };
  const classifier = { name: "Classify release changes", conclusion: "success" };
  assert.equal(policy.releaseOutcome({
    run: { ...run, conclusion: "failure" },
    jobs: [classifier],
    mergeCommitSha: sha,
  }).eligible, false);
  assert.deepEqual(policy.releaseOutcome({
    run,
    jobs: [{ name: "Classify release changes", conclusion: "failure" }],
    mergeCommitSha: sha,
  }), {
    eligible: false,
    reason: "the release classifier did not complete successfully",
  });
  assert.deepEqual(policy.releaseOutcome({
    run,
    jobs: [classifier, { name: "Promote preview image", conclusion: "failure" }],
    mergeCommitSha: sha,
  }), {
    eligible: false,
    reason: "a prerelease publication job failed",
  });
  assert.equal(policy.releaseOutcome({
    run,
    jobs: [classifier, { name: "No prerelease required", conclusion: "success" }],
    mergeCommitSha: sha,
  }).eligible, true);
  assert.equal(policy.releaseOutcome({
    run,
    jobs: [classifier, { name: "Prepare prerelease", conclusion: "success" }],
    mergeCommitSha: sha,
  }).eligible, false);
  assert.deepEqual(policy.releaseOutcome({
    run,
    jobs: [classifier, { name: "Promote preview image", conclusion: "success" }, { name: "No prerelease required", conclusion: "success" }],
    mergeCommitSha: sha,
  }), {
    eligible: false,
    reason: "release workflow reported both publication and no-release decisions",
  });
});

test("deletion planning removes preview roots and owned untagged children", () => {
  const versions = [
    { id: 1, name: digest("a"), tags: ["test-42"], children: [digest("b")] },
    { id: 2, name: digest("b"), tags: [], previewPr: 42, children: [] },
    { id: 3, name: digest("c"), tags: ["v2.6.4-rc.9"], children: [digest("d")] },
    { id: 4, name: digest("d"), tags: [], previewPr: null, children: [] },
  ];
  const plan = policy.buildDeletionPlan({ versions, eligiblePrs: new Set([42]), targetPr: 42 });
  assert.deepEqual(plan.actions.map(({ type, versionId }) => [type, versionId]), [["delete", 1], ["delete", 2]]);
  assert.deepEqual(plan.blocked, []);
  assert.ok(plan.protectedDigests.includes(digest("c")));
  assert.ok(plan.protectedDigests.includes(digest("d")));
});

test("orphaned preview manifests and stable roots are handled by the complete graph", () => {
  const versions = [
    { id: 5, name: digest("k"), tags: ["test-42"], children: [digest("m")] },
    { id: 6, name: digest("m"), tags: [], previewPr: 42, children: [] },
    { id: 7, name: digest("n"), tags: [], previewPr: 42, children: [] },
    { id: 8, name: digest("o"), tags: ["latest"], children: [digest("m")] },
  ];
  const plan = policy.buildDeletionPlan({ versions, eligiblePrs: new Set([42]), targetPr: 42 });
  assert.deepEqual(plan.actions.map(({ type, versionId }) => [type, versionId]), [["delete", 5], ["delete", 7]]);
  assert.ok(plan.protectedDigests.includes(digest("o")));
  assert.ok(plan.protectedDigests.includes(digest("m")));
});

test("foreign ownership blocks an otherwise deletable preview graph", () => {
  const plan = policy.buildDeletionPlan({
    versions: [
      { id: 9, name: digest("p"), tags: ["test-42"], children: [digest("q")] },
      { id: 10, name: digest("q"), tags: [], previewPr: 43, children: [] },
    ],
    eligiblePrs: new Set([42]),
    targetPr: 42,
  });
  assert.deepEqual(plan.actions, [{ type: "delete", versionId: 9, digest: digest("p"), tags: ["test-42"] }]);
  assert.deepEqual(plan.blocked, [{ digest: digest("q"), reason: "untagged manifest child is missing or owned by another pull request" }]);
});

test("a refreshed retained tag changes the plan and must prevent deletion", () => {
  const original = [
    { id: 13, name: digest("r"), tags: ["test-42"], children: [digest("s")] },
    { id: 14, name: digest("s"), tags: [], previewPr: 42, children: [] },
  ];
  const before = policy.buildDeletionPlan({ versions: original, eligiblePrs: new Set([42]), targetPr: 42 });
  const changed = policy.buildDeletionPlan({
    versions: original.map((version) => version.id === 13 ? { ...version, tags: ["latest", "test-42"] } : version),
    eligiblePrs: new Set([42]),
    targetPr: 42,
  });
  assert.deepEqual(before.actions.map(({ type, versionId }) => [type, versionId]), [["delete", 13], ["delete", 14]]);
  assert.deepEqual(changed.actions.map(({ type, versionId }) => [type, versionId]), [["detach", 13]]);
});

test("shared test and release tags detach only the selected test alias", () => {
  const versions = [
    { id: 11, name: digest("e"), tags: ["test-42", "v2.6.4-rc.9"], children: [digest("f")] },
    { id: 12, name: digest("f"), tags: [], previewPr: 42, children: [] },
  ];
  const plan = policy.buildDeletionPlan({ versions, eligiblePrs: new Set([42]), targetPr: 42 });
  assert.deepEqual(plan.actions, [{
    type: "detach",
    versionId: 11,
    digest: digest("e"),
    tags: ["test-42"],
  }]);
});

test("detached cleanup deletes generated descendants but protects retained graphs", () => {
  const sourceRoot = digest("s");
  const sourceChild = digest("t");
  const detachedRoot = digest("u");
  const detachedChild = digest("v");
  const detachedGrandchild = digest("w");
  const externalRoot = digest("q");
  const plan = policy.buildDetachedDeletionPlan({
    versions: [
      { id: 51, name: sourceRoot, tags: ["v2.6.4-rc.9"], children: [sourceChild] },
      { id: 52, name: sourceChild, tags: [], children: [] },
      { id: 53, name: detachedRoot, tags: ["test-42"], children: [detachedChild] },
      { id: 54, name: detachedChild, tags: [], children: [detachedGrandchild] },
      { id: 55, name: detachedGrandchild, tags: [], children: [] },
      { id: 56, name: externalRoot, tags: [], children: [detachedChild] },
    ],
    detachedDigest: detachedRoot,
    selectedTags: ["test-42"],
  });
  assert.deepEqual(plan.actions.map(({ versionId }) => versionId), [53]);
  assert.deepEqual(plan.blocked, []);
  assert.ok(plan.protectedDigests.includes(sourceRoot));
  assert.ok(plan.protectedDigests.includes(sourceChild));
  assert.ok(plan.protectedDigests.includes(externalRoot));
  assert.ok(plan.protectedDigests.includes(detachedChild));

  const ordered = policy.buildDetachedDeletionPlan({
    versions: [
      { id: 53, name: detachedRoot, tags: ["test-42"], children: [detachedChild] },
      { id: 54, name: detachedChild, tags: [], children: [detachedGrandchild] },
      { id: 55, name: detachedGrandchild, tags: [], children: [] },
    ],
    detachedDigest: detachedRoot,
    selectedTags: ["test-42"],
  });
  assert.deepEqual(ordered.actions.map(({ versionId }) => versionId), [53, 54, 55]);
});

test("detached cleanup blocks retained tags and plan drift", () => {
  const root = digest("1");
  const child = digest("2");
  const retainedRoot = policy.buildDetachedDeletionPlan({
    versions: [
      { id: 71, name: root, tags: ["test-42", "latest"], children: [child] },
      { id: 72, name: child, tags: [], children: [] },
    ],
    detachedDigest: root,
    selectedTags: ["test-42"],
  });
  assert.deepEqual(retainedRoot.blocked, [{ digest: root, reason: "detached manifest has a retained tag" }]);

  const retainedChild = policy.buildDetachedDeletionPlan({
    versions: [
      { id: 73, name: root, tags: ["test-42"], children: [child] },
      { id: 74, name: child, tags: ["test-42"], children: [] },
    ],
    detachedDigest: root,
    selectedTags: ["test-42"],
  });
  assert.deepEqual(retainedChild.blocked, [{ digest: child, reason: "generated manifest has a retained tag" }]);

  const before = policy.buildDeletionPlan({
    versions: [{ id: 75, name: root, tags: ["test-42"], children: [] }],
    eligiblePrs: new Set([42]),
    targetPr: 42,
  });
  const changed = policy.buildDeletionPlan({
    versions: [{ id: 75, name: root, tags: ["latest", "test-42"], children: [] }],
    eligiblePrs: new Set([42]),
    targetPr: 42,
  });
  assert.throws(() => policy.assertPlanUnchanged(before, changed), /cleanup plan changed before deletion/);
});

test("registry scanning records OCI children and propagates preview ownership", () => {
  const root = digest("x");
  const child = digest("y");
  const labels = {
    "io.dense-mem.preview.pr": "42",
    "io.dense-mem.preview.head": "a".repeat(40),
    "io.dense-mem.preview.main": "b".repeat(40),
    "io.dense-mem.preview.run-id": "123",
    "io.dense-mem.preview.run-attempt": "1",
    "org.opencontainers.image.revision": "c".repeat(40),
  };
  const registry = new policy.RegistryClient({ image: "ghcr.io/example/image" });
  registry.run = (args) => {
    if (args[0] === "manifest") {
      return args[2].endsWith(root)
        ? JSON.stringify({ manifests: [{ digest: child, platform: { os: "linux", architecture: "amd64" } }] })
        : JSON.stringify({ config: { digest: digest("z") } });
    }
    if (args[0] === "image") return JSON.stringify({ config: { Labels: labels } });
    throw new Error(`unexpected registry command: ${args.join(" ")}`);
  };
  const versions = [
    { id: 61, digest: root, tags: ["test-42"] },
    { id: 62, digest: child, tags: [] },
  ];
  registry.scanVersions(versions);
  assert.deepEqual(versions[0].children, [child]);
  assert.equal(versions[1].previewPr, 42);
  assert.equal(versions[0].previewPr, 42);
  assert.equal(versions[0].imageRevision, labels["org.opencontainers.image.revision"]);
});

test("release history pagination is shared across a cleanup invocation", async () => {
  const api = new policy.GitHubApi({ apiUrl: "https://api.github.com", token: "test", repository: "markhuangai/dense-mem" });
  let calls = 0;
  api.paged = async () => {
    calls += 1;
    return [];
  };
  await Promise.all([api.releaseRuns(), api.releaseRuns(), api.releaseRuns()]);
  assert.equal(calls, 1);
});

test("release target resolution skips failed reruns in favor of a complete receipt", async () => {
  const mergeCommitSha = "d".repeat(40);
  const failedRerun = {
    id: 12,
    name: "Release prerelease",
    display_title: `Release prerelease: ${mergeCommitSha}`,
    conclusion: "failure",
  };
  const successfulNoRelease = {
    id: 11,
    name: "Release prerelease",
    display_title: `Release prerelease: ${mergeCommitSha}`,
    conclusion: "success",
  };
  const api = {
    releaseRuns: async () => [failedRerun, successfulNoRelease],
    jobs: async (runId) => runId === failedRerun.id
      ? [{ name: "Classify release changes", conclusion: "success" }]
      : [
        { name: "Classify release changes", conclusion: "success" },
        { name: "No prerelease required", conclusion: "success" },
      ],
  };
  const receipt = await policy.releaseForPull(api, {
    merged_at: "2026-09-20T10:00:00Z",
    merge_commit_sha: mergeCommitSha,
  });
  assert.equal(receipt.run.id, successfulNoRelease.id);
});

test("detached manifests retain synthetic preview ownership for retry discovery", () => {
  const registry = new policy.RegistryClient({ image: "ghcr.io/example/image" });
  const source = digest("a");
  const detached = digest("b");
  const calls = [];
  registry.run = (args) => {
    calls.push(args);
    if (args[0] === "manifest" && args[1] === "head") return detached;
    return "";
  };
  assert.equal(registry.detachTag(source, "test-42", "42-123"), detached);
  const mod = calls.find((args) => args[0] === "image");
  assert.deepEqual(mod.slice(-12), [
    "--label", "io.dense-mem.preview.pr=42",
    "--label", "io.dense-mem.preview.head=cleanup-42-123",
    "--label", "io.dense-mem.preview.main=cleanup-42-123",
    "--label", "io.dense-mem.preview.run-id=42-123",
    "--label", "io.dense-mem.preview.run-attempt=1",
    "--label", "org.opencontainers.image.version=cleanup-42-123",
  ]);

  assert.throws(
    () => registry.detachTag(source, "latest", "42-124"),
    /cannot detach a non-preview tag/,
  );
  const unchanged = new policy.RegistryClient({ image: "ghcr.io/example/image" });
  unchanged.run = (args) => args[0] === "manifest" && args[1] === "head" ? source : "";
  assert.throws(
    () => unchanged.detachTag(source, "test-42", "42-125"),
    /did not create a new manifest/,
  );
});

test("preview run polling paginates only active workflow statuses", async () => {
  const api = new policy.GitHubApi({ apiUrl: "https://api.github.com", token: "test", repository: "markhuangai/dense-mem" });
  const paths = [];
  api.paged = async (path) => {
    paths.push(path);
    const status = new URL(`https://api.github.com${path}`).searchParams.get("status");
    return [{ id: status, status }];
  };
  const runs = await api.previewRuns();
  assert.deepEqual(paths.map((path) => new URL(`https://api.github.com${path}`).searchParams.get("status")).sort(), [
    "in_progress", "pending", "queued", "requested", "waiting",
  ].sort());
  assert.equal(runs.length, 5);
});

test("manual target fanout stays within its concurrency bound", async () => {
  let active = 0;
  let peak = 0;
  await policy.mapWithConcurrency(Array.from({ length: 20 }, (_, index) => index), 3, async () => {
    active += 1;
    peak = Math.max(peak, active);
    await new Promise((resolve) => setTimeout(resolve, 1));
    active -= 1;
  });
  assert.equal(peak, 3);
});

test("cleanup waits for active preview publication before rescanning", async () => {
  let reads = 0;
  let jobs = 0;
  const sleeps = [];
  await policy.waitForPreviewQuiescence({
    previewRuns: async () => {
      reads += 1;
      return [{ id: 7, display_title: "PR test image: PR #42", status: "in_progress" }];
    },
    jobs: async () => [{
      name: "Publish trusted preview",
      status: reads === 1 ? "in_progress" : "completed",
      conclusion: reads === 1 ? null : "success",
    }],
  }, [42], { maxPolls: 3, pollMilliseconds: 0, sleep: async (milliseconds) => { sleeps.push(milliseconds); } });
  assert.equal(reads, 2);
  assert.deepEqual(sleeps, [0]);
});

test("cleanup does not wait for production E2E after publication", async () => {
  let jobs = 0;
  await policy.waitForPreviewQuiescence({
    previewRuns: async () => [{ id: 8, display_title: "PR test image: PR #42", status: "in_progress" }],
    jobs: async () => {
      jobs += 1;
      return [
        { name: "Build untrusted preview", status: "completed", conclusion: "success" },
        { name: "Publish trusted preview", status: "completed", conclusion: "success" },
        { name: "Run production-image E2E", status: "in_progress" },
      ];
    },
  }, [42], { maxPolls: 3, pollMilliseconds: 0 });
  assert.equal(jobs, 1);
});

test("cleanup does not wait after a failed preview build", async () => {
  let sleeps = 0;
  await policy.waitForPreviewQuiescence({
    previewRuns: async () => [{ id: 10, display_title: "PR test image: PR #42", status: "in_progress" }],
    jobs: async () => [{ name: "Build untrusted preview", status: "completed", conclusion: "failure" }],
  }, [42], { maxPolls: 3, pollMilliseconds: 0, sleep: async () => { sleeps += 1; } });
  assert.equal(sleeps, 0);
});

test("preview quiescence backs off to a five-minute polling cap", async () => {
  let reads = 0;
  let clock = 0;
  const delays = [];
  await assert.rejects(
    policy.waitForPreviewQuiescence({
      previewRuns: async () => {
        reads += 1;
        return [{ id: 9, display_title: "PR test image: PR #42", status: "in_progress" }];
      },
      jobs: async () => [
        { name: "Build untrusted preview", status: "in_progress" },
        { name: "Publish trusted preview", status: "queued" },
      ],
    }, [42], {
      sleep: async (milliseconds) => { delays.push(milliseconds); clock += milliseconds; },
      now: () => clock,
    }),
    /preview publication is still active for pull requests: 42/,
  );
  assert.equal(reads, 20);
  assert.deepEqual(delays, [60_000, 120_000, 240_000, ...Array(16).fill(300_000), 180_000]);
});

test("preview quiescence deadline includes API time and fails closed", async () => {
  let clock = 0;
  let reads = 0;
  await assert.rejects(
    policy.waitForPreviewQuiescence({
      previewRuns: async () => {
        reads += 1;
        clock = 101;
        return [{ id: 11, display_title: "PR test image: PR #42", status: "in_progress" }];
      },
      jobs: async () => [],
    }, [42], {
      maxPolls: 20,
      maxWaitMilliseconds: 100,
      now: () => clock,
      sleep: async () => {},
    }),
    /preview publication is still active for pull requests: 42/,
  );
  assert.equal(reads, 1);
});

test("preview quiescence deadline includes job API time", async () => {
  let clock = 0;
  let reads = 0;
  await assert.rejects(
    policy.waitForPreviewQuiescence({
      previewRuns: async () => [{ id: 12, display_title: "PR test image: PR #42", status: "in_progress" }],
      jobs: async () => {
        reads += 1;
        clock = 101;
        return [{ name: "Publish trusted preview", status: "completed", conclusion: "success" }];
      },
    }, [42], {
      maxWaitMilliseconds: 100,
      now: () => clock,
      sleep: async () => {},
    }),
    /preview publication is still active for pull requests: 42/,
  );
  assert.equal(reads, 1);
});

test("preview quiescence fails closed before returning after the deadline", async () => {
  let nowCalls = 0;
  await assert.rejects(
    policy.waitForPreviewQuiescence({
      previewRuns: async () => [{ id: 15, display_title: "PR test image: PR #42", status: "in_progress" }],
      jobs: async () => [{ name: "Publish trusted preview", status: "completed", conclusion: "success" }],
    }, [42], {
      maxWaitMilliseconds: 100,
      now: () => (nowCalls++ >= 4 ? 101 : 0),
      sleep: async () => {},
    }),
    /preview publication is still active for pull requests: 42/,
  );
});

test("preview quiescence observes a newly appearing preview rerun", async () => {
  let reads = 0;
  let jobs = 0;
  await policy.waitForPreviewQuiescence({
    previewRuns: async () => {
      reads += 1;
      if (reads < 3) {
        return [{ id: reads === 1 ? 13 : 14, display_title: "PR test image: PR #42", status: "in_progress" }];
      }
      return [];
    },
    jobs: async () => {
      jobs += 1;
      return [{ name: "Publish trusted preview", status: "in_progress" }];
    },
  }, [42], { maxPolls: 3, pollMilliseconds: 0, sleep: async () => {} });
  assert.equal(reads, 3);
  assert.equal(jobs, 2);
});

test("preview quiescence fails through the production GitHub API on a polling error", async () => {
  const api = new policy.GitHubApi({ apiUrl: "https://api.github.com", token: "test", repository: "markhuangai/dense-mem" });
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response("temporary outage", { status: 503 });
  try {
    await assert.rejects(
      policy.waitForPreviewQuiescence(api, [42], { maxPolls: 1, pollMilliseconds: 0 }),
      /GitHub API 503 for .*pr-test-image\.yml\/runs/,
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("preview quiescence bounds 100 matching runs to 2,100 production API requests", async () => {
  const api = new policy.GitHubApi({ apiUrl: "https://api.github.com", token: "test", repository: "markhuangai/dense-mem" });
  const originalFetch = globalThis.fetch;
  let requests = 0;
  let clock = 0;
  globalThis.fetch = async (url) => {
    requests += 1;
    const parsed = new URL(url);
    if (/\/actions\/runs\/\d+\/jobs$/.test(parsed.pathname)) {
      return new Response(JSON.stringify([{ name: "Publish trusted preview", status: "in_progress" }]), { status: 200 });
    }
    if (parsed.pathname.includes("/actions/workflows/pr-test-image.yml/runs")) {
      const status = parsed.searchParams.get("status");
      const offset = ["queued", "in_progress", "waiting", "requested", "pending"].indexOf(status) * 20;
      return new Response(JSON.stringify(Array.from({ length: 20 }, (_, index) => ({
        id: 1_000 + offset + index,
        display_title: `PR test image: PR #${(offset + index) % 2 === 0 ? 42 : 43}`,
        status: "in_progress",
      }))), { status: 200 });
    }
    throw new Error(`unexpected request: ${url}`);
  };
  try {
    await assert.rejects(
      policy.waitForPreviewQuiescence(api, [42, 43], {
        now: () => clock,
        sleep: async (milliseconds) => { clock += milliseconds; },
      }),
      /preview publication is still active for pull requests: 42, 43/,
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
  assert.equal(requests, 2_100);
});

test("preview run polling uses production pagination for each active status", async () => {
  const api = new policy.GitHubApi({ apiUrl: "https://api.github.com", token: "test", repository: "markhuangai/dense-mem" });
  const originalFetch = globalThis.fetch;
  const pages = [];
  globalThis.fetch = async (url) => {
    const parsed = new URL(url);
    pages.push(parsed.searchParams.get("status") + ":" + parsed.searchParams.get("page"));
    const page = Number(parsed.searchParams.get("page"));
    const offset = ["queued", "in_progress", "waiting", "requested", "pending"].indexOf(parsed.searchParams.get("status")) * 100;
    return new Response(JSON.stringify(page === 1 ? Array.from({ length: 100 }, (_, index) => ({ id: offset + index, status: parsed.searchParams.get("status") })) : []), { status: 200 });
  };
  try {
    const runs = await api.previewRuns();
    assert.equal(runs.length, 500);
  } finally {
    globalThis.fetch = originalFetch;
  }
  assert.equal(pages.length, 10);
  assert.equal(pages.filter((page) => page.endsWith(":2")).length, 5);
});

test("retained tags and unknown untagged children block destructive cleanup", () => {
  const versions = [
    { id: 21, name: digest("g"), tags: ["test-42"], children: [digest("h")] },
    { id: 22, name: digest("h"), tags: [], previewPr: null, children: [] },
  ];
  const plan = policy.buildDeletionPlan({ versions, eligiblePrs: new Set([42]), targetPr: 42 });
  assert.deepEqual(plan.actions, [{ type: "delete", versionId: 21, digest: digest("g"), tags: ["test-42"] }]);
  assert.deepEqual(plan.blocked, [{ digest: digest("h"), reason: "untagged manifest child is missing or owned by another pull request" }]);
});

test("batch limits fail closed", () => {
  const versions = [
    { id: 31, name: digest("i"), tags: ["test-42"], children: [] },
    { id: 32, name: digest("j"), tags: ["test-43"], children: [] },
  ];
  const plan = policy.buildDeletionPlan({ versions, eligiblePrs: new Set([42, 43]), maxActions: 1 });
  assert.deepEqual(plan.actions, []);
  assert.match(plan.blocked[0].reason, /batch limit/);
  assert.deepEqual(policy.aggregateBatch({ plans: [{ actions: [{}, {}] }, { actions: [{}] }], maxActions: 2 }), {
    total: 3,
    allowed: false,
  });
  assert.deepEqual(policy.aggregateBatch({
    plans: [
      { actions: [{}, {}], blocked: [{ reason: "retained child" }], cost: 2 },
      { actions: [{}], blocked: [], cost: 1 },
    ],
    maxActions: 1,
  }), {
    total: 1,
    allowed: true,
  });
});

test("workflow is trusted, event-fenced, dry-run capable, and registered in CI", async () => {
  const [workflow, request, release, ci] = await Promise.all([
    readFile(new URL("../../.github/workflows/pr-image-cleanup.yml", import.meta.url), "utf8"),
    readFile(new URL("../../.github/workflows/pr-image-cleanup-request.yml", import.meta.url), "utf8"),
    readFile(new URL("../../.github/workflows/release-rc.yml", import.meta.url), "utf8"),
    readFile(new URL("../../.github/workflows/ci-shared.yml", import.meta.url), "utf8"),
  ]);
  assert.match(workflow, /pull_request_target:/);
  assert.match(workflow, /types: \[closed\]/);
  assert.match(workflow, /schedule:\n    - cron: "\*\/15 \* \* \* \*"/);
  assert.match(workflow, /workflow_run:/);
  assert.match(workflow, /Request PR image cleanup/);
  assert.match(workflow, /group: pr-test-image-cleanup/);
  assert.match(workflow, /queue: max/);
  assert.doesNotMatch(workflow, /workflow_dispatch:/);
  assert.match(request, /workflow_dispatch:/);
  assert.match(request, /default: true/);
  assert.match(request, /allow_legacy:/);
  assert.match(request, /retention-days: 14/);
  assert.doesNotMatch(request, /packages: write/);
  assert.match(workflow, /packages: write/);
  assert.match(workflow, /persist-credentials: false/);
  assert.match(workflow, /ref: main/);
  assert.match(workflow, /REGCTL_SHA256/);
  assert.match(workflow, /CLEANUP_BATCH_LIMIT: "200"/);
  assert.match(workflow, /CLEANUP_ALLOW_LEGACY/);
  assert.match(workflow, /timeout-minutes: 100/);
  assert.match(workflow, /triggering_actor/);
  assert.match(workflow, /github\.triggering_actor/);
  assert.match(release, /run-name: "Release prerelease: \$\{\{ github\.event\.workflow_run\.head_sha \}\}"/);
  assert.match(release, /name: No prerelease required/);
  assert.match(release, /needs:\n      - classify-release\n      - prepare-prerelease/);
  assert.match(release, /needs\.prepare-prerelease\.outputs\.should_publish == 'false'/);
  assert.match(ci, /node --test tests\/uat\/pr_image_cleanup\.test\.mjs/);
  assert.match(ci, /node --test tests\/uat\/pr_image_cleanup_registry\.test\.mjs/);
});
