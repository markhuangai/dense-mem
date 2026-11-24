import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";
import fs from "node:fs";
import { createServer } from "node:http";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { requireRunner } from "../../../.github/scripts/evaluation-policy.mjs";
import { cleanup, location, main, prepareFixtureSeed, runBinary, saveRuntimeJSON, setup } from "./workflow.mjs";

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

test("hosted cutoff stops the owned server before capture and preserves verified scores", { timeout: 900_000 }, async (t) => {
  requireRunner(process.env);
  const fixture = fs.mkdtempSync(path.join(process.env.RUNNER_TEMP, "evaluation-deadline-"));
  const env = { ...process.env, EVAL_MODE: "fixtures", EVAL_DATASET: "hotpotqa" };
  const fixtureEnv = () => ({ ...env, GITHUB_RUN_ID: `${env.GITHUB_RUN_ID}-fixture-${randomUUID()}` });
  const preparedEnv = fixtureEnv();
  const runEnvs = [fixtureEnv(), fixtureEnv()];
  let seed;
  t.after(async () => {
    for (const ownedEnv of [preparedEnv, ...runEnvs]) {
      const scoring = ownedEnv !== preparedEnv;
      const { directory, stateFile } = location(ownedEnv, scoring);
      if (fs.existsSync(stateFile)) await cleanup(ownedEnv, { scoring });
      fs.rmSync(directory, { recursive: true, force: true });
    }
    if (seed) {
      const stored = JSON.parse(docker("image", "inspect", seed.seedImage))[0];
      assert.equal(stored.Id, seed.seedImage);
      docker("image", "rm", seed.seedImage);
    }
    fs.rmSync(fixture, { recursive: true });
  });
  seed = await prepareFixtureSeed(preparedEnv);
  const foreign = scope(t, true);
  const realDocker = spawnSync("sh", ["-c", "command -v docker"], { encoding: "utf8" }).stdout.trim();
  assert.ok(path.isAbsolute(realDocker));
  const instrumentedDocker = path.join(fixture, "docker");
  fs.writeFileSync(instrumentedDocker, `#!/bin/sh
set -eu
if [ "$1" = container ] && [ "$2" = logs ]; then
  "$EVAL_FIXTURE_DOCKER" container inspect --format '{{.State.Running}}' "$3" > "$EVAL_FIXTURE_EXIT_PROOF"
  while [ "$(date +%s)" -le "$EVAL_FIXTURE_PRESERVE_UNTIL" ]; do sleep 1; done
fi
exec "$EVAL_FIXTURE_DOCKER" "$@"
`, { mode: 0o700 });
  const runner = path.join(fixture, "runner");
  fs.writeFileSync(runner, `#!/bin/sh
exec "$EVAL_FIXTURE_RUNNER" "$@" > "$EVAL_FIXTURE_LOG" 2>&1
`, { mode: 0o700 });

  let unfinishedRequests = 0;
  const heldAnswer = createServer(async (request, response) => {
    let body = "";
    for await (const chunk of request) body += chunk;
    const messages = JSON.parse(body).messages;
    const question = JSON.parse(messages.findLast(({ role }) => role === "user").content).question;
    if (question.includes("coordination")) {
      unfinishedRequests++;
      return;
    }
    const answer = { action: "answer", query: "", topic_id: "", evidence_id: "", answer: "PostgreSQL", citations: [] };
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({ choices: [{ message: { content: JSON.stringify(answer) }, finish_reason: "stop" }],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } }));
  });
  await new Promise((resolve) => heldAnswer.listen(0, "127.0.0.1", resolve));
  t.after(() => { heldAnswer.closeAllConnections(); heldAnswer.close(); });
  for (const [index, runEnv] of runEnvs.entries()) {
    const verified = index === 1;
    const runtime = await setup(runEnv, seed);
    const container = `densemem-eval-${runtime.paths.runID}-server`;
    const marker = JSON.stringify({ level: "WARN", msg: "terminal capture canary", detail: runtime.state.environment.EVAL_CONTROL_TOKEN });
    docker("container", "exec", container, "sh", "-c", 'printf "%s\n" "$1" >> /proc/1/fd/1', "fixture", marker);
    const deadline = Math.floor(Date.now() / 1000) + (verified ? 150 : 12);
    runtime.manifest.model_deadline = new Date(deadline * 1000).toISOString();
    saveRuntimeJSON(runtime.paths.manifestFile, runtime.manifest);
    const proof = path.join(fixture, `exit-${index}`);
    const output = path.join(fixture, `console-${index}`);
    const environment = {
      ...runEnv, PATH: `${fixture}:${runEnv.PATH}`, EVAL_MODEL_DEADLINE_EPOCH: String(deadline),
      EVAL_RUNNER_BINARY: runner, EVAL_FIXTURE_DOCKER: realDocker, EVAL_FIXTURE_EXIT_PROOF: proof,
      EVAL_FIXTURE_LOG: output, EVAL_FIXTURE_RUNNER: runEnv.EVAL_RUNNER_BINARY,
      EVAL_FIXTURE_PRESERVE_UNTIL: verified ? String(deadline) : "0",
    };
    const runRuntime = verified ? runtime : {
      ...runtime,
      state: { ...runtime.state, environment: { ...runtime.state.environment, DENSE_MEM_EVAL_VERIFIER_URL: `http://127.0.0.1:${heldAnswer.address().port}/v1` } },
    };
    if (verified) await runBinary("run", runRuntime, environment);
    else await assert.rejects(runBinary("run", runRuntime, environment), /Runner binary operation failed/);
    const report = JSON.parse(fs.readFileSync(path.join(runtime.manifest.output_dir, "scores.json"), "utf8"));
    const console = fs.readFileSync(output, "utf8");
    assert.equal(fs.readFileSync(proof, "utf8").trim(), "false", "capture opened only after owned exit");
    assert.ok(console.includes("terminal capture canary"));
    assert.equal(console.includes(runtime.state.environment.EVAL_CONTROL_TOKEN), false, "terminal capture protects credentials");
    assert.equal(report.complete, verified);
    assert.equal(report.model_verified_before_cutoff, verified);
    if (verified) {
      assert.equal(report.capture_complete, true);
      assert.equal(report.completed_answers, 4);
      assert.equal(report.identity.condition, "off");
      assert.ok(report.timings_ms.scoring_through_flush > 0);
      assert.ok(Date.now() > deadline * 1000, "completed model result finished capture in the preservation window");
    } else {
      assert.ok(unfinishedRequests > 0);
      assert.match(report.failure, /ontology off incomplete|log capture incomplete/);
      assert.equal(report.completed_answers, 2);
      const records = console.split("\n").filter((line) => line.startsWith("{")).map(JSON.parse);
      const terminal = records.find(({ kind, payload }) => kind === "answer_wire_unavailable" && payload?.state === "no_response");
      assert.ok(terminal, "cutoff must preserve the explicit no-response outcome");
      assert.ok(records.some(({ kind, payload }) => kind === "answer_response" && payload.operation_id === terminal.payload.operation_id &&
        payload.attempt.number === terminal.payload.attempt), "cutoff must preserve the attempt usage state");
      const requests = records.filter(({ kind }) => kind === "answer_wire_request");
      const sequences = new Set(requests.map(({ sequence }) => sequence));
      const decoded = [...sequences].map((sequence) => {
        const group = requests.filter((record) => record.sequence === sequence);
        if (group[0].payload) return group[0].payload;
        const chunks = group.map(({ payload_chunk }) => payload_chunk).sort((a, b) => a.index - b.index);
        const body = chunks.map((chunk) => chunk.body).join("");
        assert.equal(chunks.length, chunks[0].count);
        assert.equal(createHash("sha256").update(body).digest("hex"), chunks[0].sha256);
        return JSON.parse(body).payload;
      });
      assert.ok(decoded.some(({ exchange_id }) => exchange_id === terminal.payload.exchange_id), "wire request and terminal must share an exchange ID");
    }
    await cleanup(runEnv, { scoring: true });
    const receipt = JSON.parse(fs.readFileSync(path.join(runtime.manifest.output_dir, "cleanup.json"), "utf8"));
    assert.deepEqual(receipt, { run_id: report.identity.run_id, complete: true });
  }
  assert.equal(docker("container", "inspect", "--format", "{{.State.Running}}", foreign.container), "true");
});
