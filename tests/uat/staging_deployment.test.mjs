import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { runInNewContext } from "node:vm";
import { test } from "node:test";

const execute = promisify(execFile);
const helper = fileURLToPath(new URL("../../.github/scripts/deploy-staging-image.sh", import.meta.url));
const revision = "a".repeat(40);
const otherRevision = "b".repeat(40);
const docker = async (...args) => (await execute("docker", args, { maxBuffer: 4 * 1024 * 1024 })).stdout.trim();

async function freePort() {
  const server = createServer();
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const port = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return port;
}

function job(workflow, name) {
  const start = workflow.indexOf(`\n  ${name}:\n`);
  assert.notEqual(start, -1, `missing ${name}`);
  return workflow.slice(start + 1).split(/\n(?=  [a-z][\w-]*:\n)/)[0];
}

test("automatic recovery remains reachable after publication failure and shares staging serialization", async () => {
  const [automatic, manual, rehearsal] = await Promise.all(["release-rc", "deploy-stage", "staging-migration-rehearsal"].map((name) => readFile(new URL(`../../.github/workflows/${name}.yml`, import.meta.url), "utf8")));
  const recovery = job(automatic, "recover-staging");
  assert.match(recovery, /- publish-image\n      - promote-preview-image/);
  assert.doesNotMatch(recovery, /publish-demo-image|publish-python|continue-on-error/);
  const condition = recovery.match(/if: >-\n([\s\S]*?)\n    permissions:/)[1].trim().slice(3, -2)
    .replace(/needs\.([\w-]+)/g, 'needs["$1"]');
  for (const publication of ["success", "failure", "skipped"]) {
    const needs = { "classify-release": { result: "success", outputs: { should_release: "true" } }, "publish-image": { result: publication }, "promote-preview-image": { result: "skipped" } };
    assert.equal(runInNewContext(condition, { needs, always: () => true, cancelled: () => false }), true, publication);
    assert.equal(runInNewContext(condition, { needs, always: () => true, cancelled: () => true }), false);
    needs["classify-release"].outputs.should_release = "false";
    assert.equal(runInNewContext(condition, { needs, always: () => true, cancelled: () => false }), false);
  }
  for (const caller of [automatic, manual]) {
    assert.match(caller, /group: release-version-tags\n  cancel-in-progress: false\n  queue: max/);
    assert.match(caller, /EXPECTED_REVISION: \$\{\{ needs\.staging-migration-rehearsal\.outputs\.revision \}\}/);
    assert.match(caller, /deploy-staging-image\.sh \\\n            "\$\{HOME\}\/dense-mem-stage" "\$\{IMAGE_TAG\}" "\$\{EXPECTED_REVISION\}"/);
  }
  const queue = rehearsal.match(/group: \$\{\{ (.+) \}\}/)[1];
  const evaluateQueue = (workflow) => runInNewContext(queue, { github: { workflow, run_id: 42 }, format: (template, id) => template.replace("{0}", id) });
  assert.equal(evaluateQueue("Staging migration rehearsal"), "release-version-tags");
  assert.equal(evaluateQueue("Release prerelease"), "staging-rehearsal-42");
  assert.equal(evaluateQueue("Deploy staging"), "staging-rehearsal-42");
  assert.match(rehearsal, /value: \$\{\{ jobs\.staging-migration-rehearsal\.outputs\.revision \}\}/);
  assert.ok(rehearsal.indexOf('echo "revision=${revision}"') > rehearsal.indexOf("-timeout=40m"));

  const directory = await mkdtemp(join(tmpdir(), "dense-mem-staging-policy-"));
  try {
    const guard = recovery.split("        run: |\n")[1].split(/\n      - name:/)[0].split("\n").map((line) => line.slice(10)).join("\n");
    for (const [result, tag, source] of [["success", "v1.0.0-rc.1", revision], ["failure", "v1.0.0-rc.1", revision], ["success", "", revision], ["success", "v1.0.0-rc.1", otherRevision]]) {
      const env = { ...process.env, REHEARSAL_RESULT: result, IMAGE_TAG: tag, EXPECTED_REVISION: revision, SOURCE_REVISION: source, GITHUB_STEP_SUMMARY: join(directory, "summary") };
      const command = execute("bash", ["-c", guard], { env });
      if (result === "success" && tag && source === revision) await command;
      else await assert.rejects(command, /Staging recovery requires a successful rehearsal/);
    }
    assert.match(await readFile(join(directory, "summary"), "utf8"), /dispatch Deploy staging from main with a compatible image tag/);
  } finally {
    await rm(directory, { recursive: true });
  }
});

test("real Compose deploys only a verified pinned image and leaves failed candidates stopped", { timeout: 240000 }, async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-staging-deploy-"));
  const project = directory.split("/").at(-1).toLowerCase();
  const ownedTags = [];
  let registry = "";
  let repository = "";
  let holder;
  const env = () => ({ ...process.env, DENSE_MEM_IMAGE: "good", GITHUB_STEP_SUMMARY: join(directory, "summary"), GITHUB_OUTPUT: join(directory, "outputs") });
  const compose = async (...args) => (await execute("docker", ["compose", ...args], { cwd: directory, env: env() })).stdout.trim();
  t.after(async () => {
    const failures = [];
    for (const cleanup of [
      async () => { if (holder && holder.exitCode === null) { holder.stdin.end("release\n"); await once(holder, "exit"); } },
      async () => { if (repository) await compose("down", "--remove-orphans"); },
      async () => { if (registry) await docker("rm", "-f", registry); },
      ...ownedTags.map((tag) => () => docker("image", "rm", tag)),
      () => rm(directory, { recursive: true }),
    ]) {
      try { await cleanup(); } catch (error) { failures.push(error); }
    }
    if (failures.length) throw new AggregateError(failures, "Deployment fixture cleanup failed");
  });
  const port = await freePort();
  registry = await docker("run", "-d", "--rm", "--network", "host", "-e", `REGISTRY_HTTP_ADDR=127.0.0.1:${port}`, "registry:2");
  repository = `127.0.0.1:${port}/staging-regression`;
  for (let attempt = 0; ; attempt++) {
    try { if ((await fetch(`http://127.0.0.1:${port}/v2/`)).ok) break; } catch {}
    assert.ok(attempt < 100, "registry must become ready");
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  await writeFile(join(directory, "Dockerfile"), `FROM alpine:3.24
ARG REVISION
ARG VARIANT=production
ARG HEALTHY=true
ARG SLOW_STOP=false
LABEL org.opencontainers.image.revision=$REVISION org.opencontainers.image.variant=$VARIANT
RUN if [ "$HEALTHY" = true ]; then touch /healthy; fi
RUN if [ "$SLOW_STOP" = true ]; then touch /slow-stop; fi
HEALTHCHECK --interval=1s --timeout=1s --retries=1 CMD test -f /healthy
CMD ["sh", "-c", "if [ -f /slow-stop ]; then trap '' TERM INT; else trap 'exit 0' TERM INT; fi; while :; do sleep 1; done"]
`);
  await writeFile(join(directory, "compose.yaml"), `name: ${project}
services:
  server:
    image: ${repository}:\${DENSE_MEM_IMAGE:?image required}
  keeper:
    image: ${repository}:good
`);
  for (const [tag, source, variant, healthy, slowStop = "false"] of [["good", revision, "production", "true"], ["wrong", otherRevision, "production", "true"], ["demo", revision, "demo", "true"], ["unhealthy", revision, "production", "false"], ["slow-stop", revision, "production", "false", "true"]]) {
    const name = `${repository}:${tag}`;
    ownedTags.push(name);
    await docker("build", "--quiet", "--build-arg", `REVISION=${source}`, "--build-arg", `VARIANT=${variant}`, "--build-arg", `HEALTHY=${healthy}`, "--build-arg", `SLOW_STOP=${slowStop}`, "--tag", name, directory);
    await docker("push", name);
  }
  const deploy = (tag, source = revision) => execute("bash", [helper, directory, tag, source], { env: env(), maxBuffer: 4 * 1024 * 1024 });
  const serverID = () => compose("ps", "--all", "--quiet", "server");
  const running = async (id) => docker("inspect", "--format", "{{.State.Running}}", id);
  await compose("up", "-d", "keeper");
  const keeper = await compose("ps", "--quiet", "keeper");

  await t.test("matching source starts the pinned production image", async () => {
    await deploy("good");
    const server = await serverID();
    assert.equal(await running(server), "true");
    assert.match(await docker("inspect", "--format", "{{.Config.Image}}", server), /:good@sha256:[0-9a-f]{64}$/);
    assert.equal(await docker("inspect", "--format", '{{index .Config.Labels "org.opencontainers.image.revision"}}', server), revision);
    assert.match(await readFile(join(directory, "outputs"), "utf8"), /image_digest=sha256:[0-9a-f]{64}/);
    await compose("stop", "--timeout", "3", "server");
  });
  const stoppedServer = await serverID();
  for (const [tag, message] of [["wrong", /revision does not match/], ["demo", /not the production variant/], ["missing", /requested image is unavailable/]]) {
    await t.test(`${tag} cannot restart the stopped server`, async () => {
      await assert.rejects(deploy(tag), message);
      assert.equal(await serverID(), stoppedServer);
      assert.equal(await running(stoppedServer), "false");
      assert.equal(await running(keeper), "true");
    });
  }
  await t.test("a moved tag cannot substitute a different revision", async () => {
    const moving = `${repository}:moving`;
    ownedTags.push(moving);
    await docker("tag", `${repository}:good`, moving);
    await docker("push", moving);
    await deploy("moving");
    await compose("stop", "--timeout", "3", "server");
    await docker("tag", `${repository}:wrong`, moving);
    await docker("push", moving);
    await assert.rejects(deploy("moving"), /revision does not match/);
    assert.equal(await running(await serverID()), "false");
  });
  await t.test("an unhealthy candidate is stopped without restarting another service", async () => {
    await assert.rejects(deploy("unhealthy"), /did not become healthy/);
    assert.equal(await running(await serverID()), "false");
    assert.equal(await running(keeper), "true");
    assert.match(await readFile(join(directory, "summary"), "utf8"), /Healthy: `false`[\s\S]*No previous image was restarted/);
  });
  await t.test("the deployment deadline stops a starting candidate and completes cleanup", async () => {
    const previousServer = await serverID();
    const override = join(directory, "deadline.yaml");
    const summary = join(directory, "deadline-summary");
    await writeFile(override, "services:\n  server:\n    healthcheck:\n      start_period: 1m\n");
    await assert.rejects(execute("timeout", ["--signal=TERM", "--kill-after=60s", "--verbose", "15s", "bash", helper, directory, "unhealthy", revision], {
      env: { ...env(), COMPOSE_FILE: `${join(directory, "compose.yaml")}:${override}`, GITHUB_STEP_SUMMARY: summary },
      maxBuffer: 4 * 1024 * 1024,
    }), (error) => error.code === 124);
    const candidate = await serverID();
    assert.notEqual(candidate, previousServer);
    assert.ok(Date.parse(await docker("inspect", "--format", "{{.State.StartedAt}}", candidate)) > 0);
    assert.equal(await running(candidate), "false");
    assert.equal(await running(keeper), "true");
    assert.match(await readFile(summary, "utf8"), /Healthy: `false`[\s\S]*No previous image was restarted/);
  });
  await t.test("the real migration lock fences deployment until the holder releases it", async () => {
    holder = spawn("bash", ["-c", 'exec 9>"$1"; flock 9; printf ready; read -r release', "staging-lock", join(directory, "migration-rehearsal.lock")], { stdio: ["pipe", "pipe", "pipe"] });
    await once(holder.stdout, "data");
    let completed = false;
    const pending = deploy("good").finally(() => { completed = true; });
    try {
      await new Promise((resolve) => setTimeout(resolve, 250));
      assert.equal(completed, false);
      assert.equal(await running(await serverID()), "false");
    } finally {
      holder.stdin.end("release\n");
      await once(holder, "exit");
      await pending;
    }
    assert.equal(await running(await serverID()), "true");
  });
  await t.test("a deadline arriving during failed-candidate cleanup preserves stop and summary", async () => {
    const summary = join(directory, "cleanup-deadline-summary");
    await assert.rejects(execute("timeout", ["--signal=TERM", "--kill-after=60s", "--verbose", "25s", "bash", helper, directory, "slow-stop", revision], {
      env: { ...env(), GITHUB_STEP_SUMMARY: summary },
      maxBuffer: 4 * 1024 * 1024,
    }), (error) => error.code === 124 && /The verified candidate did not become healthy/.test(error.stderr));
    assert.equal(await running(await serverID()), "false");
    assert.equal(await running(keeper), "true");
    assert.match(await readFile(summary, "utf8"), /Healthy: `false`[\s\S]*Failure: The verified candidate did not become healthy/);
  });
});
