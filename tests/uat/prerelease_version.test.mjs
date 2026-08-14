import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const resolver = fileURLToPath(
  new URL("../../.github/scripts/prerelease-version.sh", import.meta.url),
);

function git(repository, ...args) {
  const result = spawnSync("git", args, {
    cwd: repository,
    encoding: "utf8",
  });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

function createRepository(t) {
  const repository = mkdtempSync(join(tmpdir(), "dense-mem-prerelease-"));
  t.after(() => rmSync(repository, { recursive: true, force: true }));

  git(repository, "init", "--quiet");
  git(repository, "config", "user.email", "prerelease-test@example.invalid");
  git(repository, "config", "user.name", "Prerelease Test");
  git(repository, "commit", "--quiet", "--allow-empty", "-m", "initial");
  return repository;
}

function resolve(repository, ...args) {
  return spawnSync("bash", [resolver, ...args], {
    cwd: repository,
    encoding: "utf8",
  });
}

function tag(repository, ...tags) {
  for (const name of tags) {
    git(repository, "tag", name);
  }
}

test("next defaults to a patch RC when no newer prerelease line exists", (t) => {
  const repository = createRepository(t);
  tag(repository, "v2.4.8", "v2.4.8-rc.4", "v2.4.7-rc.6");

  const result = resolve(repository, "next");

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trim(), "v2.4.9-rc.0");
});

test("next advances the highest prerelease base newer than stable", (t) => {
  const repository = createRepository(t);
  tag(
    repository,
    "v2.4.8",
    "v2.4.9-rc.0",
    "v2.5.0-rc.0",
    "v2.5.0-rc.9",
    "v2.5.0-rc.10",
  );

  const result = resolve(repository, "next");

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trim(), "v2.5.0-rc.11");
});

test("next returns to patch allocation after the active line becomes stable", (t) => {
  const repository = createRepository(t);
  tag(
    repository,
    "v2.4.8",
    "v2.4.9-rc.0",
    "v2.5.0-rc.3",
    "v2.5.0",
  );

  const result = resolve(repository, "next");

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trim(), "v2.5.1-rc.0");
});

test("start accepts any canonical SemVer base newer than known lines", (t) => {
  const repository = createRepository(t);
  tag(repository, "v2.4.8", "v2.4.9-rc.0");
  const head = git(repository, "rev-parse", "HEAD");

  const result = resolve(repository, "start", "v3.0.0", head);

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trim(), "v3.0.0-rc.0");
});

test("start is idempotent only for rc.0 on the requested target", (t) => {
  const repository = createRepository(t);
  tag(repository, "v2.4.8", "v2.5.0-rc.0");
  const taggedHead = git(repository, "rev-parse", "HEAD");

  const repeated = resolve(repository, "start", "v2.5.0", taggedHead);
  assert.equal(repeated.status, 0, repeated.stderr);
  assert.equal(repeated.stdout.trim(), "v2.5.0-rc.0");

  git(repository, "commit", "--quiet", "--allow-empty", "-m", "advance main");
  const advancedHead = git(repository, "rev-parse", "HEAD");
  const moved = resolve(repository, "start", "v2.5.0", advancedHead);
  assert.notEqual(moved.status, 0);
  assert.match(moved.stderr, /already exists at .* expected/);
});

test("start rejects malformed, stale, and already-advanced bases", async (t) => {
  const repository = createRepository(t);
  tag(repository, "v2.4.8", "v2.5.0-rc.1");
  const head = git(repository, "rev-parse", "HEAD");

  for (const releaseBase of ["2.6.0", "v02.6.0", "v2.4.8", "v2.4.9", "v2.5.0"]) {
    await t.test(releaseBase, () => {
      const result = resolve(repository, "start", releaseBase, head);
      assert.notEqual(result.status, 0);
    });
  }
});
