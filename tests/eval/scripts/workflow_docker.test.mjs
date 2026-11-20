import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { requireRunner } from "../../../.github/scripts/evaluation-policy.mjs";
import { cleanup, location, main } from "./workflow.mjs";

const image = "busybox:1.37.0-musl";
const ownershipLabel = "io.dense-mem.eval.run";

function docker(...args) {
  const result = spawnSync("docker", args, { encoding: "utf8", timeout: 120_000 });
  assert.equal(result.status, 0, `Docker ${args[0]} failed (${result.signal || result.status})`);
  return result.stdout.trim();
}

function exists(kind, name) {
  const args = [kind, "ls", ...(kind === "container" ? ["--all"] : []), "--format", kind === "container" ? "{{.Names}}" : "{{.Name}}"];
  return docker(...args).split("\n").includes(name);
}

function scope(t, foreign = false, local = false) {
  const dataset = `cleanup_${randomUUID().replaceAll("-", "")}`;
  const seedBuildID = `local-${randomUUID().replaceAll("-", "")}`;
  const env = local ? { EVAL_MODE: "live", EVAL_DATASET: dataset, EVAL_LOCAL_SEED_ID: seedBuildID, EVAL_LOCAL_SEED_ROOT: os.tmpdir() }
    : { ...process.env, EVAL_MODE: "fixtures", EVAL_DATASET: dataset };
  const { runID, directory, checkpointFile } = location(env);
  const prefix = `densemem-eval-${runID}`;
  const container = `${prefix}-server`;
  const network = `${prefix}-network`;
  const volume = `${prefix}-postgres`;
  const owner = foreign ? `foreign-${runID}` : runID;
  fs.mkdirSync(directory, { mode: 0o700 });
  t.after(() => {
    if (exists("container", container)) {
      assert.equal(JSON.parse(docker("container", "inspect", container))[0].Config.Labels[ownershipLabel], owner);
      docker("container", "rm", "--force", "--volumes", container);
    }
    for (const [kind, name] of [["network", network], ["volume", volume]]) {
      if (!exists(kind, name)) continue;
      assert.equal(JSON.parse(docker(kind, "inspect", name))[0].Labels[ownershipLabel], runID);
      docker(kind, "rm", name);
    }
    fs.rmSync(directory, { recursive: true });
  });
  docker("network", "create", "--label", `${ownershipLabel}=${runID}`, network);
  docker("volume", "create", "--label", `${ownershipLabel}=${runID}`, volume);
  docker("run", "--detach", "--name", container, "--label", `${ownershipLabel}=${owner}`, "--network", network,
    "--mount", `type=volume,source=${volume},target=/owned`, image, "sh", "-c",
    local ? "trap 'exit 0' TERM; while :; do sleep 1; done" : 'trap "" TERM; echo ready; while :; do sleep 1; done');
  const state = { runID, containers: [container], network, volume, environment: { EVAL_API_KEY: randomUUID() } };
  fs.writeFileSync(path.join(directory, "runtime.json"), JSON.stringify(state), { mode: 0o600 });
  for (const file of ["server.env", "postgres.env"]) fs.writeFileSync(path.join(directory, file), "private fixture configuration", { mode: 0o600 });
  return { env, runID, directory, container, network, volume, checkpointFile };
}

test("owned cleanup bounds an unresponsive instance and removes only its runtime", { timeout: 180_000 }, async (t) => {
  requireRunner(process.env);
  docker("image", "pull", image);
  const runtime = scope(t);
  const deadline = Date.now() + 10_000;
  while (!docker("container", "logs", runtime.container).includes("ready")) {
    assert.ok(Date.now() < deadline, "fixture process did not install its signal handler");
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  await main("cleanup", runtime.env);
  for (const [kind, name] of [["container", runtime.container], ["network", runtime.network], ["volume", runtime.volume]]) assert.equal(exists(kind, name), false);
  assert.equal(JSON.parse(fs.readFileSync(path.join(runtime.directory, "runtime.json"))).environment, undefined);
  for (const file of ["server.env", "postgres.env"]) assert.equal(fs.existsSync(path.join(runtime.directory, file)), false);
  assert.ok(docker("image", "inspect", image));
});

test("cleanup rejects a foreign container before stopping or removing resources", { timeout: 30_000 }, async (t) => {
  requireRunner(process.env);
  const runtime = scope(t, true);
  await assert.rejects(main("cleanup", runtime.env), /ownership/);
  assert.equal(JSON.parse(docker("container", "inspect", runtime.container))[0].State.Running, true);
  for (const [kind, name] of [["network", runtime.network], ["volume", runtime.volume]]) assert.equal(exists(kind, name), true);
  assert.ok(JSON.parse(fs.readFileSync(path.join(runtime.directory, "runtime.json"))).environment);
  assert.equal(fs.existsSync(path.join(runtime.directory, "server.env")), true);
});

test("local seed cleanup preserves only its owned checkpoint until completion", { timeout: 40_000 }, async (t) => {
  const runtime = scope(t, false, true);
  docker("exec", runtime.container, "sh", "-c", "printf committed-row > /owned/receipt");
  const checkpoint = { run_id: runtime.runID, volume: runtime.volume };
  fs.writeFileSync(runtime.checkpointFile, JSON.stringify({ ...checkpoint, volume: "foreign" }), { mode: 0o600 });
  await assert.rejects(cleanup(runtime.env, { retainVolume: true }), /ownership differs/);
  assert.equal(exists("container", runtime.container), true);
  fs.writeFileSync(runtime.checkpointFile, JSON.stringify(checkpoint), { mode: 0o600 });
  await cleanup(runtime.env, { retainVolume: true });
  assert.equal(exists("container", runtime.container), false);
  assert.equal(exists("network", runtime.network), false);
  assert.equal(exists("volume", runtime.volume), true);
  for (const file of ["server.env", "postgres.env"]) assert.equal(fs.existsSync(path.join(runtime.directory, file)), false);
  assert.equal(docker("run", "--rm", "--mount", `type=volume,source=${runtime.volume},target=/owned,readonly`, image, "cat", "/owned/receipt"), "committed-row");
  await cleanup(runtime.env);
  assert.equal(exists("volume", runtime.volume), false);
});
