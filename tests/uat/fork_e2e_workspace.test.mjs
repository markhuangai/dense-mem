import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtemp, mkdir, readFile, writeFile, rm, symlink } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { test } from "node:test";
import workspace from "../../.github/scripts/fork-e2e-workspace.cjs";

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "dense-mem-fork-workspace-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const harness = join(root, "harness");
  const source = join(root, "source");
  for (const directory of [harness, source]) {
    await mkdir(directory);
    execFileSync("git", ["init", "--quiet", directory]);
  }
  async function put(directory, name, content) {
    await mkdir(join(directory, name, ".."), { recursive: true });
    await writeFile(join(directory, name), content);
  }
  await put(harness, ".github/scripts/fork-e2e-policy.cjs", "trusted policy");
  await put(harness, "scripts/e2e-host-controller.sh", "trusted controller");
  await put(harness, "tests/uat/proof.mjs", "trusted assertions");
  await put(harness, "internal/feature/feature_test.go", "trusted Go tests");
  await put(harness, "internal/feature/feature.go", "upstream implementation");
  await put(source, "internal/feature/feature.go", "candidate implementation");
  await put(source, "internal/feature/feature_test.go", "fake passing tests");
  await put(source, "scripts/e2e-host-controller.sh", "fake controller");
  await put(source, "tests/uat/fake.mjs", "fake assertions");
  for (const directory of [harness, source]) execFileSync("git", ["-C", directory, "add", "."]);
  return { root, harness, source, put };
}

test("fork workspace runs trusted assertions against the candidate implementation", async (t) => {
  const { harness, source } = await fixture(t);
  const index = await readFile(join(source, ".git/index"));
  workspace.prepareForkWorkspace(harness, source);
  assert.equal(await readFile(join(source, "internal/feature/feature.go"), "utf8"), "candidate implementation");
  assert.equal(await readFile(join(source, "internal/feature/feature_test.go"), "utf8"), "trusted Go tests");
  assert.equal(await readFile(join(source, "scripts/e2e-host-controller.sh"), "utf8"), "trusted controller");
  assert.equal(await readFile(join(source, "tests/uat/proof.mjs"), "utf8"), "trusted assertions");
  await assert.rejects(readFile(join(source, "tests/uat/fake.mjs")), { code: "ENOENT" });
  assert.deepEqual(await readFile(join(source, ".git/index")), index);
});

test("fork workspace rejects symlinks that would redirect trusted test files", async (t) => {
  const { root, harness, source } = await fixture(t);
  await rm(join(source, "internal"), { recursive: true });
  const outside = join(root, "outside");
  await mkdir(outside);
  await symlink(outside, join(source, "internal"));
  assert.throws(() => workspace.prepareForkWorkspace(harness, source), /fork symlink/);
  await assert.rejects(readFile(join(outside, "feature/feature_test.go")), { code: "ENOENT" });
});
