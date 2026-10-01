import assert from "node:assert/strict";
import { test } from "node:test";
import policy from "../../.github/scripts/image-release-policy.cjs";

const repository = "markhuangai/dense-mem";
const pull = { number: 42, head: { sha: "a".repeat(40) } };
const status = { runId: "100", runAttempt: "2", targetUrl: `https://github.com/${repository}/actions/runs/100` };
const receipt = { version: 1, pr_number: pull.number, source_sha: pull.head.sha,
  image: `ghcr.io/${repository}:test-42@sha256:${"b".repeat(64)}`,
  run_id: "100", run_attempt: "2", trusted_revision: "c".repeat(40) };
function comment(value, url = status.targetUrl) {
  return { user: { login: "github-actions[bot]" }, body: policy.formatPreviewReceipt(value, url) };
}
function select(comments) { return policy.selectPreviewReceipt({ comments, pull, status, repository }); }

test("preview receipts select the current successful attempt instead of an older image", () => {
  const older = { ...receipt, run_attempt: "1", image: `ghcr.io/${repository}:test-42@sha256:${"d".repeat(64)}` };
  const otherRun = { ...receipt, run_id: "101" };
  assert.deepEqual(select([comment(older), comment(otherRun, status.targetUrl.replace("100", "101")), comment(receipt)]), receipt);
});

test("preview receipts reject missing current attempts, ambiguity, altered bodies, and untrusted authors", () => {
  for (const comments of [[], [comment({ ...receipt, run_attempt: "1" })],
    [comment(receipt), comment(receipt)],
    [{ ...comment(receipt), user: { login: "contributor" } }],
    [{ ...comment(receipt), body: `${comment(receipt).body}\nextra` }],
    [{ user: { login: "github-actions[bot]" }, body: `Published test image \`${receipt.image}\` for \`${receipt.source_sha}\`.\n\n${status.targetUrl}` }]]) {
    assert.throws(() => select(comments), /current-attempt preview receipt/);
  }
});

test("preview receipt validation binds PR, SHA, immutable upstream image, and policy revision", () => {
  for (const changes of [{ pr_number: 43 }, { source_sha: "d".repeat(40) }, { image: receipt.image.replace(repository, "fork/dense-mem") },
    { trusted_revision: "main" }, { run_attempt: "3" }]) {
    const value = { ...receipt, ...changes };
    if (changes.trusted_revision === "main") {
      assert.throws(() => comment(value), /invalid versioned/);
    } else assert.throws(() => select([comment(value)]), /current-attempt preview receipt/);
  }
});
