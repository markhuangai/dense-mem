import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { chmod, mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { test } from "node:test";
import { execFile, spawn } from "node:child_process";
import { once } from "node:events";
import { promisify } from "node:util";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { devNull, tmpdir, userInfo } from "node:os";
import { createServer } from "node:http";
import { createRedactor, redactChunks, valueFromEnvFile } from "../../scripts/e2e-redact-diagnostics.mjs";
import { actionStep, jobScripts, parseWorkflow, runStep, stringifyWorkflow, workflowJob } from "./workflow_helpers.mjs";

const execFileAsync = promisify(execFile);

const root = fileURLToPath(new URL("../..", import.meta.url));
const scripts = join(root, "scripts");
const [
  controller,
  stack,
  runtime,
  postgres,
  scenario,
  local,
  compose,
  productionWorkflow,
  scenarioWorkflow,
  packageJSON,
  realController,
  pythonE2E,
] = await Promise.all([
  readFile(join(scripts, "e2e-host-controller.sh"), "utf8"),
  readFile(join(scripts, "e2e-host-controller-stack.sh"), "utf8"),
  readFile(join(scripts, "e2e-host-controller-runtime.sh"), "utf8"),
  readFile(join(scripts, "e2e-host-controller-postgres.sh"), "utf8"),
  readFile(join(scripts, "e2e-scenario.sh"), "utf8"),
  readFile(join(scripts, "e2e.sh"), "utf8"),
  readFile(join(scripts, "e2e-stack.yml"), "utf8"),
  readFile(join(root, ".github/workflows/production-image-e2e.yml"), "utf8"),
  readFile(join(root, ".github/workflows/production-e2e-scenario.yml"), "utf8"),
  readFile(join(root, "package.json"), "utf8"),
  readFile(join(root, "tests/uat/e2e_host_controller_real.sh"), "utf8"),
  readFile(join(root, "packages/python/scripts/e2e.sh"), "utf8"),
]);

async function executable(path, contents) {
  await writeFile(path, contents, { mode: 0o700 });
  await chmod(path, 0o700);
}

function pythonDependencySetup() {
  const setup = pythonE2E.match(/node --input-type=module <<'NODE'\n([\s\S]*?)\nNODE/);
  assert.ok(setup, "Python setup must retain its executable dependency owner");
  return setup[1];
}

test("Python dependency setup retains inherited resolvers and records successful phases", async () => {
  const setup = pythonDependencySetup();
  const helper = setup.slice(0, setup.indexOf('\nsetupPhase("apt_index_refresh"'));
  const { stdout } = await run(process.execPath, ["--input-type=module", "-e", `${helper}\nsetupPhase("apt_index_refresh", process.execPath, ["-e", "console.log('exact admitted setup output')"]);`], {
    env: { ...process.env, DENSE_MEM_E2E_RUN_ID: "741", DENSE_MEM_E2E_RUN_ATTEMPT: "1", DENSE_MEM_E2E_SCENARIO: "python_client", DENSE_MEM_E2E_COMMIT_SHA: "a".repeat(40), AI_API_KEY: "private-setup-canary" },
  });
  const events = stdout.trim().split("\n").filter((line) => line.startsWith("{")).map((line) => JSON.parse(line));
  assert.equal(events.length, 2);
  assert.deepEqual(events.map(({ outcome, level }) => [outcome, level]), [["started", "INFO"], ["passed", "INFO"]]);
  const nameservers = [...(await readFile("/etc/resolv.conf", "utf8")).matchAll(/^\s*nameserver\s+(\S+)/gm)].map((match) => match[1]);
  for (const event of events) {
    assert.equal(event.event, "python_dependency_setup");
    assert.equal(event.phase, "apt_index_refresh");
    assert.equal(event.run_id, "741");
    assert.equal(event.run_attempt, "1");
    assert.equal(event.scenario, "python_client");
    assert.equal(event.source_revision, "a".repeat(40));
    assert.equal(event.resolver_capture_state, "captured");
    assert.deepEqual(event.nameservers, nameservers);
    assert.ok(event.elapsed_ms >= 0);
  }
  assert.equal(events[1].exit_code, 0);
  assert.match(stdout, /exact admitted setup output/);
  assert.doesNotMatch(stdout, /private-setup-canary/);
});

test("Python dependency setup preserves failing and signalled command outcomes", async () => {
  const setup = pythonDependencySetup();
  const helper = setup.slice(0, setup.indexOf('\nsetupPhase("apt_index_refresh"'));
  for (const [command, status, signal] of [["console.error('setup command failed');process.exit(23)", 23, null], ["process.kill(process.pid,'SIGTERM')", 143, "SIGTERM"]]) {
    await assert.rejects(run(process.execPath, ["--input-type=module", "-e", `${helper}\nsetupPhase("apt_package_install", process.execPath, ["-e", ${JSON.stringify(command)}]);`]), (error) => {
      assert.equal(error.code, status);
      const events = error.stdout.trim().split("\n").map((line) => JSON.parse(line));
      assert.equal(events[0].outcome, "started");
      assert.equal(events[1].outcome, "failed");
      assert.equal(events[1].level, "ERROR");
      assert.equal(events[1].phase, "apt_package_install");
      assert.equal(events[1].exit_code, status);
      assert.equal(events[1].signal, signal);
      if (!signal) assert.match(error.stderr, /setup command failed/);
      return true;
    });
  }
});

test("Python dependency setup stops after a real APT index download failure", { timeout: 20000 }, async (t) => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-python-apt-"));
  let requests = 0;
  const server = createServer((_request, response) => {
    requests += 1;
    response.writeHead(503, "Service Unavailable");
    response.end("temporary fixture outage");
  });
  try {
    await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
    for (const directory of ["lists/partial", "archives/partial", "cache", "log", "apt.conf.d", "sources.list.d"]) await mkdir(join(fixture, directory), { recursive: true });
    await writeFile(join(fixture, "status"), "");
    await writeFile(join(fixture, "main.conf"), "");
    await writeFile(join(fixture, "sources.list"), `deb [trusted=yes] http://127.0.0.1:${server.address().port} stable main\n`);
    const config = [
      `Dir::Etc "${fixture}";`, `Dir::Etc::parts "${fixture}/apt.conf.d";`, `Dir::Etc::main "${fixture}/main.conf";`,
      `Dir::Etc::sourcelist "${fixture}/sources.list";`, `Dir::Etc::sourceparts "${fixture}/sources.list.d";`,
      `Dir::State "${fixture}";`, `Dir::State::lists "${fixture}/lists";`, `Dir::State::status "${fixture}/status";`,
      `Dir::Cache "${fixture}/cache";`,
      `Dir::Cache::archives "${fixture}/archives";`, `Dir::Log "${fixture}/log";`,
      'Dir::Bin::dpkg "/bin/false";', `APT::Sandbox::User "${userInfo().username}";`,
      'Acquire::Retries "0";', 'Acquire::http::Timeout "2";', 'Acquire::http::Proxy "DIRECT";',
    ].join("\n");
    await writeFile(join(fixture, "apt.conf"), `${config}\n`);
    await assert.rejects(run(process.execPath, ["--input-type=module", "-e", pythonDependencySetup()], {
      env: { ...process.env, APT_CONFIG: join(fixture, "apt.conf") }, timeout: 15000,
    }), (error) => {
      assert.equal(error.code, 100);
      const events = error.stdout.split("\n").filter((line) => line.startsWith("{")).map((line) => JSON.parse(line));
      assert.deepEqual(events.map(({ phase, outcome }) => [phase, outcome]), [["apt_index_refresh", "started"], ["apt_index_refresh", "failed"]]);
      assert.equal(events[1].exit_code, 100);
      assert.match(error.stderr, /503\s+Service Unavailable/);
      assert.ok(requests > 0, "the real APT client must reach the loopback repository");
      t.diagnostic(JSON.stringify({ event: "python_dependency_failure_reproduction", phases: events, apt_error: error.stderr }));
      return true;
    });
  } finally {
    await new Promise((resolve) => server.close(resolve));
    await rm(fixture, { recursive: true, force: true });
  }
});

test("production Compose bundle renders with distinct retained volume identities", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-compose-contract-"));
  try {
    const envFile = join(fixture, ".env");
    await writeFile(envFile, "");
    const environment = { ...process.env };
    for (const [, name] of compose.matchAll(/\$\{([A-Z0-9_]+):\?/g)) {
      environment[name] = `densemem-static-${name.toLowerCase().replaceAll("_", "-")}`;
    }
    environment.DENSE_MEM_CI_ENV_FILE = envFile;
    environment.DENSE_MEM_IMAGE = "densemem-static:fixture";
    const { stdout } = await run("docker", ["compose", "--env-file", envFile, "--project-name", "densemem-static", "--profile", "*", "--file", join(scripts, "e2e-stack.yml"), "config", "--format", "json"], { env: environment });
    const rendered = JSON.parse(stdout);
    assert.equal(rendered.volumes["client-env"].name, environment.DENSE_MEM_CI_CLIENT_VOLUME_NAME);
    assert.equal(rendered.volumes["postgres-data"].name, environment.DENSE_MEM_CI_POSTGRES_VOLUME_NAME);
    const names = Object.values(rendered.volumes).map((volume) => volume.name);
    assert.equal(new Set(names).size, names.length);
    assert(Object.values(rendered.services).every((service) => !(service.ports?.length)));
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("ADK scenario enables private intake and dispatches its installed-wheel harness", () => {
  assert.match(stack, /scenario === "google_adk_memory"[^\n]*serverEnvironment.set\("SESSION_INGEST_ENABLED", "true"\)/);
  assert.match(scenario, /google_adk_memory\) bash "\$\{ROOT_DIR\}\/packages\/python\/scripts\/e2e.sh" google_adk_memory ;;/);
});

test("Client overlays reserve a bounded request budget", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-adk-overlay-"));
  try {
    const commandStart = stack.indexOf('    node - "$DENSE_MEM_CI_COMPOSE_OVERLAY_FILE"');
    const marker = "<<'NODE'\n";
    const start = stack.indexOf(marker, commandStart) + marker.length;
    const end = stack.indexOf("\nNODE\n", start);
    assert.ok(commandStart >= 0 && start > commandStart && end > start);
    const generator = join(fixture, "overlay.cjs");
    await writeFile(generator, stack.slice(start, end));
    for (const name of ["google_adk_memory", "langchain_retriever", "python_client", "session_ingest", "synchronous_write", "synchronous_write_telemetry_disabled"]) {
      const destination = join(fixture, `${name}.yml`);
      await run(process.execPath, [
        generator, destination, "synchronous_write", "", "", "fixture-model", "1536",
        name, "adk-overlay-test", "30",
      ], { env: { DENSE_MEM_CI_HOSTED: "1" } });
      const overlay = await readFile(destination, "utf8");
      if (name === "langchain_retriever") {
        assert.doesNotMatch(overlay, /SESSION_INGEST_ENABLED/);
      } else {
        assert.match(overlay, /SESSION_INGEST_ENABLED: "true"/);
      }
      if (["google_adk_memory", "langchain_retriever", "python_client"].includes(name)) {
        assert.match(overlay, /RATE_LIMIT_PER_MINUTE: "1000"/);
      } else {
        assert.doesNotMatch(overlay, /RATE_LIMIT_PER_MINUTE/);
      }
      if (["python_client", "session_ingest", "synchronous_write", "synchronous_write_telemetry_disabled"].includes(name)) {
        assert.match(overlay, /LOG_LEVEL: "debug"/);
      } else {
        assert.doesNotMatch(overlay, /LOG_LEVEL/);
      }
    }
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

async function run(command, args, options = {}) {
  const { env: overrides, ...commandOptions } = options;
  const env = { ...process.env, ...overrides };
  for (const name of Object.keys(env)) {
    if (name.startsWith("GIT_")) delete env[name];
  }
  env.GIT_CONFIG_GLOBAL = devNull;
  env.GIT_CONFIG_NOSYSTEM = "1";
  return execFileAsync(command, args, { ...commandOptions, env, maxBuffer: 1024 * 1024 });
}

function nulLines(contents) {
  return contents
    .split("\n")
    .filter(Boolean)
    .map((line) => line.split("\0").filter(Boolean));
}

test("the shared controller owns CI and local lifecycle without legacy state", () => {
  assert.match(controller, /CONTRACT_VERSION="dense-mem-ci-e2e\.v1"/);
  for (const command of ["doctor", "start", "run", "stop", "stale-cleanup", "precheck"]) {
    assert.match(controller, new RegExp("e2e-host-controller[.]sh " + command));
  }
  assert.match(controller, /copy_worktree_source\(\)/);
  assert.match(controller, /git -C "\$source_dir" ls-files --cached --others --exclude-standard -z/);
  assert.match(controller, /DENSE_MEM_CI_LOCAL/);
  assert.match(controller, /docker context inspect/);
  assert.doesNotMatch(controller, /DENSE_MEM_CI_DAEMON_ID|DENSE_MEM_CI_DOCKER_SOCKET|LEASE_DIR|RUN_DIR|e2e-docker-proxy|e2e-runtime-adapter/);
  assert.doesNotMatch(controller, /\$\{source_dir\}:\/workspace|\$\{runtime_compose_host\}:|\$\{helper_overlay\}:|\$\{run_root\}\/results/);
  assert.match(runtime, /copy_git_source "\$container" "\$source_dir"/);
  assert.match(stack, /copy_git_source "\$container" "\$source_dir"/);
});

test("the default precheck partitions capabilities and propagates failures", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-precheck-partition-"));
  try {
    const wrapper = controller.slice(controller.lastIndexOf("\nprecheck() {") + 1, controller.indexOf("\ndoctor() {"));
    assert.ok(wrapper.includes("partition_precheck_capabilities"));
    const script = `#!/usr/bin/env bash
set -euo pipefail
${wrapper}
partition_precheck_capabilities() {
  printf 'capability_a,semantic-write\\ncapability_c,capability_d\\n'
}
precheck_capability() {
  printf 'capability=%s\\n' "$5"
  [[ "$5" != *capability_c* ]]
}
precheck 123 1 ghcr.io/markhuangai/dense-mem:test@sha256:${"1".repeat(64)} /workspace
`;
    const scriptPath = join(fixture, "partition-test.sh");
    await executable(scriptPath, script);
    await assert.rejects(
      run("bash", [scriptPath], { env: { TMPDIR: fixture } }),
      (error) => {
        const output = `${error.stdout || ""}${error.stderr || ""}`;
        assert.match(output, /capability=capability_a,semantic-write/);
        assert.match(output, /capability=capability_c,capability_d/);
        assert.doesNotMatch(output, /capability=repository|capability=postgres/);
        return true;
      },
    );
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("precheck shards select each complete capability group", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-precheck-shards-"));
  try {
    const wrapper = controller.slice(controller.lastIndexOf("\nprecheck() {") + 1, controller.indexOf("\ndoctor() {"));
    const script = [
      "#!/usr/bin/env bash",
      "set -euo pipefail",
      wrapper,
      "partition_precheck_capabilities() { printf 'alpha,beta\\ngamma,delta\\nepsilon\\n'; }",
      "precheck_capability() { printf 'selected=%s\\n' \"$5\"; }",
      "fail() { printf '%s\\n' \"$*\" >&2; exit 1; }",
      "precheck 123 1 image /workspace \"$1\"",
    ].join("\n");
    const scriptPath = join(fixture, "shard-test.sh");
    await executable(scriptPath, script);
    for (const [index, expected] of ["alpha,beta", "gamma,delta", "epsilon"].entries()) {
      const { stdout } = await run("bash", [scriptPath, "shard:" + index]);
      assert.equal(stdout.trim(), "selected=" + expected);
    }
    await assert.rejects(run("bash", [scriptPath, "shard:3"]), /invalid precheck shard/);
    const incompletePath = join(fixture, "incomplete-shard-test.sh");
    await executable(incompletePath, script.replace("alpha,beta\\ngamma,delta\\nepsilon\\n", "alpha,beta\\ngamma,delta\\n"));
    await assert.rejects(run("bash", [incompletePath, "shard:2"]), /requires 3 populated shards/);
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("default precheck streams partition progress before completion", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-precheck-stream-"));
  let child;
  try {
    const wrapper = controller.slice(controller.lastIndexOf("\nprecheck() {") + 1, controller.indexOf("\ndoctor() {"));
    const script = [
      "#!/usr/bin/env bash",
      "set -euo pipefail",
      wrapper,
      "partition_precheck_capabilities() { printf 'alpha\\nbeta\\n'; }",
      "precheck_capability() { printf 'ready %s\\n' \"$5\"; while [[ ! -f \"$RELEASE_FILE\" ]]; do sleep 0.02; done; }",
      "fail() { printf '%s\\n' \"$*\" >&2; exit 1; }",
      "precheck 123 1 image /workspace",
    ].join("\n");
    const scriptPath = join(fixture, "stream-test.sh");
    const releasePath = join(fixture, "release");
    await executable(scriptPath, script);
    child = spawn("bash", [scriptPath], {
      env: { ...process.env, TMPDIR: fixture, RELEASE_FILE: releasePath },
    });
    let output = "";
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error("precheck progress was not streamed")), 5000);
      child.stdout.on("data", (chunk) => {
        output += chunk;
        if (output.includes("[alpha] ready alpha") && output.includes("[beta] ready beta")) {
          clearTimeout(timer);
          resolve();
        }
      });
      child.on("error", (error) => {
        clearTimeout(timer);
        reject(error);
      });
      child.on("close", (code) => {
        if (code !== 0) {
          clearTimeout(timer);
          reject(new Error("precheck exited before release"));
        }
      });
    });
    assert.equal(child.exitCode, null);
    const completed = once(child, "close");
    await writeFile(releasePath, "go");
    const [code] = await completed;
    assert.equal(code, 0);
  } finally {
    child?.kill();
    await rm(fixture, { recursive: true, force: true });
  }
});

test("the default precheck propagates capability discovery failures", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-precheck-discovery-failure-"));
  try {
    const wrapper = controller.slice(controller.lastIndexOf("\nprecheck() {") + 1, controller.indexOf("\ndoctor() {"));
    const script = `#!/usr/bin/env bash
set -euo pipefail
${wrapper}
fail() { printf 'dense-mem CI controller: %s\\n' "$*" >&2; exit 1; }
partition_precheck_capabilities() {
  printf 'database case registry is malformed\\n' >&2
  return 1
}
precheck_capability() {
  printf 'unexpected capability execution\\n' >&2
  return 0
}
precheck 123 1 ghcr.io/markhuangai/dense-mem:test@sha256:${"1".repeat(64)} /workspace
`;
    const scriptPath = join(fixture, "discovery-failure-test.sh");
    await executable(scriptPath, script);
    await assert.rejects(
      run("bash", [scriptPath], { env: { TMPDIR: fixture } }),
      (error) => {
        const output = `${error.stdout || ""}${error.stderr || ""}`;
        assert.match(output, /database case registry is malformed/);
        assert.match(output, /unable to discover precheck database capabilities/);
        assert.doesNotMatch(output, /unexpected capability execution/);
        return true;
      },
    );
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("database case fragments drive complete three-way capability selection", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-precheck-registry-"));
  try {
    const sourceRoot = join(fixture, "repo");
    const casesDir = join(sourceRoot, "scripts/e2e-db-cases");
    await mkdir(casesDir, { recursive: true });
    const fragments = [
      ["alpha", "TestAlpha"],
      ["beta", "TestBeta"],
      ["delta", "TestDelta"],
      ["epsilon", "TestEpsilon"],
      ["semantic-write", "TestSemanticWrite"],
    ];
    for (const [capability, run] of fragments) {
      await writeFile(join(casesDir, `${capability}.json`), JSON.stringify({
        version: 1,
        capability,
        cases: [{ id: `${capability}/${run}`, package: "./fixture", run, phase: "precheck" }],
      }));
    }
    const start = controller.indexOf("\ndatabase_case_capabilities()") + 1;
    const end = controller.indexOf("\nprecheck() {", start);
    const helper = controller.slice(start, end);
    const script = `#!/usr/bin/env bash
set -euo pipefail
${helper}
fail() { printf '%s\\n' "$*" >&2; return 1; }
partition_precheck_capabilities "${sourceRoot}"
`;
    const scriptPath = join(fixture, "registry-test.sh");
    await executable(scriptPath, script);
    const { stdout } = await run("bash", [scriptPath]);
    assert.deepEqual(stdout.trim().split(/\r?\n/), [
      "alpha,epsilon",
      "beta,semantic-write",
      "delta",
    ]);
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("the repository precheck isolates ontology and selects every case once", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-precheck-ontology-"));
  try {
    const casesDir = join(scripts, "e2e-db-cases");
    const fragments = await Promise.all((await readdir(casesDir))
      .filter((entry) => entry.endsWith(".json"))
      .map(async (entry) => JSON.parse(await readFile(join(casesDir, entry), "utf8"))));
    const casesByCapability = new Map(fragments
      .map((fragment) => [fragment.capability, fragment.cases.filter((item) => item.phase === "precheck")])
      .filter(([, cases]) => cases.length > 0));
    const start = controller.indexOf("\ndatabase_case_capabilities()") + 1;
    const end = controller.indexOf("\nprecheck() {", start);
    const scriptPath = join(fixture, "ontology-registry-test.sh");
    await executable(scriptPath, `#!/usr/bin/env bash
set -euo pipefail
${controller.slice(start, end)}
partition_precheck_capabilities "$1"
`);
    const { stdout } = await run("bash", [scriptPath, root]);
    const groups = stdout.trim().split(/\r?\n/).map((group) => group.split(","));
    assert.equal(groups.length, 3);
    assert.deepEqual(groups[0], ["ontology"]);
    assert.deepEqual(groups.flat().sort(), [...casesByCapability.keys()].sort());
    const selected = groups.flatMap((group) => group
      .flatMap((capability) => casesByCapability.get(capability).map((item) => item.id)));
    const registered = [...casesByCapability.values()].flat().map((item) => item.id);
    assert.equal(new Set(selected).size, selected.length);
    assert.deepEqual(selected.sort(), registered.sort());
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

for (const includeOntology of [false, true]) {
  test(`weighted precheck partition ${includeOntology ? "isolates ontology and spreads other large fragments" : "spreads large capability fragments"}`, async () => {
    const fixture = await mkdtemp(join(tmpdir(), "dense-mem-precheck-weighted-"));
    try {
      const sourceRoot = join(fixture, "repo");
      const casesDir = join(sourceRoot, "scripts/e2e-db-cases");
      await mkdir(casesDir, { recursive: true });
      const fragments = [
        ["access", 80],
        ["audit", 1],
        ["community", 1],
        ["dream", 80],
        ["graph", 1],
        ["http", 1],
        ["knowledge", 80],
        ["migration", 1],
        ...(includeOntology ? [["ontology", 1]] : []),
        ["operations", 1],
        ["postgres", 80],
        ["privacy", 1],
        ["remember", 1],
        ["repository", 80],
        ["search", 1],
        ["settings", 1],
        ["trace", 80],
      ];
      for (const [capability, count] of fragments) {
        const cases = Array.from({ length: count }, (_, index) => ({
          id: `${capability}/Test${capability}${index}`,
          package: "./fixture",
          run: `^Test${capability}${index}$`,
          phase: "precheck",
        }));
        await writeFile(join(casesDir, `${capability}.json`), JSON.stringify({
          version: 1,
          capability,
          cases,
        }));
      }
      const start = controller.indexOf("\ndatabase_case_capabilities()") + 1;
      const end = controller.indexOf("\nprecheck() {", start);
      const helper = controller.slice(start, end);
      const script = `#!/usr/bin/env bash
  set -euo pipefail
  ${helper}
  fail() { printf '%s\\n' "$*" >&2; return 1; }
  partition_precheck_capabilities "${sourceRoot}"
  `;
      const scriptPath = join(fixture, "weighted-registry-test.sh");
      await executable(scriptPath, script);
      const { stdout } = await run("bash", [scriptPath]);
      const groups = stdout.trim().split(/\r?\n/).map((group) => group.split(","));
      assert.equal(groups.length, 3);
      if (includeOntology) assert.deepEqual(groups[0], ["ontology"]);
      assert.deepEqual(groups.flat().sort(), fragments.map(([capability]) => capability).sort());
      const heavy = new Set(["access", "dream", "knowledge", "postgres", "repository", "trace"]);
      assert.deepEqual(groups.map((group) => group.filter((capability) => heavy.has(capability)).length), includeOntology ? [0, 3, 3] : [2, 2, 2]);
    } finally {
      await rm(fixture, { recursive: true, force: true });
    }
  });
}

test("precheck partition balances cases when package weights overlap", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-precheck-case-balance-"));
  try {
    const sourceRoot = join(fixture, "repo");
    const casesDir = join(sourceRoot, "scripts/e2e-db-cases");
    await mkdir(casesDir, { recursive: true });
    const fragments = [
      ["http", { "./internal/http": 1, "./internal/sse": 2, "./internal/http/middleware": 1, "./internal/storage/redis": 1 }],
      ["postgres", { "./internal/storage/postgres": 97 }],
      ["knowledge", { "./internal/knowledge/postgres": 42, "./internal/storage/postgres": 6 }],
      ["repository", { "./internal/knowledge/postgres": 42 }],
      ["access", { "./internal/access/postgres": 26, "./internal/service": 7 }],
      ["dream", { "./internal/storage/postgres": 2, "./internal/dream/postgres": 27 }],
    ];
    for (const [capability, packages] of fragments) {
      const cases = [];
      for (const [packageName, count] of Object.entries(packages)) {
        for (let index = 0; index < count; index += 1) {
          cases.push({
            id: `${capability}/${packageName}/${index}`,
            package: packageName,
            run: `^Test${capability}${index}$`,
            phase: "precheck",
          });
        }
      }
      await writeFile(join(casesDir, `${capability}.json`), JSON.stringify({
        version: 1,
        capability,
        cases,
      }));
    }
    const start = controller.indexOf("\ndatabase_case_capabilities()") + 1;
    const end = controller.indexOf("\nprecheck() {", start);
    const helper = controller.slice(start, end);
    const script = `#!/usr/bin/env bash
set -euo pipefail
${helper}
fail() { printf '%s\\n' "$*" >&2; return 1; }
partition_precheck_capabilities "${sourceRoot}"
`;
    const scriptPath = join(fixture, "case-balance-test.sh");
    await executable(scriptPath, script);
    const { stdout } = await run("bash", [scriptPath]);
    const groups = stdout.trim().split(/\r?\n/).map((group) => group.split(","));
    assert.equal(groups.length, 3);
    const casesByCapability = new Map(fragments.map(([capability, packages]) => [
      capability,
      Object.values(packages).reduce((total, count) => total + count, 0),
    ]));
    const groupCaseCounts = groups.map((group) => group.reduce(
      (total, capability) => total + casesByCapability.get(capability),
      0,
    ));
    assert.ok(Math.max(...groupCaseCounts) - Math.min(...groupCaseCounts) <= 20);
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("capability-specific precheck project names stay bounded and distinct", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-precheck-project-name-"));
  try {
    const validateStart = controller.indexOf("\nvalidate_project()") + 1;
    const validateEnd = controller.indexOf("\nvalidate_phase()", validateStart);
    const managedStart = controller.indexOf("\nmanaged_project_name()") + 1;
    const managedEnd = controller.indexOf("\nvalidate_phase()", managedStart);
    const script = `#!/usr/bin/env bash
set -euo pipefail
${controller.slice(validateStart, validateEnd)}
${controller.slice(managedStart, managedEnd)}
first="$(managed_project_name 1234567890 1 precheck audit,graph,migration,repository,trace)"
second="$(managed_project_name 1234567890 1 precheck community,http,postgres,server)"
collision_first="$(managed_project_name 1234567890 1 precheck a,b)"
collision_second="$(managed_project_name 1234567890 1 precheck a-b)"
[[ "\${first}" != "\${second}" ]]
[[ "\${collision_first}" != "\${collision_second}" ]]
(( \${#first} <= 63 ))
(( \${#second} <= 63 ))
(( \${#collision_first} <= 63 ))
(( \${#collision_second} <= 63 ))
`;
    const scriptPath = join(fixture, "project-name-test.sh");
    await executable(scriptPath, script);
    await assert.doesNotReject(run("bash", [scriptPath]));
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("local adapter builds the working tree and delegates to the shared controller", () => {
  assert.match(local, /docker build --target production/);
  assert.match(local, /DENSE_MEM_CI_LOCAL=1/);
  assert.match(local, /DENSE_MEM_CI_ENV_FILE/);
  assert.match(local, /DENSE_MEM_CI_TELEMETRY_TOKEN_FILE/);
  assert.match(local, /DENSE_MEM_CI_PROMETHEUS_FILE/);
  assert.match(local, /CONTROLLER="\$\{DENSE_MEM_E2E_CONTROLLER:-\$\{ROOT_DIR\}\/scripts\/e2e-host-controller\.sh\}"/);
  assert.match(local, /"\$CONTROLLER" start/);
  assert.match(local, /"\$CONTROLLER" run/);
  assert.match(local, /"\$CONTROLLER" stop/);
  assert.match(local, /IMAGE_REF="\$IMAGE_TAG"/);
  assert.doesNotMatch(local, /local image did not expose a content digest/);
  assert.doesNotMatch(local, /DOCKER_HOST=|rootless/);
  assert.deepEqual(JSON.parse(packageJSON).scripts, { e2e: "bash scripts/e2e.sh" });
});

test("local entrypoint classifies scenarios and delegates lifecycle calls", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-local-entrypoint-"));
  try {
    const localRoot = join(fixture, "repo");
    const localScripts = join(localRoot, "scripts");
    const localExamples = join(localRoot, "examples");
    const bin = join(fixture, "bin");
    const envFile = join(fixture, ".env");
    const tokenFile = join(fixture, "telemetry-scrape-token");
    const prometheusFile = join(fixture, "prometheus.yml");
    const controllerLog = join(fixture, "controller.log");
    const dockerLog = join(fixture, "docker.log");
    const controllerStub = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\\0' "$@" "playwright=\${DENSE_MEM_CI_RUN_PLAYWRIGHT:-unset}" >> "\${DENSE_MEM_TEST_CONTROLLER_LOG}"
printf '\\n' >> "\${DENSE_MEM_TEST_CONTROLLER_LOG}"
case "\${1:-}" in
  doctor|stop) ;;
  start) printf '%s\\n' stub-project ;;
  run)
    if [[ "\${DENSE_MEM_TEST_SIGNAL_PARENT:-}" == "TERM" ]]; then
      kill -TERM "$PPID"
    fi
    ;;
  *) exit 2 ;;
esac
`;
    const dockerStub = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\\0' "$@" >> "\${DENSE_MEM_TEST_DOCKER_LOG}"
printf '\\n' >> "\${DENSE_MEM_TEST_DOCKER_LOG}"
case "\${1:-}" in
  build) ;;
  image)
    case "\${2:-}" in
      inspect) printf '%s\\n' sha256:1111111111111111111111111111111111111111111111111111111111111111 ;;
      rm) ;;
      *) exit 2 ;;
    esac
    ;;
  *) exit 2 ;;
esac
`;
    await mkdir(localScripts, { recursive: true });
    await mkdir(localExamples, { recursive: true });
    await mkdir(bin);
    await writeFile(join(localScripts, "e2e.sh"), local, { mode: 0o700 });
    await chmod(join(localScripts, "e2e.sh"), 0o700);
    for (const name of ["e2e-scenario-registry.mjs", "e2e-scenarios.json", "e2e-redact-diagnostics.mjs"]) {
      await writeFile(join(localScripts, name), await readFile(join(scripts, name)));
    }
    await writeFile(join(localExamples, "prometheus.yml"), await readFile(join(root, "examples/prometheus.yml")));
    await executable(join(bin, "docker"), dockerStub);
    await executable(join(bin, "controller"), controllerStub);
    await writeFile(envFile, "CONTROL_PORTAL_TOKEN=local-test-token\n");
    await writeFile(tokenFile, "local-telemetry-token\n");
    await writeFile(prometheusFile, "global:\n  scrape_interval: 15s\n");
    await writeFile(controllerLog, "");
    await writeFile(dockerLog, "");
    await run("git", ["init", "--quiet"], { cwd: localRoot });
    await run("git", ["config", "user.email", "e2e@example.test"], { cwd: localRoot });
    await run("git", ["config", "user.name", "Dense-Mem E2E"], { cwd: localRoot });
    await run("git", ["add", "."], { cwd: localRoot });
    await run("git", ["commit", "--quiet", "-m", "fixture"], { cwd: localRoot });

    const baseEnv = {
      ...process.env,
      PATH: `${bin}:${process.env.PATH}`,
      DENSE_MEM_E2E_CONTROLLER: join(bin, "controller"),
      DENSE_MEM_E2E_ENV_FILE: envFile,
      DENSE_MEM_E2E_TELEMETRY_TOKEN_FILE: tokenFile,
      DENSE_MEM_E2E_PROMETHEUS_FILE: prometheusFile,
      DENSE_MEM_TEST_CONTROLLER_LOG: controllerLog,
      DENSE_MEM_TEST_DOCKER_LOG: dockerLog,
    };
    const runScenario = async (name, env = baseEnv) => run("bash", [join(localScripts, "e2e.sh"), name], { env });
    const runFailure = async (name, env, message) => {
      await assert.rejects(runScenario(name, env), (error) => {
        assert.match(`${error.stderr || ""}${error.stdout || ""}`, message);
        return true;
      });
    };

    await runScenario("mcp_boundaries");
    let calls = nulLines(await readFile(controllerLog, "utf8"));
    assert.deepEqual(calls.map(([command]) => command), ["doctor", "start", "run", "stop"]);
    assert.equal(calls[1][3], "exclusive");
    assert.equal(calls[1][4], "mcp_boundaries");
    assert.match(calls[1][5], /^ghcr\.io\/markhuangai\/dense-mem:e2e-local-/);
    assert.doesNotMatch(calls[1][5], /@sha256:/);
    assert.equal(calls[2][3], "exclusive");
    assert.equal(calls[2][4], "mcp_boundaries");
    assert.equal(calls[2].at(-1), "playwright=0");
    assert.equal(calls[3][1], "stub-project");

    await writeFile(controllerLog, "");
    await runScenario("mcp_sdk_parity");
    calls = nulLines(await readFile(controllerLog, "utf8"));
    assert.deepEqual(calls.map(([command]) => command), ["doctor", "start", "run", "stop"]);
    assert.equal(calls[1][3], "shared");
    assert.equal(calls[1][4], "shared");
    assert.equal(calls[2][3], "shared");
    assert.equal(calls[2][4], "shared");
    assert.equal(calls[2][5], "mcp_sdk_parity");
    assert.equal(calls[2].at(-1), "playwright=0");

    await writeFile(controllerLog, "");
    await runScenario("mcp_oauth");
    calls = nulLines(await readFile(controllerLog, "utf8"));
    assert.equal(calls[2][5], "mcp_oauth");
    assert.equal(calls[2].at(-1), "playwright=1");

    await writeFile(controllerLog, "");
    await assert.rejects(
      runScenario("mcp_boundaries", { ...baseEnv, DENSE_MEM_TEST_SIGNAL_PARENT: "TERM" }),
      (error) => {
        assert.equal(error.code, 143);
        return true;
      },
    );
    calls = nulLines(await readFile(controllerLog, "utf8"));
    assert.deepEqual(calls.map(([command]) => command), ["doctor", "start", "run", "stop"]);

    await writeFile(controllerLog, "");
    await runFailure("not-valid", baseEnv, /usage: scripts\/e2e\.sh SCENARIO/);
    assert.equal((await readFile(controllerLog, "utf8")).length, 0);
    await runFailure("future_scenario", baseEnv, /not audited for local production execution/);
    assert.equal((await readFile(controllerLog, "utf8")).length, 0);
    await writeFile(join(localRoot, ".env"), "CONTROL_PORTAL_TOKEN=repository-fallback-token\n");
    const missingEnv = { ...baseEnv, DENSE_MEM_E2E_ENV_FILE: join(fixture, "missing.env") };
    await runFailure("mcp_boundaries", missingEnv, /missing environment file/);
    assert.equal((await readFile(controllerLog, "utf8")).length, 0);

    await executable(join(bin, "git"), "#!/usr/bin/env bash\nexit 42\n");
    await writeFile(dockerLog, "");
    await runFailure("mcp_boundaries", baseEnv, /unable to resolve the git revision/);
    const dockerCalls = nulLines(await readFile(dockerLog, "utf8"));
    assert.equal(dockerCalls.some(([command]) => command === "build"), false);
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("scenario browser image matches the installed Playwright browser version", async () => {
  const lock = JSON.parse(await readFile(join(root, "web/package-lock.json"), "utf8"));
  const version = lock.packages["node_modules/playwright-core"].version;
  const { stdout } = await run("bash", ["-c", 'source "$1"; printf "%s" "$SCENARIO_TEST_IMAGE"',
    "browser-image-test", join(scripts, "e2e-host-controller-runtime.sh")]);
  assert.equal(stdout, `mcr.microsoft.com/playwright:v${version}-noble`);
});

test("scenario containers discover a per-user Docker Compose plugin", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-compose-plugin-"));
  try {
    const bin = join(fixture, "bin");
    const dockerConfig = join(fixture, "docker-config");
    const docker = join(bin, "docker");
    const plugin = join(dockerConfig, "cli-plugins", "docker-compose");
    await mkdir(bin, { recursive: true });
    await mkdir(join(dockerConfig, "cli-plugins"), { recursive: true });
    await executable(docker, "#!/usr/bin/env sh\nexit 0\n");
    await executable(plugin, "#!/usr/bin/env sh\nexit 0\n");

    await run(
      "bash",
      [
        "-c",
        'set -euo pipefail; fail() { printf "%s\\n" "$*" >&2; exit 1; }; source "$1"; docker_cli_paths; [[ "$DENSE_MEM_CI_DOCKER_BIN" == "$(readlink -f "$2")" ]]; [[ "$DENSE_MEM_CI_COMPOSE_PLUGIN" == "$(readlink -f "$3")" ]]',
        "compose-plugin-test",
        join(scripts, "e2e-host-controller-runtime.sh"),
        docker,
        plugin,
      ],
      {
        env: {
          ...process.env,
          PATH: `${bin}:${process.env.PATH}`,
          DOCKER_CONFIG: dockerConfig,
        },
      },
    );
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("local controller honors the selected daemon while filtering worktree source", async () => {
  assert.match(controller, /telemetry-scrape-token/);
  assert.match(controller, /test-results/);
  assert.match(controller, /playwright-report/);
  assert.match(controller, /densemem-e2e-/);
  assert.match(controller, /tar --null --verbatim-files-from/);
  assert.match(controller, /--transform='s,\^,workspace\//);

  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-worktree-filter-"));
  try {
    const source = join(fixture, "source");
    const bin = join(fixture, "bin");
    const library = join(fixture, "controller");
    const archiveList = join(fixture, "archive.list");
    await mkdir(source, { recursive: true });
    await mkdir(bin);
    await mkdir(library);
    await run("git", ["init", "--quiet"], { cwd: source });
    await run("git", ["config", "user.email", "e2e@example.test"], { cwd: source });
    await run("git", ["config", "user.name", "Dense-Mem E2E"], { cwd: source });

    const files = {
      "README.md": "allowed\n",
      "scripts/e2e.sh": "allowed\n",
      "src/allowed.txt": "allowed\n",
      "deleted.txt": "removed from the worktree\n",
      ".env": "blocked\n",
      ".env.local": "blocked\n",
      "nested/.env": "blocked\n",
      "telemetry-scrape-token": "blocked\n",
      "nested/telemetry-scrape-token": "blocked\n",
      "coverage": "blocked\n",
      "coverage.out": "blocked\n",
      "test-results/report.json": "blocked\n",
      "playwright-report/index.html": "blocked\n",
      "nested/densemem-e2e-run/result.json": "blocked\n",
    };
    for (const [relative, contents] of Object.entries(files)) {
      const path = join(source, relative);
      await mkdir(join(path, ".."), { recursive: true });
      await writeFile(path, contents);
    }
    await run("git", ["add", "."], { cwd: source });
    await run("git", ["commit", "--quiet", "-m", "fixture"], { cwd: source });
    await rm(join(source, "deleted.txt"));

    const dockerStub = `#!/usr/bin/env bash
set -euo pipefail
case "\${1:-}" in
  cp)
    [[ "\${2:-}" == - && "\${3:-}" == test-container:/ ]] || exit 2
    tar -tf - > "\${DENSE_MEM_DOCKER_CP_ARCHIVE}"
    ;;
  image)
    [[ "\${2:-}" == inspect ]] || exit 2
    printf '%s\\n' sha256:2222222222222222222222222222222222222222222222222222222222222222
    ;;
  *) exit 2 ;;
esac
`;
    await executable(join(bin, "docker"), dockerStub);
    await writeFile(archiveList, "");

    const controllerSources = {
      "e2e-host-controller.sh": controller.slice(0, controller.indexOf('case "${1:-}" in')),
      "e2e-host-controller-stack.sh": stack,
      "e2e-host-controller-postgres.sh": postgres,
      "e2e-host-controller-runtime.sh": runtime,
      "e2e-redact-diagnostics.mjs": await readFile(join(scripts, "e2e-redact-diagnostics.mjs"), "utf8"),
    };
    assert.ok(!controllerSources["e2e-host-controller.sh"].includes('case "${1:-}" in'));
    for (const [name, contents] of Object.entries(controllerSources)) {
      const path = join(library, name);
      await writeFile(path, contents);
      if (name.endsWith(".sh")) await chmod(path, 0o700);
    }

    await run(
      "bash",
      [
        "-c",
        'set -euo pipefail; source "$1"; [[ "$(docker_socket_path)" == /explicit/docker.sock ]]; resolve_image_ref "ghcr.io/markhuangai/dense-mem:local-test"; [[ "$DENSE_MEM_CI_RESOLVED_IMAGE" == ghcr.io/markhuangai/dense-mem:local-test ]]; [[ "$DENSE_MEM_CI_RESOLVED_DIGEST" == sha256:2222222222222222222222222222222222222222222222222222222222222222 ]]; copy_worktree_source "$2" "$3"',
        "worktree-filter-test",
        join(library, "e2e-host-controller.sh"),
        "test-container",
        source,
      ],
      {
        env: {
          ...process.env,
          PATH: `${bin}:${process.env.PATH}`,
          DENSE_MEM_DOCKER_CP_ARCHIVE: archiveList,
          DENSE_MEM_CI_LOCAL: "1",
          DOCKER_HOST: "unix:///explicit/docker.sock",
          DOCKER_CONTEXT: "",
        },
      },
    );

    const transferred = (await readFile(archiveList, "utf8"))
      .trim()
      .split(/\r?\n/)
      .filter(Boolean)
      .sort();
    assert.deepEqual(transferred, ["workspace/README.md", "workspace/scripts/e2e.sh", "workspace/src/allowed.txt"]);
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("shared PostgreSQL provisioning keeps runtime identity least-privileged", () => {
  assert.match(postgres, /DENSE_MEM_CI_BOOTSTRAP_POSTGRES_USER="densemem_e2e_bootstrap"/);
  assert.match(postgres, /export DENSE_MEM_CI_BOOTSTRAP_POSTGRES_USER DENSE_MEM_CI_BOOTSTRAP_POSTGRES_PASSWORD/);
  assert.match(postgres, /CREATE EXTENSION IF NOT EXISTS vector/);
  assert.match(postgres, /NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS/);
  assert.match(postgres, /ALTER DATABASE :"runtime_database" OWNER TO densemem_e2e_database_owner/);
  assert.match(postgres, /role\.rolbypassrls/);
  assert.match(postgres, /run_identity_cleanup_startup_matrix/);
  assert.match(postgres, /DATABASE_URL=\$\{database_url\}/);
  assert.match(postgres, /--scenario identity_cleanup \\\s+--timeout 20m --total-timeout 25m/);
  assert.doesNotMatch(postgres, /--scenario identity_cleanup --capability postgres/);
  assert.doesNotMatch(postgres, /--case postgres\/TestIdentityCleanupComposeSeed/);
  assert.match(controller, /provision_postgres_runtime_role/);
  assert.match(controller, /verify_postgres_runtime_migration_state/);
  assert.match(controller, /docker network inspect --format '[^']*\.Containers/);
  assert.match(controller, /docker rm -f "\$container_id"/);
});

test("Compose stack has no host bindings and carries only project-scoped inputs", () => {
  assert.doesNotMatch(compose, /^\s+ports:/m);
  assert.doesNotMatch(stack, /\$\{sourceDir\}\/examples\/grafana/);
  assert.match(stack, /grafana-provisioning:\/etc\/grafana\/provisioning:ro/);
  assert.match(stack, /grafana-dashboards:\/opt\/dense-mem\/grafana\/dashboards:ro/);
  assert.match(compose, /POSTGRES_USER: \$\{DENSE_MEM_CI_BOOTSTRAP_POSTGRES_USER:/);
  assert.match(compose, /POSTGRES_PASSWORD: \$\{DENSE_MEM_CI_BOOTSTRAP_POSTGRES_PASSWORD:/);
  assert.match(compose, /env_file:\n\s+- \$\{DENSE_MEM_CI_ENV_FILE/);
  assert.match(compose, /prometheus-config:/);
  assert.match(compose, /telemetry-scrape-token:/);
  assert.match(compose, /entra-mock:/);
  assert.match(compose, /profiles: \[oauth_compatibility\]/);
  assert.match(compose, /external: true/);
});

test("verifier scenarios use the deterministic provider without replacing embeddings", () => {
  assert.match(compose, /profiles: \[synchronous_write, verifier\]/);
  assert.match(realController, /"  synchronous-write-provider:"[\s\S]*"    profiles: \[synchronous_write, verifier\]"/);
  const verifierStart = stack.indexOf('if (has("verifier"))');
  const verifierEnd = stack.indexOf('if (scenario === "identity_cleanup")', verifierStart);
  assert.ok(verifierStart >= 0 && verifierEnd > verifierStart);
  const verifierBlock = stack.slice(verifierStart, verifierEnd);
  assert.match(verifierBlock, /AI_VERIFIER_API_URL: "http:\/\/synchronous-write-provider:8787\/v1"/);
  assert.doesNotMatch(verifierBlock, /\bAI_API_URL:/);
  assert.match(stack, /has_helper "\$helpers" verifier \|\| has_helper "\$helpers" synchronous_write/);
});

test("Grafana provisioning leaves organization and datasource IDs to the installation", async () => {
  const grafana = join(root, "examples/grafana");
  const [provider, datasource, overview, ai, workflows] = await Promise.all([
    readFile(join(grafana, "provisioning/dashboards/provider.yml"), "utf8"),
    readFile(join(grafana, "provisioning/datasources/datasource.yml"), "utf8"),
    readFile(join(grafana, "dashboards/dense-mem-service.json"), "utf8"),
    readFile(join(grafana, "dashboards/dense-mem-ai-recall.json"), "utf8"),
    readFile(join(grafana, "dashboards/dense-mem-workflows.json"), "utf8"),
  ]);
  assert.doesNotMatch(provider, /^\s*orgId:/m);
  assert.doesNotMatch(datasource, /^\s*(orgId|uid):/m);
  for (const contents of [overview, ai, workflows]) {
    const dashboard = JSON.parse(contents);
    assert.ok(dashboard.templating.list.some((variable) => variable.name === "datasource"));
    assert.ok(dashboard.panels.every((panel) => panel.targets.every((target) => target.datasource?.uid === "$datasource")));
  }
});

test("Grafana panels fit the grid without hiding their series labels", async () => {
  for (const name of ["dense-mem-service", "dense-mem-ai-recall", "dense-mem-workflows"]) {
    const dashboard = JSON.parse(await readFile(join(root, `examples/grafana/dashboards/${name}.json`), "utf8"));
    for (const [index, panel] of dashboard.panels.entries()) {
      const box = panel.gridPos;
      assert.ok(box.x >= 0 && box.y >= 0 && box.w > 0 && box.h > 0 && box.x + box.w <= 24, `${name}: ${panel.title} is outside the grid`);
      assert.ok(panel.targets.every((target) => target.legendFormat === undefined), `${name}: ${panel.title} hides series labels`);
      for (const other of dashboard.panels.slice(index + 1)) {
        const next = other.gridPos;
        const overlaps = box.x < next.x + next.w && next.x < box.x + box.w && box.y < next.y + next.h && next.y < box.y + box.h;
        assert.equal(overlaps, false, `${name}: ${panel.title} overlaps ${other.title}`);
      }
    }
  }
});

test("Grafana component cost panels use only their own request activity", async () => {
  const dashboard = JSON.parse(await readFile(join(root, "examples/grafana/dashboards/dense-mem-ai-recall.json"), "utf8"));
  const expression = (title) => dashboard.panels.find((panel) => panel.title === title)?.targets[0].expr;
  const aggregate = expression("AI cost");
  const verifier = expression("Verifier cost");
  const embedding = expression("Embedding cost");
  assert.match(aggregate, /densemem_verifier_requests_total/);
  assert.match(aggregate, /densemem_embedding_requests_total/);
  assert.match(verifier, /densemem_verifier_requests_total/);
  assert.doesNotMatch(verifier, /densemem_embedding_requests_total/);
  assert.match(embedding, /densemem_embedding_requests_total/);
  assert.doesNotMatch(embedding, /densemem_verifier_requests_total/);
});

test("Grafana retains Evidence intake and removes retired factual graph measures", async () => {
  for (const name of ["dense-mem-service", "dense-mem-ai-recall", "dense-mem-workflows", "dense-mem-usage", "dense-mem-performance"]) {
    const contents = await readFile(join(root, `examples/grafana/dashboards/${name}.json`), "utf8");
    assert.doesNotMatch(contents, /densemem_(?:dream|recall_hypothes|operational_(?:dream|hypothes|relationship)|conflict_(?:review_duration|queue))/);
    const dashboard = JSON.parse(contents);
    if (name === "dense-mem-workflows") {
      assert.equal(dashboard.title, "Dense-Mem Remember and Evidence");
      assert(dashboard.panels.some((panel) => panel.targets.some((target) => target.expr.includes("densemem_operational_remember_attempts"))));
    }
  }
});

test("Grafana range panels use a scrape-safe lookback", async () => {
  for (const name of ["dense-mem-service", "dense-mem-ai-recall", "dense-mem-workflows"]) {
    const dashboard = JSON.parse(await readFile(join(root, `examples/grafana/dashboards/${name}.json`), "utf8"));
    for (const panel of dashboard.panels.filter((item) => item.type === "timeseries")) {
      const expression = panel.targets[0].expr;
      assert.doesNotMatch(expression, /\[\$__interval\]|\$__interval_ms/, `${name}: ${panel.title} uses a short rate window`);
      if (expression.includes("increase(")) {
        assert.match(expression, /\[\$__rate_interval\]/, `${name}: ${panel.title} lacks a bounded lookback`);
        assert.doesNotMatch(expression, /\$window/, `${name}: ${panel.title} scans the rolling-total window at every point`);
      }
    }
  }
});

test("ontology fixtures forward healthy embeddings upstream", () => {
  assert.match(stack, /const fixtureEmbeddingProvider = [^;]*\["ontology_organization", "ontology_management", "ontology_upgrade"\]\.includes\(scenario\)/);
  assert.match(stack, /AI_API_URL: "http:\/\/synchronous-write-provider:8787\/v1"/);
  assert.ok(stack.includes('if (scenario === "ontology_management") serverEnvironment.set("LOG_LEVEL", "debug");'));
  assert.match(stack, /DENSE_MEM_E2E_EMBEDDING_BASE_URL/);
  assert.match(stack, /DENSE_MEM_E2E_EMBEDDING_API_KEY/);
  assert.match(stack, /embedding_upstream\.mjs/);
  assert.match(stack, /DENSE_MEM_CI_HOSTED/);
});

test("identity cleanup retains chat helpers and retired graph scenarios are absent", async () => {
  const registry = JSON.parse(await readFile(join(scripts, "e2e-scenarios.json"), "utf8"));
  const helpers = (name) => registry.scenarios.find((entry) => entry.name === name)?.helper_profiles;
  assert.deepEqual(helpers("identity_cleanup"), ["verifier"]);
  for (const name of ["community", "conflict", "conflict_queue"]) assert.equal(helpers(name), undefined);
  assert.match(stack, /if \(scenario === "identity_cleanup"\) \{[\s\S]*?AI_API_URL: "http:\/\/synchronous-write-provider:8787\/v1"/);
  assert.match(controller, /run_identity_cleanup_startup_matrix/);
  assert.doesNotMatch(runtime, /CONFLICT_PROVIDER|CONFLICT_REVIEW/);
});

test("Go helper containers fall back from module proxy transport errors", () => {
  assert.match(stack, /-e "GOPROXY=https:\/\/proxy\.golang\.org\|direct"/);
});

test("image acquisition accepts only the bounded registry HTTP failure allowlist", async () => {
  const start = controller.indexOf("\ntransient_image_pull_failure() {") + 1;
  const end = controller.indexOf("\npull_images() {", start);
  assert.ok(start > 0 && end > start);
  const script = `${controller.slice(start, end)}\nprintf '%s\\n' "$1" | transient_image_pull_failure`;
  for (const reason of ["500 Internal Server Error", "502 Bad Gateway", "503 Service Unavailable", "504 Gateway Timeout"]) {
    const failure = `unknown: failed to copy: httpReadSeeker: unexpected status from GET request to https://registry.example/v2/redis/blobs/sha256:123: ${reason}`;
    await run("bash", ["-c", script, "image-acquisition", failure]);
    await run("bash", ["-c", script, "image-acquisition", `Error response from daemon: unexpected status from HEAD request to http://127.0.0.1:1234/v2/recovery/manifests/latest: ${reason}`]);
    await run("bash", ["-c", script, "image-acquisition", `Image redis Pulling\nImage redis Error ${failure}\n${failure}`]);
    await assert.rejects(run("bash", ["-c", script, "image-acquisition", `${failure}\nError response from daemon: unauthorized: authentication required`]));
    await assert.rejects(run("bash", ["-c", script, "image-acquisition", `${failure}\nError response from daemon: unknown registry error`]));
    await assert.rejects(run("bash", ["-c", script, "image-acquisition", `${failure}\nHTTP 404 Not Found`]));
  }
  for (const failure of [
    "", "Image redis Pulled", "500 Internal Server Error", "Error response from daemon: 429 Too Many Requests",
    "request returned 500 Internal Server Error for API route and version http://docker/v1.45/images/create, check if the server supports the requested API version",
    "Error response from daemon: 503 Service Unavailable",
    "Error response from daemon: manifest unknown", "Error response from daemon: no matching manifest",
    "Error response from daemon: digest mismatch", "Error response from daemon: context canceled",
    "Error response from daemon: x509 certificate expired", "Error response from daemon: 404 Not Found",
    "Error response from daemon: unknown error", "Error response from daemon: connection refused",
    "Error response from daemon: unauthorized despite 500 Internal Server Error",
  ]) {
    await assert.rejects(run("bash", ["-c", script, "image-acquisition", failure]), failure);
  }
});

test("phase receipts keep successful controller stdout available for the project", async () => {
  const start = controller.indexOf("\nreport_phase() {") + 1;
  const end = controller.indexOf("\ntransient_image_pull_failure() {", start);
  const { stdout, stderr } = await run("bash", ["-c", `${controller.slice(start, end)}
SECONDS=4
report_phase image_acquisition 2 1 2 densemem-ci-test
printf '%s\\n' densemem-ci-test`]);
  assert.equal(stdout, "densemem-ci-test\n");
  assert.match(stderr, /^dense-mem CI phase: phase=image_acquisition project=densemem-ci-test status=1 elapsed_seconds=2 attempt=2\n$/);
});

test("scenario runner executes Entra and diagnostics through the shared path", () => {
  assert.match(scenario, /dense-mem CI scenario \[%s\]/);
  assert.match(scenario, /log "running \$\{script\}"/);
  assert.match(scenario, /log "running Playwright specs: \$\{specs\[\*\]\}"/);
  assert.match(scenario, /Entra OIDC mock/);
  assert.match(scenario, /tests\/uat\/entra_scim_e2e\.mjs/);
  assert.match(scenario, /synchronous_write_telemetry_disabled/);
  assert.match(scenario, /synchronous_write\) specs=\("tests-compose\/remember-attempts\.spec\.ts"\)/);
  assert.match(scenario, /DENSE_MEM_E2E_DIAGNOSTICS_FIXTURE_FILE/);
  assert.doesNotMatch(scenario, /parse_json_dream_statement|DENSE_MEM_E2E_DREAM_STATEMENT.*synchronous/);
  assert.match(runtime, /failed stack diagnostics/);
  assert.match(runtime, /ci_compose logs --no-color --timestamps --tail 200/);
});

test("failure stack diagnostics redact the Grafana admin password", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-grafana-redaction-"));
  try {
    const envFile = join(fixture, "e2e.env");
    await writeFile(envFile, "CONTROL_PORTAL_TOKEN=ci-control-token\n");
    const redactorStart = controller.indexOf("\nredact_diagnostics() {") + 1;
    const redactorEnd = controller.indexOf("\n}\n", redactorStart) + 2;
    const diagnosticsStart = runtime.indexOf("    (\n      DENSE_MEM_CI_COMPOSE_OVERLAY_FILE", runtime.indexOf("failed stack diagnostics"));
    const diagnosticsEnd = runtime.indexOf("\n    local -a diagnostics_pipeline_status", diagnosticsStart);
    assert(redactorStart > 0 && redactorEnd > redactorStart && diagnosticsStart >= 0 && diagnosticsEnd > diagnosticsStart);
    const script = `set -euo pipefail
${controller.slice(redactorStart, redactorEnd)}
ci_compose() { printf '%s\\n' "$grafana_password"; }
${runtime.slice(diagnosticsStart, diagnosticsEnd)}`;
    const password = "ci-grafana-password-012345";
    const { stdout } = await run("bash", ["-c", script], {
      env: {
        CONTROLLER_DIR: scripts, ENV_FILE: envFile, grafana_password: password,
        helper_overlay: "", control_token: "", telemetry_token: "", postgres_password: "",
        api_key: "", identity_upgrade_api_key: "", oauth_token: "",
      },
    });
    assert.match(stdout, /--- Compose services ---/);
    assert.match(stdout, /\[REDACTED\]/);
    assert.doesNotMatch(stdout, /ci-grafana-password-012345/);
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("startup failures preserve protected server logs before stack cleanup", async (t) => {
  const start = controller.indexOf("  cleanup_failed_start() {");
  const end = controller.indexOf("\n  prepare_stack_helpers", start);
  const redactorStart = controller.indexOf("\nredact_diagnostics() {") + 1;
  const redactorEnd = controller.indexOf("\n}\n", redactorStart) + 2;
  assert(start >= 0 && end > start && redactorStart > 0 && redactorEnd > redactorStart);
  for (const mode of ["complete", "collection failure", "redaction failure"]) {
    await t.test(mode, async () => {
      const fixture = await mkdtemp(join(tmpdir(), "dense-mem-startup-diagnostics-"));
      try {
        const envFile = join(fixture, "e2e.env");
        const eventsFile = join(fixture, "events");
        await writeFile(envFile, "CONTROL_PORTAL_TOKEN=fixture-startup-credential\n");
        const script = `set -euo pipefail
${controller.slice(redactorStart, redactorEnd)}
ci_compose() {
  printf 'capture:%s\\n' "$*" >> "$FIXTURE_EVENTS"
  printf '%s\\n' 'migration failed: fixture-startup-credential' 'exact admitted text survives'
  return "$FIXTURE_COLLECTION_STATUS"
}
report_phase() { :; }
cleanup_identity_cleanup_lock() { printf 'unlock\\n' >> "$FIXTURE_EVENTS"; }
stop_stack() { printf 'stop\\n' >> "$FIXTURE_EVENTS"; }
run_fixture() {
  local current_phase=fixture_preparation phase_started=$SECONDS project=fixture-project
  local runtime_compose_path="" DENSE_MEM_CI_HELPER_DIR="" DENSE_MEM_CI_PRIVATE_DIR=""
${controller.slice(start, end)}
  exit 17
}
run_fixture`;
        const result = await new Promise((resolve, reject) => {
          execFile("bash", ["-c", script], { env: {
            ...process.env, CONTROLLER_DIR: mode === "redaction failure" ? join(fixture, "missing") : scripts,
            ENV_FILE: envFile, FIXTURE_EVENTS: eventsFile,
            FIXTURE_COLLECTION_STATUS: mode === "collection failure" ? "24" : "0",
          } }, (error, stdout, stderr) => {
            if (error && typeof error.code !== "number") reject(error);
            else resolve({ code: error?.code || 0, stdout, stderr });
          });
        });
        assert.equal(result.code, 17, result.stderr);
        assert.deepEqual((await readFile(eventsFile, "utf8")).trim().split("\n"), [
          "capture:logs --no-color --timestamps server", "unlock", "stop",
        ]);
        assert.doesNotMatch(result.stdout + result.stderr, /fixture-startup-credential/);
        if (mode !== "redaction failure") {
          assert.match(result.stderr, /migration failed: \[REDACTED\]/);
          assert.match(result.stderr, /exact admitted text survives/);
        }
        assert.equal(result.stderr.includes("startup diagnostic capture incomplete"), mode !== "complete");
      } finally {
        await rm(fixture, { recursive: true, force: true });
      }
    });
  }
});

test("scenario failures collect protected evidence before removing the client", { timeout: 20000 }, async (t) => {
  const lifecycleStart = runtime.indexOf('  local container="" failure_diagnostics_collected=0');
  const lifecycleEnd = runtime.indexOf("\n}\n\ncontrol_api_request()", lifecycleStart);
  const redactorStart = controller.indexOf("\nredact_diagnostics() {") + 1;
  const redactorEnd = controller.indexOf("\n}\n", redactorStart) + 2;
  assert(lifecycleStart >= 0 && lifecycleEnd > lifecycleStart && redactorStart > 0 && redactorEnd > redactorStart);
  for (const entry of [
    { name: "success", status: 0 },
    { name: "assertion", status: 17 },
    { name: "setup", status: 24 },
    { name: "timeout", status: 143, signal: "SIGTERM" },
    { name: "interrupt", status: 130, signal: "SIGINT" },
    { name: "redaction failure", status: 1, redactionFail: true },
    { name: "failed collection and cleanup", status: 143, signal: "SIGTERM", collectionFail: true, cleanupFail: true },
  ]) {
    await t.test(entry.name, async () => {
      const fixture = await mkdtemp(join(tmpdir(), "dense-mem-scenario-trap-"));
      let child;
      let deadline;
      try {
        const envFile = join(fixture, "e2e.env");
        const eventsFile = join(fixture, "events");
        await writeFile(envFile, "CONTROL_PORTAL_TOKEN=fixture-control-credential\n");
        await executable(join(fixture, "docker"), '#!/usr/bin/env bash\ndocker "$@"\n');
        const script = `set -euo pipefail
${controller.slice(redactorStart, redactorEnd)}
docker() {
  case "$1" in
    create) printf '%s\\n' fixture-client ;;
    start)
      if [[ -n "$FIXTURE_SIGNAL" ]]; then
        trap 'exit 130' INT
        trap 'exit 143' TERM
        printf '%s\\n' fixture:ready >> "$FIXTURE_EVENTS"
        read -r -t 10 unused
      else
        return "$FIXTURE_STATUS"
      fi ;;
    rm)
      [[ "${'${*: -1}'}" == fixture-client ]] || return 91
      printf '%s\\n' "$*" >> "$FIXTURE_EVENTS"
      return "$FIXTURE_CLEANUP_FAILURE" ;;
    *) return 92 ;;
  esac
}
export -f docker
copy_git_source() { [[ "$FIXTURE_SETUP_FAILURE" == 0 ]] || return 24; }
copy_file_into_container() { :; }
report_phase() { printf 'phase:%s:%s\\n' "$1" "$3" >> "$FIXTURE_EVENTS"; }
fail() { printf '%s\\n' "$*" >&2; exit 1; }
ci_compose() {
  printf 'diagnostics:%s\\n' "$*" >> "$FIXTURE_EVENTS"
  printf '%s\\n' "$control_token" "$grafana_password"
  return "$FIXTURE_COLLECTION_FAILURE"
}
run_fixture() {
  local scenario=synchronous_write_telemetry_disabled source_dir="$FIXTURE_SOURCE_DIR" project=fixture-project
  local helper_overlay="" helper_dir="$FIXTURE_DIR/missing" runtime_compose_host=""
  local control_token=fixture-control-credential grafana_password=fixture-grafana-credential
  local telemetry_token="" postgres_password="" api_key="" identity_upgrade_api_key="" oauth_token=""
  local DENSE_MEM_CI_DOCKER_BIN="" DENSE_MEM_CI_COMPOSE_PLUGIN=""
  local -a docker_args=(create)
${runtime.slice(lifecycleStart, lifecycleEnd)}
}
run_fixture`;
        child = spawn("bash", ["-c", script], {
          detached: true,
          env: {
            ...process.env, CONTROLLER_DIR: entry.redactionFail ? join(fixture, "missing") : scripts,
            ENV_FILE: envFile, FIXTURE_DIR: fixture, FIXTURE_SOURCE_DIR: root, FIXTURE_EVENTS: eventsFile, PATH: `${fixture}:${process.env.PATH}`,
            FIXTURE_SIGNAL: entry.signal || "", FIXTURE_STATUS: String(entry.status),
            FIXTURE_SETUP_FAILURE: entry.name === "setup" ? "1" : "0",
            FIXTURE_COLLECTION_FAILURE: entry.collectionFail ? "1" : "0", FIXTURE_CLEANUP_FAILURE: entry.cleanupFail ? "1" : "0",
          },
        });
        let stdout = "", stderr = "", signalled = false;
        child.stdout.on("data", (chunk) => { stdout += chunk; });
        child.stderr.on("data", (chunk) => { stderr += chunk; });
        deadline = setTimeout(() => process.kill(-child.pid, "SIGKILL"), 5000);
        const finished = once(child, "close");
        if (entry.signal) {
          while (child.exitCode === null && child.signalCode === null) {
            let events = "";
            try { events = await readFile(eventsFile, "utf8"); } catch (error) { if (error.code !== "ENOENT") throw error; }
            if (events.includes("fixture:ready\n")) {
              signalled = true;
              process.kill(-child.pid, entry.signal);
              break;
            }
            await new Promise((resolve) => setTimeout(resolve, 10));
          }
        }
        const [code, signal] = await finished;
        assert.equal(signal, null, stderr);
        assert.equal(code, entry.status, stderr);
        if (entry.signal) assert(signalled, "fixture must reach readiness before cancellation");
        const events = (await readFile(eventsFile, "utf8")).trim().split("\n");
        assert.match(events.at(-1), /^rm (?:-f )?fixture-client$/);
        if (entry.status === 0) {
          assert.equal(events.some((event) => event.startsWith("diagnostics:")), false);
        } else {
          assert.equal(events.filter((event) => event === "diagnostics:ps --all").length, 1);
          assert.equal(events.filter((event) => event === "diagnostics:logs --no-color --timestamps --tail 200").length, 1);
          if (!entry.redactionFail) assert.match(stdout, /\[REDACTED\]/);
          assert.doesNotMatch(stdout + stderr, /fixture-control-credential|fixture-grafana-credential/);
        }
        if (entry.collectionFail) assert.match(stderr, /stack diagnostic collection failed/);
        if (entry.redactionFail) assert.match(stderr, /stack diagnostic redaction failed/);
        if (entry.signal) assert(events.includes(`phase:scenario_execution:${entry.status}`));
      } finally {
        clearTimeout(deadline);
        if (child && child.exitCode === null && child.signalCode === null) process.kill(-child.pid, "SIGKILL");
        await rm(fixture, { recursive: true, force: true });
      }
    });
  }
});

test("diagnostics stream complete lines while retaining overlapping partial credentials", () => {
  const secret = `fixture-control-${"x".repeat(128)}`;
  let output = "";
  const redactor = createRedactor(["fixture-control", secret], (chunk) => { output += chunk; });
  redactor.write(`ready\n${secret.slice(0, 20)}`);
  assert.equal(output, "ready\n");
  redactor.write(`${secret.slice(20)}\n`);
  assert.equal(output, "ready\n[REDACTED]\n");
  redactor.end();
  assert.equal(output, "ready\n[REDACTED]\n");
  const input = `ready\n${secret}\nfinished\n`;
  for (let index = 0; index <= input.length; index += 1) {
    assert.equal(redactChunks([input.slice(0, index), input.slice(index)], ["fixture-control", secret]), "ready\n[REDACTED]\nfinished\n");
  }
});

test("active case reaches controller output before an interrupted runner exits", { timeout: 6000 }, async () => {
  const moduleURL = new URL("./synchronous_write/runner.mjs", import.meta.url).href;
  const runner = spawn(process.execPath, ["--input-type=module", "-e",
    `import { runStep } from ${JSON.stringify(moduleURL)}; setInterval(() => {}, 1000); await runStep("diagnostics/stalled-ci-case", () => new Promise(() => {}));`], {
    env: { ...process.env, DENSE_MEM_E2E_RUN_ID: "live-progress", DENSE_MEM_E2E_RUN_ATTEMPT: "1",
      DENSE_MEM_E2E_COMMIT_SHA: "f".repeat(40), DENSE_MEM_E2E_SCENARIO: "synchronous_write" },
    stdio: ["ignore", "ignore", "pipe"],
  });
  const redactor = spawn(process.execPath, [join(scripts, "e2e-redact-diagnostics.mjs")], {
    env: { ...process.env, DENSE_MEM_CI_REDACT_ENV_FILE: "", DENSE_MEM_CI_REDACT_ALLOW_SHORT: "1",
      DENSE_MEM_CI_REDACT_EXTRA_VALUES: "x".repeat(128) },
    stdio: ["pipe", "pipe", "inherit"],
  });
  const stopped = [once(runner, "close"), once(redactor, "close")];
  let deadline;
  try {
    const line = await new Promise((resolve, reject) => {
      let output = "";
      deadline = setTimeout(() => reject(new Error("controller withheld the active case while the runner was blocked")), 4000);
      runner.on("error", reject);
      redactor.on("error", reject);
      redactor.stdout.on("data", (chunk) => {
        output += chunk;
        if (output.includes("\n")) resolve(output.slice(0, output.indexOf("\n")));
      });
      runner.stderr.pipe(redactor.stdin);
    });
    const progress = JSON.parse(line);
    assert.equal(progress.case, "diagnostics/stalled-ci-case");
    assert.equal(progress.outcome, "started");
    assert.equal(progress.run_id, "live-progress");
    assert.equal(runner.exitCode, null);
    assert.equal(redactor.exitCode, null);
  } finally {
    clearTimeout(deadline);
    runner.kill("SIGTERM");
    redactor.kill("SIGTERM");
    await Promise.allSettled(stopped);
  }
});

test("diagnostics retain exact redaction across split and overlapping secrets", () => {
  const values = ["secret", "secret-extended", "token:fixture", "🔑private-key"];
  const input = "Ω plain secret-extended / secret / token:fixture / 🔑private-key end";
  const expected = "Ω plain [REDACTED] / [REDACTED] / [REDACTED] / [REDACTED] end";
  for (let index = 0; index <= input.length; index += 1) {
    assert.equal(redactChunks([input.slice(0, index), input.slice(index)], values), expected);
  }
  assert.equal(redactChunks(Array.from(input), values), expected);
  assert.equal(redactChunks(["ab", "cd", "ef"], []), "abcdef");
  let output = "";
  const short = createRedactor(["ab"], (chunk) => { output += chunk; }, 2);
  for (const chunk of ["xx", "ab", "yy"]) short.write(chunk);
  short.end();
  assert.equal(output, "xx[REDACTED]yy");
});

test("diagnostics preserve typed public configuration values in measurement JSON", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-public-diagnostics-"));
  try {
    const envFile = join(fixture, ".env");
    await writeFile(envFile, [
      "TELEMETRY_ENABLED=false", "AI_VERIFIER_DISABLE_TEMPERATURE=true",
      "AI_API_EMBEDDING_DIMENSIONS=1024", "AI_API_EMBEDDING_MAX_BATCH_ITEMS=100",
      "AI_API_EMBEDDING_MAX_CONCURRENCY=12", "POSTGRES_PASSWORD=fixture-database-secret",
    ].join("\n") + "\n");
    const input = { observation_enabled: true, unavailable: false, sources: 100, sql_statements: 1100,
      dimensions: 1024, concurrency: 12, duration_ms: 100.1024, hash: "00102400", password: "fixture-database-secret" };
    const script = 'printf \'%s\\n\' "$1" | node "$2"';
    const { stdout } = await run("bash", ["-c", script, "measurement-json", JSON.stringify(input), join(scripts, "e2e-redact-diagnostics.mjs")], {
      env: { DENSE_MEM_CI_REDACT_ENV_FILE: envFile, DENSE_MEM_CI_REDACT_ALLOW_SHORT: "1", DENSE_MEM_CI_REDACT_EXTRA_VALUES: "" },
    });
    assert.deepEqual(JSON.parse(stdout), { ...input, password: "[REDACTED]" });
    assert.equal(valueFromEnvFile(envFile, "AI_API_EMBEDDING_MAX_BATCH_ITEMS"), "100");
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("diagnostics retain credential collisions, unknown settings and malformed public values", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-credential-diagnostics-"));
  try {
    const envFile = join(fixture, ".env");
    await writeFile(envFile, [
      "TELEMETRY_ENABLED=true", "AI_API_EMBEDDING_MAX_BATCH_ITEMS=100",
      "POSTGRES_PASSWORD=true", "AI_API_KEY=100", "CUSTOM_SETTING=unknown-protected-value",
      "AI_VERIFIER_DISABLE_TEMPERATURE=invalid-boolean-secret", "AI_API_EMBEDDING_DIMENSIONS=invalid-integer-secret",
      "AI_API_EMBEDDING_MAX_CONCURRENCY=999999999999999999999999",
      "AI_VERIFIER_MODEL=fixture-model", "AI_VERIFIER_API_KEY=fixture-model",
      "ONTOLOGY_MAINTENANCE_MODEL=private?token=credential",
    ].join("\n") + "\n");
    const input = "true 100 unknown-protected-value invalid-boolean-secret invalid-integer-secret 999999999999999999999999 extra-protected-pattern fixture-model private?token=credential";
    const { stdout } = await run("bash", ["-c", 'printf \'%s\\n\' "$1" | node "$2"', "credential-collisions", input, join(scripts, "e2e-redact-diagnostics.mjs")], {
      env: { DENSE_MEM_CI_REDACT_ENV_FILE: envFile, DENSE_MEM_CI_REDACT_ALLOW_SHORT: "1", DENSE_MEM_CI_REDACT_EXTRA_VALUES: "extra-protected-pattern" },
    });
    assert.equal(stdout.trim(), Array(9).fill("[REDACTED]").join(" "));
    assert.equal(valueFromEnvFile(envFile, "TELEMETRY_ENABLED"), "true");
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("diagnostics preserve protected assessment hashes and effective model identities", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-assessment-diagnostics-"));
  try {
    const envFile = join(fixture, ".env");
    const models = { AI_VERIFIER_MODEL: "dense-mem-ontology-e2e", AI_REMEMBER_MODEL: "dense-mem-ontology-e2e",
      AI_CONFLICT_REVIEW_MODEL: "dense-mem-ontology-e2e", ONTOLOGY_MAINTENANCE_MODEL: "dense-mem-ontology-e2e",
      AI_API_EMBEDDING_MODEL: "@cf/baai/bge-m3" };
    await writeFile(envFile, [...Object.entries(models).map(([key, value]) => `${key}=${value}`),
      "AI_API_KEY=dense-mem-ontology-e2e-key", "POSTGRES_PASSWORD=fixture-database-secret"].join("\n") + "\n");
    const body = JSON.stringify({ models, password: "ordinary admitted text", credentials: "[REDACTED]" });
    const record = { payload_chunk: { body, sha256: createHash("sha256").update(body).digest("hex") } };
    const { stdout } = await run("bash", ["-c", 'printf \'%s\\n\' "$1" | node "$2"', "assessment-hash", JSON.stringify(record), join(scripts, "e2e-redact-diagnostics.mjs")], {
      env: { DENSE_MEM_CI_REDACT_ENV_FILE: envFile, DENSE_MEM_CI_REDACT_EXTRA_VALUES: "" },
    });
    const observed = JSON.parse(stdout).payload_chunk;
    assert.equal(observed.body, body);
    assert.equal(createHash("sha256").update(observed.body).digest("hex"), observed.sha256);
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("diagnostics complete large output with a delayed reader", { timeout: 15000 }, async () => {
  const values = ["alpha", "alpha-long", "pha-longer", "token:value", "secret-secret"];
  const input = `${"synthetic SQL output ".repeat(100000)}\nsecret-secret Ω\n`;
  const child = spawn(process.execPath, [join(scripts, "e2e-redact-diagnostics.mjs")], {
    env: { ...process.env, DENSE_MEM_CI_REDACT_ENV_FILE: "", DENSE_MEM_CI_REDACT_EXTRA_VALUES: values.join("\n") },
  });
  let output = "", stderr = "", inputError;
  child.stdin.on("error", (error) => { inputError = error; });
  child.stdout.setEncoding("utf8");
  child.stdout.on("data", (chunk) => { output += chunk; });
  child.stderr.setEncoding("utf8");
  child.stderr.on("data", (chunk) => { stderr += chunk; });
  child.stdout.pause();
  const reader = setTimeout(() => child.stdout.resume(), 250);
  const deadline = setTimeout(() => child.kill("SIGKILL"), 10000);
  let code, signal;
  try {
    child.stdin.end(input);
    [code, signal] = await once(child, "close");
  } finally {
    clearTimeout(reader);
    clearTimeout(deadline);
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
  }
  assert.equal(code, 0, stderr);
  assert.equal(signal, null);
  assert.equal(inputError, undefined);
  assert.equal(output, input.replaceAll("secret-secret", "[REDACTED]"));
});

test("OAuth compatibility harness keeps root logs in the Compose stream", () => {
  assert.match(stack, /while \[ ! -f \/e2e\/harness-ready \]/);
  assert.match(stack, /chown densemem:densemem \/e2e\/ca\.pem \/e2e\/server\.key \/e2e\/config\.json/);
  assert.match(stack, /exec \/app\/oauth-compat-harness --listen=:9445/);
  assert.doesNotMatch(stack, /docker exec -d "\$harness" \/app\/oauth-compat-harness/);
});

function assertHostedScenarioWorkflows(production, reusable) {
  const authorize = jobScripts(workflowJob(production, "authorize"));
  assert.match(authorize, /--matrix all/);
  assert.equal(workflowJob(production, "scenarios").strategy["max-parallel"], 8);
  for (const id of ["prechecks", "database-prechecks"]) {
    const docker = actionStep(workflowJob(production, id), "docker/setup-docker-action");
    assert.equal(String(docker.with.rootless), "true");
    assert.equal(String(docker.with["set-host"]), "true");
  }
  assert.ok(Object.hasOwn(production.on, "workflow_dispatch"));
  assert.match(authorize, /parseSuccessfulPolicyStatus/);
  assert.match(authorize, /Publish trusted preview/);
  runStep(workflowJob(production, "database-prechecks"), "scripts/e2e-host-controller.sh precheck");
  for (const [id, job] of Object.entries(production.jobs)) {
    assert.ok(!["shared-start", "shared-stop", "exclusive-cleanup"].includes(id));
    assert.notEqual(job["runs-on"], "rootless-docker");
  }
  const scenario = workflowJob(reusable, "scenario");
  assert.equal(scenario["runs-on"], "ubuntu-latest");
  assert.equal(scenario["timeout-minutes"], "${{ inputs.timeout_minutes > 30 && 30 || inputs.timeout_minutes }}");
  runStep(scenario, "scripts/e2e-host-controller.sh start");
  runStep(scenario, "scripts/e2e-host-controller.sh run");
  assert.equal(runStep(scenario, "scripts/e2e-host-controller.sh stop").if, "always() && steps.stack.outputs.project != ''");
  assert.match(jobScripts(scenario), /stop-commands/);
  assert.match(JSON.stringify(scenario), /evidence_telemetry_portal/);
  for (const scope of [scenario, ...scenario.steps]) assert.equal(String(scope["continue-on-error"] ?? false), "false");
}

test("production workflows give each hosted scenario its own stack across action upgrades", () => {
  const production = parseWorkflow(productionWorkflow);
  const reusable = parseWorkflow(scenarioWorkflow);
  assertHostedScenarioWorkflows(production, reusable);
  const upgraded = structuredClone(production);
  for (const job of Object.values(upgraded.jobs)) {
    for (const step of job.steps ?? []) {
      step.name = "Renamed scenario setup";
      if (step.uses) step.uses = step.uses.split("@")[0] + "@v99";
    }
  }
  const reformatted = (workflow) => parseWorkflow(stringifyWorkflow(workflow, { indent: 4, sortKeys: true, forceQuotes: true }));
  assertHostedScenarioWorkflows(reformatted(upgraded), reformatted(reusable));
  const weak = structuredClone(production);
  actionStep(weak.jobs.prechecks, "docker/setup-docker-action").with.rootless = false;
  assert.throws(() => assertHostedScenarioWorkflows(weak, reusable));
  const missingCleanup = structuredClone(reusable);
  const cleanup = runStep(missingCleanup.jobs.scenario, "scripts/e2e-host-controller.sh stop");
  missingCleanup.jobs.scenario.steps = missingCleanup.jobs.scenario.steps.filter((step) => step !== cleanup);
  assert.throws(() => assertHostedScenarioWorkflows(production, missingCleanup));
  assert.match(controller, /--timeout 40m --total-timeout 58m/);
});

test("hosted bootstrap writes private Cloudflare configuration and masks generated credentials", async () => {
  const temp = await mkdtemp(join(tmpdir(), "dense-mem-hosted-bootstrap-"));
  try {
    const githubEnv = join(temp, "github-env");
    await writeFile(githubEnv, "");
    const { stdout } = await run("bash", [join(scripts, "e2e-ci-bootstrap.sh")], {
      env: {
        RUNNER_TEMP: temp,
        GITHUB_ENV: githubEnv,
        DOCKER_HOST: "unix:///tmp/docker.sock",
        CLOUDFLARE_ACCOUNT_ID: "a".repeat(32),
        CLOUDFLARE_API_TOKEN: "dedicated-ci-token",
      },
    });
    const configDir = join(temp, "dense-mem-ci");
    const envPath = join(configDir, ".env");
    const tokenPath = join(configDir, "telemetry-scrape-token");
    const [envText, telemetry, githubEnvText, envStat, tokenStat] = await Promise.all([
      readFile(envPath, "utf8"), readFile(tokenPath, "utf8"), readFile(githubEnv, "utf8"),
      stat(envPath), stat(tokenPath),
    ]);
    assert.equal(envStat.mode & 0o777, 0o600);
    assert.equal(tokenStat.mode & 0o777, 0o600);
    assert.match(envText, /^LOG_LEVEL=info$/m);
    assert.match(envText, /AI_API_EMBEDDING_MODEL=@cf\/baai\/bge-m3/);
    assert.match(envText, /AI_API_EMBEDDING_MAX_BATCH_ITEMS=100/);
    assert.match(envText, /AI_API_URL=https:\/\/api\.cloudflare\.com\/client\/v4\/accounts\/a{32}\/ai\/v1/);
    assert.match(envText, /AI_API_KEY=dedicated-ci-token/);
    assert.match(envText, /DOCKER_HOST=unix:\/\/\/tmp\/docker\.sock/);
    assert.ok(telemetry.trim().length >= 32);
    assert.match(githubEnvText, /DENSE_MEM_CI_HOSTED=1/);
    assert.equal((stdout.match(/::add-mask::/g) ?? []).length, 3);
    assert.doesNotMatch(stdout, /dedicated-ci-token/);
  } finally {
    await rm(temp, { recursive: true, force: true });
  }
});

test("obsolete local Compose entrypoints are removed", async () => {
  for (const name of [
    "e2e-compose.sh",
    "e2e-compose-all.sh",
    "e2e-compose-conflict.sh",
    "e2e-compose-identity-cleanup.sh",
    "e2e-compose-json.sh",
    "e2e-compose-memory-spaces.sh",
    "e2e-compose-oauth.sh",
    "e2e-compose-private-memory.sh",
    "e2e-compose-prometheus.sh",
    "e2e-compose-security.sh",
    "e2e-compose-synchronous-write.sh",
  ]) {
    await assert.rejects(readFile(join(scripts, name), "utf8"));
  }
});

test("ontology retains a complete dedicated precheck shard", async () => {
  const start = controller.indexOf("\ndatabase_case_capabilities()") + 1;
  const end = controller.indexOf("\nprecheck() {", start);
  const helper = controller.slice(start, end);
  const { stdout } = await run("bash", ["-c", `${helper}\npartition_precheck_capabilities "$1"`, "precheck-test", root]);
  const groups = stdout.trim().split(/\r?\n/).map((group) => group.split(","));
  assert.equal(groups.length, 3);
  assert.deepEqual(groups[0], ["ontology"]);
  const { stdout: expected } = await run("bash", ["-c", `${helper}\ndatabase_case_capabilities "$1" precheck`, "precheck-test", root]);
  assert.deepEqual(groups.flat().sort(), expected.trim().split(/\r?\n/).sort());
});

test("six precheck shards account for package batches while the legacy route retains its allocation", async () => {
  const fixture = await mkdtemp(join(tmpdir(), "dense-mem-precheck-package-cost-"));
  try {
    const directory = join(fixture, "scripts/e2e-db-cases");
    await mkdir(directory, { recursive: true });
    const ontologyIDs = [
      "repository/TestOntologyMaintenanceCurrentReceiptCompletion",
      "repository/TestOntologyMaintenanceFinalizationAdmissionAndIsolation",
      "repository/TestOntologyMaintenanceLargeBacklogAndFairTurns",
      "repository/TestOntologyMaintenanceReadMeasurement",
      "repository/TestOntologyOrganizationVocabularyAndSourceFences",
      "repository/TestOntologyOrganizationBatchedDependencyBounds",
      "repository/TestOntologyOrganizationRejectsStaleAndMalformedPublication",
      "repository/TestOntologyVocabularyUsesCurrentCandidateWindow",
      "repository/TestOntologyMaintenanceRevalidationDiscoveryMeasurement",
      "repository/TestOntologyRemaining",
    ];
    for (const [capability, count] of [["ontology", ontologyIDs.length], ["alpha", 41], ["beta", 40], ["gamma", 39], ["delta", 1]]) {
      const cases = Array.from({ length: count }, (_, index) => ({
        id: capability === "ontology" ? ontologyIDs[index] : `${capability}/TestCase${index}`,
        phase: "precheck",
        package: `./${capability}${capability === "beta" || capability === "gamma" ? index : ""}`,
      }));
      await writeFile(join(directory, `${capability}.json`), JSON.stringify({ version: 1, capability, cases }));
    }
    const start = controller.indexOf("\npartition_precheck_capabilities()") + 1;
    const helper = controller.slice(start, controller.indexOf("\nprecheck() {", start));
    const partition = async (count) => (await run("bash", [
      "-c", `${helper}\npartition_precheck_capabilities "$1" ${count}`, "package-cost", fixture,
    ])).stdout.trim().split(/\r?\n/);
    const six = await partition(6);
    assert.equal(six.length, 6);
    const ordinary = six.slice(3).map((selection) => selection.split(","));
    assert.deepEqual(ordinary.flat().sort(), ["alpha", "beta", "delta", "gamma"]);
    assert.ok(ordinary.find((group) => group.includes("delta")).includes("alpha"));
    assert.deepEqual(await partition(3), ["ontology", "beta,delta", "gamma,alpha"]);
    const ontologyPath = join(directory, "ontology.json");
    const incomplete = JSON.parse(await readFile(ontologyPath, "utf8"));
    incomplete.cases = incomplete.cases.filter(({ id }) => id !== "repository/TestOntologyMaintenanceCurrentReceiptCompletion");
    await writeFile(ontologyPath, JSON.stringify(incomplete));
    await assert.rejects(partition(6), /six precheck shards require dedicated ontology cases/);
    assert.deepEqual(await partition(3), ["ontology", "beta,delta", "gamma,alpha"]);
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});

test("six precheck shards preserve every real runner selection exactly once", async () => {
  const discoveryStart = controller.indexOf("\ndatabase_case_capabilities()") + 1;
  const discovery = controller.slice(discoveryStart, controller.indexOf("\npartition_precheck_capabilities()", discoveryStart));
  for (const scenario of ["ontology_organization", "ontology_management"]) {
    const result = await run("bash", ["-c", `${discovery}\ndatabase_case_capabilities "$1" scenario "$2"`, "case-discovery", root, scenario]);
    assert.equal(result.stdout, "");
  }
  const upgrade = await run("bash", ["-c", `${discovery}\ndatabase_case_capabilities "$1" scenario ontology_upgrade`, "case-discovery", root]);
  assert.notEqual(upgrade.stdout.trim(), "");
  const start = controller.indexOf("\npartition_precheck_capabilities()") + 1;
  const helper = controller.slice(start, controller.indexOf("\nprecheck() {", start));
  const command = `${helper}\npartition_precheck_capabilities "$1" 6`;
  const { stdout } = await run("bash", ["-c", command, "six-shards", root]);
  const selections = stdout.trim().split(/\r?\n/);
  assert.equal(selections.length, 6);
  const dedicated = [
    "repository/TestOntologyMaintenanceCurrentReceiptCompletion",
    "repository/TestOntologyMaintenanceFinalizationAdmissionAndIsolation",
    "repository/TestOntologyMaintenanceLargeBacklogAndFairTurns",
    "repository/TestOntologyMaintenanceReadMeasurement",
    "repository/TestOntologyOrganizationVocabularyAndSourceFences",
    "repository/TestOntologyOrganizationBatchedDependencyBounds",
    "repository/TestOntologyOrganizationRejectsStaleAndMalformedPublication",
    "repository/TestOntologyVocabularyUsesCurrentCandidateWindow",
    "repository/TestOntologyOrganizationPreservesPerRecordDependencyBounds",
    "repository/TestOntologyOrganizationPreservesValidLargeSourceContext",
    "repository/TestOntologyUnavailableSnapshotRetainsScopedHandleForMaintenance",
  ].sort();
  const measurement = "repository/TestOntologyMaintenanceRevalidationDiscoveryMeasurement";
  assert.equal(selections[1], `cases:${measurement}`);
  assert.equal(selections[0], `cases:${dedicated.join(",")}`);
  assert.ok(selections.slice(3).every((selection) => !selection.split(",").includes("community")));
  const inventory = [];
  const directory = join(root, "scripts/e2e-db-cases");
  for (const file of await readdir(directory)) {
    const fragment = JSON.parse(await readFile(join(directory, file), "utf8"));
    inventory.push(...fragment.cases.filter((item) => item.phase === "precheck").map((item) => item.id));
  }
  const actual = [];
  const counts = [];
  for (const selection of selections) {
    const args = selection.startsWith("cases:") ? ["--case", selection.slice(6)] : ["--capability", selection];
    const selected = await run("go", ["-C", join(root, "cmd/e2e"), "run", ".", "--root", root, "--phase", "precheck", ...args, "--list"]);
    const ids = selected.stdout.trim().split(/\r?\n/);
    counts.push(ids.length);
    assert.ok(ids.length > 0);
    if (selection === selections[0]) assert.deepEqual(ids, dedicated);
    if (selection === selections[1]) assert.deepEqual(ids, [measurement]);
    if (selection === selections[2]) {
      assert.ok([...dedicated, measurement].every((id) => !ids.includes(id)));
      for (const name of ["TestOntologyAtomicPublicationReplayAndRaces", "TestOntologyDefinitionChangesValidateActiveChildren", "TestOntologySemanticPublicationLogOnlyDiagnostics"]) {
        assert.ok(ids.includes(`repository/${name}`), `organizational case ${name} must execute in shard 2`);
      }
    }
    actual.push(...ids);
  }
  assert.equal(new Set(actual).size, actual.length);
  assert.deepEqual(actual.sort(), inventory.sort());
  assert.equal(actual.length, 617);
  assert.deepEqual(counts, [11, 1, 138, 170, 126, 171]);
  assert.equal((await run("bash", ["-c", command, "six-shards", root])).stdout, stdout);
  await assert.rejects(run("bash", ["-c", `${helper}\npartition_precheck_capabilities "$1" 4`, "bad-shards", root]), /three or six shards/);
});
