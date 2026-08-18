import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { devNull, tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const resolver = fileURLToPath(
  new URL("../../.github/scripts/prerelease-version.sh", import.meta.url),
);
const isolatedGitEnvironment = Object.fromEntries(
  Object.entries(process.env).filter(([name]) => !name.startsWith("GIT_")),
);
isolatedGitEnvironment.GIT_CONFIG_GLOBAL = devNull;
isolatedGitEnvironment.GIT_CONFIG_NOSYSTEM = "1";

function gitWithEnvironment(repository, environment, ...args) {
  const result = spawnSync("git", args, {
    cwd: repository,
    encoding: "utf8",
    env: environment,
  });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

function git(repository, ...args) {
  return gitWithEnvironment(repository, isolatedGitEnvironment, ...args);
}

function createRepository(t, environment = isolatedGitEnvironment) {
  const repository = mkdtempSync(join(tmpdir(), "dense-mem-prerelease-"));
  t.after(() => rmSync(repository, { recursive: true, force: true }));

  gitWithEnvironment(repository, environment, "init", "--quiet");
  gitWithEnvironment(
    repository,
    environment,
    "config",
    "user.email",
    "prerelease-test@example.invalid",
  );
  gitWithEnvironment(repository, environment, "config", "user.name", "Prerelease Test");
  gitWithEnvironment(repository, environment, "commit", "--quiet", "--allow-empty", "-m", "initial");
  return repository;
}

function resolve(repository, ...args) {
  return spawnSync("bash", [resolver, ...args], {
    cwd: repository,
    encoding: "utf8",
    env: isolatedGitEnvironment,
  });
}

function tag(repository, ...tags) {
  for (const name of tags) {
    git(repository, "tag", name);
  }
}

test("temporary repositories ignore configured global hooks", (t) => {
  const home = mkdtempSync(join(tmpdir(), "dense-mem-prerelease-home-"));
  const hooks = join(home, "hooks");
  t.after(() => rmSync(home, { recursive: true, force: true }));
  mkdirSync(hooks);
  writeFileSync(join(home, ".gitconfig"), `[core]\n\thooksPath = ${hooks}\n`);
  writeFileSync(join(hooks, "pre-commit"), "#!/bin/sh\nexit 97\n");
  chmodSync(join(hooks, "pre-commit"), 0o755);

  const repository = createRepository(t, { ...isolatedGitEnvironment, HOME: home });

  assert.equal(git(repository, "log", "-1", "--format=%s"), "initial");
});

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

test("previous resolves the nearest first-parent release across a multi-commit delta", (t) => {
  const repository = createRepository(t);
  const mainBranch = git(repository, "branch", "--show-current");
  tag(repository, "v2.5.1");

  git(repository, "checkout", "--quiet", "-b", "tagged-side-branch");
  git(repository, "commit", "--quiet", "--allow-empty", "-m", "side release");
  tag(repository, "v9.0.0-rc.0");

  git(repository, "checkout", "--quiet", mainBranch);
  git(repository, "commit", "--quiet", "--allow-empty", "-m", "first release candidate");
  tag(repository, "v2.5.2-rc.0");
  git(repository, "merge", "--quiet", "--no-ff", "tagged-side-branch", "-m", "merge side branch");
  git(repository, "commit", "--quiet", "--allow-empty", "-m", "next release candidate");
  tag(repository, "v2.5.2-rc.1");

  const result = resolve(repository, "previous", "v2.5.2-rc.1");

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trim(), "v2.5.2-rc.0");
});
