import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { createRequire } from "node:module";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import test from "node:test";
import publication from "../../.github/scripts/wiki-publication.cjs";
import documentation from "../../.github/scripts/wiki-documentation.cjs";

const require = createRequire(import.meta.url);
const yaml = require("../../.lint/node_modules/js-yaml");
const { git, publish, resolveRequest } = publication;
const repository = "markhuangai/dense-mem";

function objectCommit(directory, tree, parent, message) {
  const attribution = "Fixture <fixture@example.invalid> 1700000000 +0000";
  const parents = (Array.isArray(parent) ? parent : parent ? [parent] : []).map((sha) => `parent ${sha}\n`).join("");
  const body = `tree ${tree}\n${parents}author ${attribution}\ncommitter ${attribution}\n\n${message}\n`;
  return git(directory, ["hash-object", "-t", "commit", "-w", "--stdin"], { input: body }).trim();
}

function commit(directory, files, message = "Update wiki") {
  for (const [name, content] of Object.entries(files)) {
    const path = join(directory, name);
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, content);
  }
  git(directory, ["add", "."]);
  const tree = git(directory, ["write-tree"]).trim();
  const refs = git(directory, ["show-ref", "--head"], { input: "" });
  const parent = refs.match(/^([0-9a-f]{40}) HEAD$/m)?.[1];
  const sha = objectCommit(directory, tree, parent, message);
  git(directory, ["update-ref", "refs/heads/wiki", sha]);
  return sha;
}

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), "dense-mem-wiki-test-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const source = join(root, "source");
  const wiki = join(root, "wiki.git");
  const directory = join(root, "publisher.git");
  mkdirSync(source);
  git(source, ["init", "--quiet"]);
  git(source, ["symbolic-ref", "HEAD", "refs/heads/wiki"]);
  mkdirSync(wiki);
  git(wiki, ["init", "--bare", "--quiet"]);
  mkdirSync(directory);
  git(directory, ["init", "--bare", "--quiet"]);
  const initial = { ".gitignore": ".codex\n", "Home.md": "# Home\n[Guide](Guide#setup)\n", "_Sidebar.md": "[Home](Home)\n", "Guide.md": "# Guide\n## Setup\nOriginal.\n" };
  for (const [name, content] of Object.entries(initial)) writeFileSync(join(source, name), content);
  git(source, ["add", "."]);
  const base = objectCommit(source, git(source, ["write-tree"]).trim(), null, "Initial wiki");
  git(source, ["update-ref", "refs/heads/wiki", base]);
  git(source, ["push", wiki, `${base}:refs/heads/master`]);
  const logs = [];
  const options = { directory, sourceURL: source, wikiURL: wiki, log: (line) => logs.push(JSON.parse(line)) };
  return { root, source, wiki, directory, base, logs, options };
}

function head(f) { return git(f.wiki, ["rev-parse", "refs/heads/master"]).trim(); }

test("publishes a validated snapshot with source attribution and no branch metadata", async (t) => {
  const f = fixture(t);
  const source = commit(f.source, { "Guide.md": "# Guide\n## Setup\nUpdated.\n",
    ".github/workflows/wiki-source.yml": "name: Wiki source\n", ".coderabbit.yaml": "reviews: {}\n",
    "assets/example.bin": Buffer.from([0, 1, 255]) });
  const result = await publish({ ...f.options, requestedSource: source });
  assert.equal(head(f), result);
  assert.equal(git(f.wiki, ["show", `${result}:Guide.md`]), "# Guide\n## Setup\nUpdated.\n");
  assert.deepEqual(git(f.wiki, ["show", `${result}:assets/example.bin`], { encoding: null }), Buffer.from([0, 1, 255]));
  assert.doesNotMatch(git(f.wiki, ["ls-tree", "-r", "--name-only", result]), /\.github|coderabbit|\.gitignore/);
  assert.match(git(f.wiki, ["show", "-s", "--format=%B", result]), new RegExp(`Dense-Mem-Wiki-Source: ${source}`));
  assert.equal(git(f.wiki, ["rev-parse", `${result}^`]).trim(), f.base);
  assert.equal(f.logs.at(-1).outcome, "published");
  assert.equal(f.logs.at(-1).source_sha, source);
  assert.equal(f.logs.at(-1).live_sha, f.base);
});

test("repeated publication creates no empty commit and later source changes remain publishable", async (t) => {
  const f = fixture(t);
  commit(f.source, { "Guide.md": "# Guide\n## Setup\nFirst.\n" });
  const first = await publish(f.options);
  assert.equal(await publish(f.options), first);
  assert.equal(f.logs.at(-1).outcome, "unchanged");
  const secondSource = commit(f.source, { "Guide.md": "# Guide\n## Setup\nSecond.\n" });
  const second = await publish({ ...f.options, requestedSource: secondSource });
  assert.notEqual(second, first);
  assert.equal(git(f.wiki, ["rev-parse", `${second}^`]).trim(), first);
});

test("stale and out-of-order requests leave the live wiki untouched", async (t) => {
  const f = fixture(t);
  const current = commit(f.source, { "Guide.md": "# Guide\n## Setup\nCurrent.\n" });
  await publish({ ...f.options, requestedSource: current });
  const before = head(f);
  await assert.rejects(publish({ ...f.options, requestedSource: f.base }), { code: "stale_source" });
  t.diagnostic(JSON.stringify(f.logs.at(-1)));
  assert.equal(head(f), before);
  assert.equal(f.logs.at(-1).reason, "stale_source");
  assert.match(f.logs.at(-1).message, /newest publication request/);
});

test("a source advance during validation prevents publication", async (t) => {
  const f = fixture(t);
  const source = commit(f.source, { "Guide.md": "# Guide\n## Setup\nSelected.\n" });
  await assert.rejects(publish({ ...f.options, requestedSource: source, log: (line) => {
    f.logs.push(JSON.parse(line));
    if (f.logs.at(-1).event === "wiki_publication_validated") commit(f.source, { "Guide.md": "# Guide\n## Setup\nNewer.\n" });
  } }), { code: "stale_source" });
  assert.equal(head(f), f.base);
});

test("direct live edits are rejected until their history is reconciled into a wiki PR", async (t) => {
  const f = fixture(t);
  commit(f.source, { "Guide.md": "# Guide\n## Setup\nBranch update.\n" });
  const direct = objectCommit(f.wiki, git(f.wiki, ["rev-parse", `${f.base}^{tree}`]).trim(), f.base, "Direct wiki edit");
  git(f.wiki, ["update-ref", "refs/heads/master", direct]);
  await assert.rejects(publish(f.options), { code: "live_diverged" });
  t.diagnostic(JSON.stringify(f.logs.at(-1)));
  assert.equal(head(f), direct);
  assert.match(f.logs.at(-1).message, /merge its history into a wiki PR/);
  git(f.source, ["fetch", f.wiki, "refs/heads/master"]);
  const merged = objectCommit(f.source, git(f.source, ["rev-parse", "HEAD^{tree}"]).trim(),
    [git(f.source, ["rev-parse", "HEAD"]).trim(), direct], "Reconcile live history");
  git(f.source, ["update-ref", "refs/heads/wiki", merged]);
  await publish(f.options);
  assert.equal(git(f.wiki, ["rev-parse", `${head(f)}^`]).trim(), direct);
});

test("a concurrent live commit causes the normal push to fail without overwriting it", async (t) => {
  const f = fixture(t);
  commit(f.source, { "Guide.md": "# Guide\n## Setup\nBranch update.\n" });
  const concurrent = objectCommit(f.wiki, git(f.wiki, ["rev-parse", `${f.base}^{tree}`]).trim(), f.base, "Concurrent live change");
  const hook = join(f.wiki, "hooks", "pre-receive");
  // The competing writer uses existing objects outside the receive hook's quarantine.
  writeFileSync(hook, `#!/bin/sh\nunset GIT_QUARANTINE_PATH GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES\ngit update-ref refs/heads/master ${concurrent}\n`, { mode: 0o700 });
  await assert.rejects(publish(f.options), { code: "git_failed" });
  t.diagnostic(JSON.stringify(f.logs.at(-1)));
  assert.equal(head(f), concurrent);
  assert.equal(f.logs.at(-1).stage, "publish");
  assert.equal(f.logs.at(-1).outcome, "failed");
});

test("failed pushes retain causal logs while protecting the supplied credential", async (t) => {
  const f = fixture(t);
  commit(f.source, { "Guide.md": "# Guide\n## Setup\nChanged.\n" });
  writeFileSync(join(f.wiki, "hooks", "pre-receive"), '#!/bin/sh\necho "write denied sentinel-secret" >&2\nexit 1\n', { mode: 0o700 });
  await assert.rejects(publish({ ...f.options, env: { ...process.env, GITHUB_TOKEN: "sentinel-secret", GITHUB_RUN_ID: "42", GITHUB_RUN_ATTEMPT: "2" } }), /write denied \[REDACTED\]/);
  assert.equal(head(f), f.base);
  const failure = f.logs.at(-1);
  t.diagnostic(JSON.stringify(failure));
  assert.equal(failure.stage, "publish");
  assert.equal(failure.reason, "git_failed");
  assert.equal(failure.run_id, "42");
  assert.equal(failure.run_attempt, "2");
  assert.ok(failure.source_sha && failure.live_sha && failure.elapsed_ms >= 0);
  assert.doesNotMatch(JSON.stringify(f.logs), /sentinel-secret/);
});

test("invalid page links and application files fail before any live mutation", async (t) => {
  for (const [name, content, reason] of [["Home.md", "# Home\n[missing](Absent)\n", "invalid_documentation"],
    ["server.go", "package main\n", "unsupported_file"]]) {
    const f = fixture(t);
    commit(f.source, { [name]: content });
    await assert.rejects(publish(f.options));
    t.diagnostic(JSON.stringify(f.logs.at(-1)));
    assert.equal(head(f), f.base);
    assert.equal(f.logs.at(-1).stage, "validate");
    assert.equal(f.logs.at(-1).reason, reason);
  }
});

test("wiki links validate GFM headings, duplicate headings, references, HTML, and assets", async () => {
  const files = new Map(Object.entries({
    "Home.md": '# Home\n[Guide][guide]\n\n[guide]: Guide#setup-1\n<a href="Guide#an-explicit-anchor">Guide</a>\n![image](./assets/image.png)\n[Here](#home)\n',
    "_Sidebar.md": "[Home](Home)\n", "Guide.md": '# Guide\n## Setup\n## Setup\n<a id="an-explicit-anchor"></a>\n~~~md\n[ignored](Missing)\n~~~\n',
    "assets/image.png": "image",
  }).map(([name, content]) => [name, Buffer.from(content)]));
  assert.equal(await documentation.validateWiki(files), 3);
  files.set("Home.md", Buffer.from("# Home\n[Guide](Guide#absent)\n"));
  await assert.rejects(documentation.validateWiki(files), /missing wiki heading/);
  files.set("Home.md", Buffer.from("# Home\n![missing](./assets/missing.png)\n"));
  await assert.rejects(documentation.validateWiki(files), /missing wiki page or asset/);
});

test("reference links follow the first definition in both valid and invalid orders", async () => {
  const files = new Map([["_Sidebar.md", Buffer.from("[Home](Home)\n")], ["Guide.md", Buffer.from("# Guide\n## Setup\n")]]);
  files.set("Home.md", Buffer.from("# Home\n[Guide][r]\n\n[r]: Guide#absent\n[r]: Guide#setup\n"));
  await assert.rejects(documentation.validateWiki(files), /missing wiki heading Guide#absent/);
  files.set("Home.md", Buffer.from("# Home\n[Guide][r]\n\n[r]: Guide#setup\n[r]: Guide#absent\n"));
  assert.equal(await documentation.validateWiki(files), 3);
});

test("publication requires trusted main execution and a successful current wiki push", () => {
  const sha = "a".repeat(40);
  const event = { repository: { full_name: repository }, workflow_run: { path: ".github/workflows/wiki-source.yml",
    event: "push", head_branch: "wiki", head_repository: { full_name: repository }, conclusion: "success", head_sha: sha } };
  assert.equal(resolveRequest(event, "workflow_run", "refs/heads/main", repository), sha);
  assert.equal(resolveRequest(event, "workflow_dispatch", "refs/heads/main", repository), null);
  for (const update of [{ path: ".github/workflows/other.yml" }, { event: "pull_request" }, { head_branch: "docs/wiki" },
    { head_repository: { full_name: "fork/dense-mem" } }, { conclusion: "failure" }, { head_sha: "invalid" }]) {
    assert.throws(() => resolveRequest({ ...event, workflow_run: { ...event.workflow_run, ...update } }, "workflow_run", "refs/heads/main", repository), { code: "untrusted_request" });
  }
  assert.throws(() => resolveRequest(event, "workflow_dispatch", "refs/heads/wiki", repository), { code: "untrusted_request" });
});

test("missing credentials produce a complete causal request-stage diagnostic", (t) => {
  const f = fixture(t);
  const eventPath = join(f.root, "event.json");
  writeFileSync(eventPath, JSON.stringify({ repository: { full_name: repository } }));
  const env = { ...process.env, GITHUB_EVENT_PATH: eventPath, GITHUB_EVENT_NAME: "workflow_dispatch",
    GITHUB_REPOSITORY: repository, GITHUB_REF: "refs/heads/main", GITHUB_RUN_ID: "43", GITHUB_RUN_ATTEMPT: "1" };
  delete env.GITHUB_TOKEN;
  const result = spawnSync(process.execPath, [fileURLToPath(new URL("../../.github/scripts/wiki-publication.cjs", import.meta.url))], { env, encoding: "utf8", timeout: 30000 });
  assert.equal(result.status, 1);
  const failure = JSON.parse(result.stderr.trim());
  t.diagnostic(JSON.stringify(failure));
  assert.equal(failure.reason, "authentication_missing");
  assert.equal(failure.stage, "request");
  assert.equal(failure.run_id, "43");
  assert.equal(failure.run_attempt, "1");
  assert.equal(failure.source_sha, null);
  assert.equal(failure.live_sha, null);
  assert.ok(failure.elapsed_ms >= 0);
  assert.equal(failure.outcome, "failed");
  assert.equal(head(f), f.base);
});

test("trusted publishing and regression gates are wired without main-PR policy exemptions", () => {
  const workflow = yaml.load(readFileSync(new URL("../../.github/workflows/publish-wiki.yml", import.meta.url), "utf8"));
  assert.deepEqual(workflow.on.workflow_run.branches, ["wiki"]);
  assert.deepEqual(workflow.on.workflow_run.workflows, ["Wiki source"]);
  assert.equal(workflow.permissions.contents, "read");
  assert.equal(workflow.jobs.publish.permissions.contents, "write");
  assert.equal(workflow.concurrency["cancel-in-progress"], false);
  assert.equal(workflow.jobs.publish.steps[0].with.ref, "${{ github.workflow_sha }}");
  assert.ok(!workflow.on.pull_request_target && !workflow.on.pull_request);
  const ci = readFileSync(new URL("../../.github/workflows/ci-shared.yml", import.meta.url), "utf8");
  assert.match(ci, /node --test tests\/uat\/wiki_publication\.test\.mjs/);
});
