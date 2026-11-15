import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { requireRunner } from "../../../.github/scripts/evaluation-policy.mjs";
import { main } from "./workflow.mjs";

requireRunner(process.env);
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

function scope(t, foreign = false) {
  const dataset = `cleanup_${randomUUID().replaceAll("-", "")}`;
  const runID = `${process.env.GITHUB_RUN_ID}-${dataset}`;
  const prefix = `densemem-eval-${runID}`;
  const directory = path.join(process.env.RUNNER_TEMP, `dense-mem-eval-${runID}`);
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
    "--mount", `type=volume,source=${volume},target=/owned`, image, "sh", "-c", 'trap "" TERM; echo ready; while :; do sleep 1; done');
  const state = { runID, containers: [container], network, volume, environment: { EVAL_API_KEY: randomUUID() } };
  fs.writeFileSync(path.join(directory, "runtime.json"), JSON.stringify(state), { mode: 0o600 });
  for (const file of ["server.env", "postgres.env"]) fs.writeFileSync(path.join(directory, file), "private fixture configuration", { mode: 0o600 });
  return { env: { ...process.env, EVAL_DATASET: dataset }, directory, container, network, volume };
}

test("owned cleanup bounds an unresponsive instance and removes only its runtime", { timeout: 180_000 }, async (t) => {
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
  const runtime = scope(t, true);
  await assert.rejects(main("cleanup", runtime.env), /ownership/);
  assert.equal(JSON.parse(docker("container", "inspect", runtime.container))[0].State.Running, true);
  for (const [kind, name] of [["network", runtime.network], ["volume", runtime.volume]]) assert.equal(exists(kind, name), true);
  assert.ok(JSON.parse(fs.readFileSync(path.join(runtime.directory, "runtime.json"))).environment);
  assert.equal(fs.existsSync(path.join(runtime.directory, "server.env")), true);
});
