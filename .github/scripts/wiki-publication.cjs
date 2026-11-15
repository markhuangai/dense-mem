"use strict";

const { spawnSync } = require("node:child_process");
const { mkdtempSync, writeFileSync, rmSync, readFileSync } = require("node:fs");
const { join } = require("node:path");
const { tmpdir } = require("node:os");
const { isWikiFile, readDirectory, validateWiki } = require("./wiki-documentation.cjs");

const SHA = /^[0-9a-f]{40}$/;
const REQUEST_PATH = ".github/workflows/wiki-source.yml";

function sourceMetadata(name) {
  return [".gitignore", ".coderabbit.yaml", ".github"].includes(name) || name.startsWith(".github/");
}

class PublicationError extends Error {
  constructor(code, message) { super(message); this.code = code; }
}

function git(directory, args, options = {}) {
  const result = spawnSync("git", args, { cwd: directory, encoding: "utf8", timeout: 30000,
    maxBuffer: 16 * 1024 * 1024, ...options });
  if (result.error || result.status !== 0) {
    const message = String(result.error?.message || result.stderr || "Git operation failed");
    const secret = options.env?.GITHUB_TOKEN || process.env.GITHUB_TOKEN;
    throw new PublicationError("git_failed", `${args[0]}: ${secret ? message.split(secret).join("[REDACTED]") : message}`);
  }
  return result.stdout;
}

function remoteHead(directory, url, ref, env) {
  const output = git(directory, ["ls-remote", "--exit-code", url, ref], { env }).trim();
  const [sha, resolved] = output.split(/\s+/);
  if (!SHA.test(sha || "") || resolved !== ref || output.includes("\n")) {
    throw new PublicationError("invalid_ref", `expected one commit for ${ref}`);
  }
  return sha;
}

function resolveRequest(event, eventName, ref, repository) {
  if (repository !== "markhuangai/dense-mem" || event.repository?.full_name !== repository || ref !== "refs/heads/main") {
    throw new PublicationError("untrusted_request", "publication must use the main workflow in markhuangai/dense-mem");
  }
  if (eventName === "workflow_dispatch") return null;
  const run = event.workflow_run;
  if (eventName !== "workflow_run" || run?.path !== REQUEST_PATH || run.event !== "push" ||
      run.head_branch !== "wiki" || run.head_repository?.full_name !== repository ||
      run.conclusion !== "success" || !SHA.test(run.head_sha || "")) {
    throw new PublicationError("untrusted_request", "expected a successful same-repository wiki push request");
  }
  return run.head_sha;
}

function snapshot(directory, revision) {
  if (!SHA.test(revision || "")) throw new PublicationError("invalid_ref", "invalid source revision");
  const files = new Map();
  const entries = git(directory, ["ls-tree", "-rz", "--full-tree", revision]).split("\0").filter(Boolean);
  for (const entry of entries) {
    const [metadata, name] = entry.split("\t");
    const [mode, type] = metadata.split(" ");
    if (name && sourceMetadata(name)) continue;
    if (!name || /[\x00-\x1f\x7f]/.test(name) || mode !== "100644" || type !== "blob" || !isWikiFile(name)) {
      throw new PublicationError("unsupported_file", `unsupported wiki entry: ${name || "unknown"}`);
    }
    files.set(name, git(directory, ["show", `${revision}:${name}`], { encoding: null }));
  }
  const roots = git(directory, ["ls-tree", "-z", revision]).split("\0").filter(Boolean)
    .filter((entry) => !sourceMetadata(entry.split("\t")[1]));
  const tree = git(directory, ["mktree", "-z"], { input: roots.join("\0") + "\0" }).trim();
  return { files, tree };
}

function ancestor(directory, older, newer) {
  const result = spawnSync("git", ["merge-base", "--is-ancestor", older, newer], { cwd: directory, encoding: "utf8", timeout: 30000 });
  if (result.error || ![0, 1].includes(result.status)) throw new PublicationError("history_unavailable", "cannot verify wiki ancestry");
  return result.status === 0;
}

function assertLiveHistory(directory, source, live) {
  if (ancestor(directory, live, source)) return;
  const message = git(directory, ["show", "-s", "--format=%B", live]);
  const previous = message.match(/^Dense-Mem-Wiki-Source: ([0-9a-f]{40})$/m)?.[1];
  if (!previous || !ancestor(directory, previous, source) ||
      snapshot(directory, previous).tree !== git(directory, ["rev-parse", `${live}^{tree}`]).trim()) {
    throw new PublicationError("live_diverged", "live wiki changed outside the source branch; merge its history into a wiki PR before publishing");
  }
}

function publicationCommit(directory, source, live, tree) {
  const original = git(directory, ["cat-file", "commit", source]);
  const author = original.match(/^author .+$/m)?.[0];
  const committer = original.match(/^committer .+$/m)?.[0];
  if (!author || !committer) throw new PublicationError("invalid_source", "source commit has no attribution");
  // Preserve source attribution without changing Git identity or configuration.
  const content = `tree ${tree}\nparent ${live}\n${author}\n${committer}\n\nPublish Dense-Mem wiki\n\nDense-Mem-Wiki-Source: ${source}\n`;
  return git(directory, ["hash-object", "-t", "commit", "-w", "--stdin"], { input: content }).trim();
}

async function publish({ directory, sourceURL, wikiURL, requestedSource, env = process.env, log = console.log }) {
  const started = Date.now();
  const context = { run_id: env.GITHUB_RUN_ID || "local", run_attempt: env.GITHUB_RUN_ATTEMPT || "1", source_sha: null, live_sha: null };
  let stage = "resolve";
  const emit = (event, fields = {}) => log(JSON.stringify({ event, level: "info", stage, ...context, ...fields, elapsed_ms: Date.now() - started }));
  try {
    const source = remoteHead(directory, sourceURL, "refs/heads/wiki", env);
    context.source_sha = source;
    emit("wiki_publication_started");
    if (requestedSource && requestedSource !== source) throw new PublicationError("stale_source", "wiki source advanced; use its newest publication request");
    git(directory, ["fetch", "--no-tags", sourceURL, "refs/heads/wiki"], { env });
    if (git(directory, ["rev-parse", "FETCH_HEAD"]).trim() !== source) throw new PublicationError("stale_source", "wiki source advanced during fetch");
    stage = "validate";
    const current = snapshot(directory, source);
    const pages = await validateWiki(current.files);
    emit("wiki_publication_validated", { pages });
    stage = "live_history";
    const live = remoteHead(directory, wikiURL, "refs/heads/master", env);
    context.live_sha = live;
    git(directory, ["fetch", "--no-tags", wikiURL, "refs/heads/master"], { env });
    if (git(directory, ["rev-parse", "FETCH_HEAD"]).trim() !== live) throw new PublicationError("live_advanced", "live wiki advanced during fetch; inspect the next request");
    assertLiveHistory(directory, source, live);
    const unchanged = current.tree === git(directory, ["rev-parse", `${live}^{tree}`]).trim();
    stage = "publish";
    if (remoteHead(directory, sourceURL, "refs/heads/wiki", env) !== source) throw new PublicationError("stale_source", "wiki source advanced before publication");
    if (unchanged) {
      emit("wiki_publication_unchanged", { published_sha: live, outcome: "unchanged" });
      return live;
    }
    const commit = publicationCommit(directory, source, live, current.tree);
    git(directory, ["push", wikiURL, `${commit}:refs/heads/master`], { env });
    stage = "verify";
    if (remoteHead(directory, wikiURL, "refs/heads/master", env) !== commit) throw new PublicationError("verification_failed", "published wiki head changed before verification");
    emit("wiki_publication_completed", { published_sha: commit, outcome: "published" });
    return commit;
  } catch (error) {
    emit("wiki_publication_failed", { level: "error", reason: error.code || "invalid_documentation", message: error.message, outcome: "failed" });
    error.reported = true;
    throw error;
  }
}

async function main() {
  if (process.argv[2] === "check") {
    console.log(`Validated ${await validateWiki(readDirectory(process.argv[3]))} wiki pages.`);
    return;
  }
  const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, "utf8"));
  const requestedSource = resolveRequest(event, process.env.GITHUB_EVENT_NAME, process.env.GITHUB_REF, process.env.GITHUB_REPOSITORY);
  if (!process.env.GITHUB_TOKEN) throw new PublicationError("authentication_missing", "GITHUB_TOKEN is required for wiki publication");
  const directory = mkdtempSync(join(tmpdir(), "dense-mem-wiki-publish-"));
  try {
    const askpass = join(directory, "askpass.sh");
    writeFileSync(askpass, '#!/bin/sh\ncase "$1" in *Username*) printf "%s\\n" x-access-token;; *) printf "%s\\n" "$GITHUB_TOKEN";; esac\n', { mode: 0o700 });
    const env = { ...process.env, GIT_ASKPASS: askpass, GIT_TERMINAL_PROMPT: "0" };
    git(directory, ["init", "--bare"]);
    await publish({ directory, sourceURL: `https://github.com/${process.env.GITHUB_REPOSITORY}.git`,
      wikiURL: `https://github.com/${process.env.GITHUB_REPOSITORY}.wiki.git`, requestedSource, env });
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

module.exports = { PublicationError, git, remoteHead, resolveRequest, snapshot, assertLiveHistory, publicationCommit, publish };
if (require.main === module) {
  const started = Date.now();
  main().catch((error) => {
    if (!error.reported) console.error(JSON.stringify({ event: "wiki_publication_failed", level: "error", stage: "request",
      run_id: process.env.GITHUB_RUN_ID || "local", run_attempt: process.env.GITHUB_RUN_ATTEMPT || "1",
      source_sha: null, live_sha: null, elapsed_ms: Date.now() - started,
      reason: error.code || "wiki_failed", message: error.message, outcome: "failed" }));
    process.exitCode = 1;
  });
}
