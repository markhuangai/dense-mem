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
    head_sha: merged.merge_commit_sha,
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
  assert.equal(policy.cleanupEligibility({ pull: merged, release: { run, jobs } }).eligible, true);
});

test("release failure and incomplete no-release decisions retain the preview", () => {
  const sha = "c".repeat(40);
  const run = { name: "Release prerelease", head_sha: sha, conclusion: "success" };
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
  const [workflow, release, ci] = await Promise.all([
    readFile(new URL("../../.github/workflows/pr-image-cleanup.yml", import.meta.url), "utf8"),
    readFile(new URL("../../.github/workflows/release-rc.yml", import.meta.url), "utf8"),
    readFile(new URL("../../.github/workflows/ci-shared.yml", import.meta.url), "utf8"),
  ]);
  assert.match(workflow, /pull_request_target:/);
  assert.match(workflow, /types: \[closed\]/);
  assert.match(workflow, /workflow_run:/);
  assert.match(workflow, /workflow_dispatch:/);
  assert.match(workflow, /default: true/);
  assert.match(workflow, /packages: write/);
  assert.match(workflow, /persist-credentials: false/);
  assert.match(workflow, /ref: main/);
  assert.match(workflow, /REGCTL_SHA256/);
  assert.match(workflow, /CLEANUP_BATCH_LIMIT: "200"/);
  assert.match(release, /run-name: "Release prerelease: \$\{\{ github\.event\.workflow_run\.head_sha \}\}"/);
  assert.match(release, /name: No prerelease required/);
  assert.match(ci, /node --test tests\/uat\/pr_image_cleanup\.test\.mjs/);
  assert.match(ci, /node --test tests\/uat\/pr_image_cleanup_registry\.test\.mjs/);
});
