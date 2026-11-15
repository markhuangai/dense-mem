import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { main, replayRegistry } from "./replay.mjs";
import { sha256 } from "../../../.github/scripts/evaluation-policy.mjs";

const source = "a".repeat(40);
const image = `ghcr.io/markhuangai/dense-mem@sha256:${"b".repeat(64)}`;

function fixture(t, options = {}) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "evaluation-adapter-test-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const registry = fs.readFileSync(options.mode === "live" ? "tests/eval/datasets.json" : "tests/eval/fixtures/datasets.json");
  const measured = {
    identity: { version: 1, run_id: "42-hotpotqa", workflow_run_id: "42", source_revision: source, image, registry_sha256: sha256(registry), dataset: "hotpotqa", model: "fixture", embedding_model: "@cf/baai/bge-m3", embedding_dimensions: 1024, context_window_tokens: 300000, ...options.identity },
    completed_answers: 8, expected_answers: 8, complete: true,
  };
  const archive = spawnSync("python3", ["-c", "import io,sys,zipfile; b=io.BytesIO(); z=zipfile.ZipFile(b,'w'); z.writestr('scores.json',sys.stdin.buffer.read()); z.close(); sys.stdout.buffer.write(b.getvalue())"], { input: JSON.stringify(measured) });
  assert.equal(archive.status, 0, archive.stderr.toString());
  const artifact = { id: 7, name: "evaluation-hotpotqa", expired: false, digest: `sha256:${sha256(archive.stdout)}`, ...options.artifact };
  const original = { id: 42, path: ".github/workflows/production-image-e2e.yml", event: "workflow_dispatch", head_sha: source, run_attempt: 1, status: "completed", ...options.run };
  const requests = [];
  const env = { GITHUB_ACTIONS: "true", RUNNER_ENVIRONMENT: "github-hosted", GITHUB_RUN_ID: "84", GITHUB_RUN_ATTEMPT: "1", GITHUB_REPOSITORY: "markhuangai/dense-mem", GH_TOKEN: "synthetic-api-canary", RUNNER_TEMP: directory, EVAL_REPLAY_RUN_ID: "42", EVAL_SOURCE_REVISION: source, EVAL_IMAGE: image, EVAL_DATASET: "hotpotqa" };
  t.mock.method(globalThis, "fetch", async (url, init) => {
    assert.equal(init.headers.Authorization, `Bearer ${env.GH_TOKEN}`);
    assert.ok(String(url).startsWith("https://api.github.com/repos/markhuangai/dense-mem/"));
    const route = String(url).slice("https://api.github.com/repos/markhuangai/dense-mem".length);
    requests.push(route);
    if (options.unavailable && route.includes(options.unavailable)) return new Response("unavailable", { status: 404 });
    if (route === "/actions/runs/42") return Response.json(original);
    if (route === "/actions/runs/42/artifacts?per_page=100&page=1") return Response.json({ artifacts: [artifact] });
    if (route === "/actions/artifacts/7/zip") return new Response(options.corruptArchive ? Buffer.concat([archive.stdout, Buffer.from("changed")]) : archive.stdout);
    if (route === "/actions/runs/42/jobs?filter=latest&per_page=100&page=1") return Response.json({ jobs: options.jobs ?? [{ id: 9, name: "Public evaluation / Evaluate hotpotqa" }] });
    if (route === "/actions/jobs/9/logs") return new Response("synthetic protected log transport fixture\n");
    throw new Error(`Unexpected transport request ${route}`);
  });
  const receipt = path.join(directory, "report-invocation.json");
  const numeric = path.join(directory, "report-output.json");
  fs.writeFileSync(numeric, JSON.stringify({ ...measured, completed_answers: options.differentScores ? 7 : measured.completed_answers }));
  const command = path.join(directory, "report-command.cjs");
  // Go replay tests own reconstruction; this executable isolates the adapter's report handoff and comparison.
  fs.writeFileSync(command, `#!${process.execPath}\nconst fs = require("node:fs");\nconst path = require("node:path");\nconst manifest = JSON.parse(fs.readFileSync(process.argv[3]));\nif (process.argv[2] !== "report" || manifest.mode !== "replay" || !process.env.EVAL_LOGS_DIR) process.exit(3);\nfs.writeFileSync(${JSON.stringify(receipt)}, JSON.stringify({ arguments: process.argv.slice(2), manifest, logs: fs.readFileSync(path.join(process.env.EVAL_LOGS_DIR, "dataset.txt"), "utf8") }));\nfs.mkdirSync(manifest.output_dir, { recursive: true });\nfs.copyFileSync(${JSON.stringify(numeric)}, path.join(manifest.output_dir, "scores.json"));\n`, { mode: 0o700 });
  env.EVAL_RUNNER_BINARY = command;
  return { env, measured, requests, receipt };
}

test("retained registry resolves both pinned campaign matrices through verified numeric archives", async (t) => {
  for (const mode of ["live", "fixtures"]) {
    await t.test(mode, async (t) => {
      const value = fixture(t, { mode });
      const result = await replayRegistry(value.env);
      assert.equal(result.mode, mode);
      assert.equal(result.matrix.dataset.length, mode === "live" ? 5 : 1);
      assert.ok(value.requests.includes("/actions/artifacts/7/zip"));
    });
  }
});

test("retained registry rejects wrong run identity, expired evidence, digests and registry pins", async (t) => {
  for (const [name, options, message] of [
    ["workflow", { run: { path: ".github/workflows/other.yml" } }, /source run identity/],
    ["event", { run: { event: "push" } }, /source run identity/],
    ["source", { run: { head_sha: "c".repeat(40) } }, /source run identity/],
    ["rerun", { run: { run_attempt: 2 } }, /source run identity/],
    ["unfinished", { run: { status: "in_progress" } }, /source run identity/],
    ["expired", { artifact: { expired: true } }, /expired/],
    ["unverifiable", { artifact: { digest: "" } }, /expired/],
    ["changed archive", { corruptArchive: true }, /digest mismatch/],
    ["measured source", { identity: { source_revision: "c".repeat(40) } }, /measured registry identity/],
    ["measured image", { identity: { image: "another-image" } }, /measured registry identity/],
    ["measured run", { identity: { workflow_run_id: "43" } }, /measured registry identity/],
    ["unknown registry", { identity: { registry_sha256: "c".repeat(64) } }, /pinned registry/],
    ["unavailable run", { unavailable: "/actions/runs/42" }, /evidence is missing/],
  ]) {
    await t.test(name, async (t) => {
      const value = fixture(t, options);
      await assert.rejects(replayRegistry(value.env), message);
    });
  }
});

test("replay adapter hands retained logs to report mode and verifies the returned numeric result", async (t) => {
  const value = fixture(t);
  await main(value.env);
  const receipt = JSON.parse(fs.readFileSync(value.receipt));
  assert.equal(receipt.arguments[0], "report");
  assert.equal(receipt.manifest.mode, "replay");
  assert.equal(receipt.manifest.workflow_run_id, "84");
  assert.equal(receipt.manifest.run_id, value.measured.identity.run_id);
  assert.equal(receipt.manifest.source_revision, source);
  assert.equal(receipt.manifest.image, image);
  assert.match(receipt.logs, /synthetic protected log transport fixture/);
  assert.ok(value.requests.every((route) => route.startsWith("/actions/")));
});

test("replay adapter rejects wrong retained inputs and stale report output", async (t) => {
  for (const [name, options, message] of [
    ["source run", { run: { head_sha: "c".repeat(40) } }, /source identity/],
    ["expired numeric artifact", { artifact: { expired: true } }, /missing, expired/],
    ["artifact digest", { corruptArchive: true }, /digest mismatch/],
    ["measured image", { identity: { image: "another-image" } }, /measured identity/],
    ["measured dataset", { identity: { dataset: "musique" } }, /measured identity/],
    ["missing job", { jobs: [] }, /logs cannot be identified/],
    ["ambiguous job", { jobs: [{ id: 9, name: "Evaluate hotpotqa" }, { id: 10, name: "Evaluate hotpotqa" }] }, /logs cannot be identified/],
    ["expired logs", { unavailable: "/logs" }, /HTTP 404/],
    ["registry mismatch", { identity: { registry_sha256: "c".repeat(64) } }, /registry differs/],
    ["numeric mismatch", { differentScores: true }, /differs from measured scores/],
  ]) {
    await t.test(name, async (t) => {
      const value = fixture(t, options);
      await assert.rejects(main(value.env), message);
      if (!options.differentScores) assert.equal(fs.existsSync(value.receipt), false);
    });
  }
});

test("both replay entrypoints reject local execution and job reruns before transport", async (t) => {
  const value = fixture(t);
  for (const env of [{ ...value.env, GITHUB_ACTIONS: "false" }, { ...value.env, GITHUB_RUN_ATTEMPT: "2" }]) {
    await assert.rejects(replayRegistry(env), /fresh GitHub-hosted/);
    await assert.rejects(main(env), /fresh GitHub-hosted/);
  }
  assert.deepEqual(value.requests, []);
});
