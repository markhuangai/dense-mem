import assert from "node:assert/strict";
import { chmod, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { test } from "node:test";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";

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
]);

async function executable(path, contents) {
  await writeFile(path, contents, { mode: 0o700 });
  await chmod(path, 0o700);
}

async function run(command, args, options = {}) {
  const { env: overrides, ...commandOptions } = options;
  const env = { ...process.env, ...overrides };
  for (const name of Object.keys(env)) {
    if (name.startsWith("GIT_")) delete env[name];
  }
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
    assert.ok(wrapper.includes("precheck_capability"));
    const script = `#!/usr/bin/env bash
set -euo pipefail
${wrapper}
precheck_capability() {
  printf 'capability=%s\\n' "$5"
  [[ "$5" != postgres ]]
}
precheck 123 1 ghcr.io/markhuangai/dense-mem:test@sha256:${"1".repeat(64)} /workspace
`;
    const scriptPath = join(fixture, "partition-test.sh");
    await executable(scriptPath, script);
    await assert.rejects(
      run("bash", [scriptPath], { env: { TMPDIR: fixture } }),
      (error) => {
        const output = `${error.stdout || ""}${error.stderr || ""}`;
        assert.match(output, /capability=repository/);
        assert.match(output, /capability=postgres/);
        assert.match(output, /capability=migration,http,service/);
        return true;
      },
    );
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
  assert.match(postgres, /--scenario identity_cleanup --capability postgres \\\s+--timeout 20m --total-timeout 25m/);
  assert.doesNotMatch(postgres, /--case postgres\/TestIdentityCleanupComposeSeed/);
  assert.match(controller, /provision_postgres_runtime_role/);
  assert.match(controller, /verify_postgres_runtime_migration_state/);
  assert.match(controller, /docker network inspect --format '[^']*\.Containers/);
  assert.match(controller, /docker rm -f "\$container_id"/);
});

test("Compose stack has no host bindings and carries only project-scoped inputs", () => {
  assert.doesNotMatch(compose, /^\s+ports:/m);
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
  const verifierEnd = stack.indexOf('if (has("conflict_provider"))', verifierStart);
  assert.ok(verifierStart >= 0 && verifierEnd > verifierStart);
  const verifierBlock = stack.slice(verifierStart, verifierEnd);
  assert.match(verifierBlock, /AI_VERIFIER_API_URL: "http:\/\/synchronous-write-provider:8787\/v1"/);
  assert.doesNotMatch(verifierBlock, /\bAI_API_URL:/);
  assert.match(stack, /has_helper "\$helpers" verifier \|\| has_helper "\$helpers" synchronous_write/);
});

test("scenario runner executes Entra and diagnostics through the shared path", () => {
  assert.match(scenario, /dense-mem CI scenario \[%s\]/);
  assert.match(scenario, /log "running \$\{script\}"/);
  assert.match(scenario, /log "running Playwright specs: \$\{specs\[\*\]\}"/);
  assert.match(scenario, /Entra OIDC mock/);
  assert.match(scenario, /tests\/uat\/entra_scim_e2e\.mjs/);
  assert.match(scenario, /synchronous_write\) specs=\("tests-compose\/remember-attempts\.spec\.ts"\)/);
  assert.match(scenario, /DENSE_MEM_E2E_DIAGNOSTICS_FIXTURE_FILE/);
  assert.doesNotMatch(scenario, /parse_json_dream_statement|DENSE_MEM_E2E_DREAM_STATEMENT.*synchronous/);
  assert.match(runtime, /failed stack diagnostics/);
  assert.match(runtime, /ci_compose logs --no-color --timestamps --tail 200/);
});

test("production workflows use capability-matched runners and one OCI handoff", () => {
  assert.match(productionWorkflow, /runs-on: ubuntu-latest/);
  assert.match(productionWorkflow, /runs-on: rootless-docker/);
  assert.match(productionWorkflow, /max-parallel: 4/);
  assert.match(productionWorkflow, /shared_project: \$\{\{ steps\.start\.outputs\.shared_project \}\}/);
  assert.match(productionWorkflow, /scripts\/e2e-scenario-registry\.mjs --validate-compatible/);
  assert.match(productionWorkflow, /for selection in repository postgres migration,http,service/);
  assert.match(controller, /--total-timeout 25m/);
  assert.doesNotMatch(productionWorkflow, /const isolations = new Set\(\["exclusive", "shared_team"\]\)/);
  assert.doesNotMatch(productionWorkflow, /rootless-docker-shared|runs-on:\s*pc|workflow_dispatch|actions\/download-artifact|actions\/upload-artifact/);
  assert.match(scenarioWorkflow, /runs-on: rootless-docker/);
  assert.match(scenarioWorkflow, /actions\/setup-node@v7/);
  assert.match(scenarioWorkflow, /stop-commands/);
  assert.match(scenarioWorkflow, /dreaming_telemetry_portal/);
  assert.doesNotMatch(scenarioWorkflow, /continue-on-error|Preserve scenario result|Print failed scenario diagnostics|tee "\$\{log\}"/);
  assert.doesNotMatch(scenarioWorkflow, /actions\/upload-artifact/);
  assert.doesNotMatch(controller, /conflict_provider\|conflict_review\|oauth\|oauth_compatibility\|playwright\|synchronous_write\|verifier/);
  assert.doesNotMatch(stack, /local source_dir="\$1" project="\$2" postgres_user=/);
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
