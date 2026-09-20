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
  let sleeps = 0;
  await policy.waitForPreviewQuiescence({
    previewRuns: async () => {
      reads += 1;
      return reads === 1
        ? [{ display_title: "PR test image: PR #42", status: "in_progress" }]
        : [];
    },
  }, [42], { maxPolls: 3, pollMilliseconds: 0, sleep: async () => { sleeps += 1; } });
  assert.equal(reads, 2);
  assert.equal(sleeps, 1);
});

test("preview quiescence default covers the preview publication window", async () => {
  let reads = 0;
  const delays = [];
  await assert.rejects(
    policy.waitForPreviewQuiescence({
      previewRuns: async () => {
        reads += 1;
        return [{ display_title: "PR test image: PR #42", status: "in_progress" }];
      },
    }, [42], { sleep: async (milliseconds) => { delays.push(milliseconds); } }),
    /preview publication is still active for pull requests: 42/,
  );
  assert.equal(reads, 90);
  assert.deepEqual(delays, Array(89).fill(60_000));
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
  assert.match(workflow, /workflow_run:/);
  assert.match(workflow, /Request PR image cleanup/);
  assert.match(workflow, /group: pr-test-image-cleanup/);
  assert.doesNotMatch(workflow, /workflow_dispatch:/);
  assert.match(request, /workflow_dispatch:/);
  assert.match(request, /default: true/);
  assert.doesNotMatch(request, /packages: write/);
  assert.match(workflow, /packages: write/);
  assert.match(workflow, /persist-credentials: false/);
  assert.match(workflow, /ref: main/);
  assert.match(workflow, /REGCTL_SHA256/);
  assert.match(workflow, /CLEANUP_BATCH_LIMIT: "200"/);
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
