import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

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
]);

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
  assert.doesNotMatch(local, /DOCKER_HOST=|rootless/);
  assert.deepEqual(JSON.parse(packageJSON).scripts, { e2e: "bash scripts/e2e.sh" });
});

test("local worktree transfer filters secrets and generated artifacts", () => {
  assert.match(controller, /telemetry-scrape-token/);
  assert.match(controller, /test-results/);
  assert.match(controller, /playwright-report/);
  assert.match(controller, /densemem-e2e-/);
  assert.match(controller, /tar --null --verbatim-files-from/);
  assert.match(controller, /--transform='s,\^,workspace\//);
});

test("shared PostgreSQL provisioning keeps runtime identity least-privileged", () => {
  assert.match(postgres, /DENSE_MEM_CI_BOOTSTRAP_POSTGRES_USER="densemem_e2e_bootstrap"/);
  assert.match(postgres, /CREATE EXTENSION IF NOT EXISTS vector/);
  assert.match(postgres, /NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS/);
  assert.match(postgres, /ALTER DATABASE :"runtime_database" OWNER TO densemem_e2e_database_owner/);
  assert.match(postgres, /role\.rolbypassrls/);
  assert.match(postgres, /run_identity_cleanup_startup_matrix/);
  assert.match(controller, /provision_postgres_runtime_role/);
  assert.match(controller, /verify_postgres_runtime_migration_state/);
});

test("Compose stack has no host bindings and carries only project-scoped inputs", () => {
  assert.doesNotMatch(compose, /^\s+ports:/m);
  assert.match(compose, /POSTGRES_USER: densemem_e2e_bootstrap/);
  assert.match(compose, /env_file:\n\s+- \$\{DENSE_MEM_CI_ENV_FILE/);
  assert.match(compose, /prometheus-config:/);
  assert.match(compose, /telemetry-scrape-token:/);
  assert.match(compose, /entra-mock:/);
  assert.match(compose, /profiles: \[oauth_compatibility\]/);
  assert.match(compose, /external: true/);
});

test("scenario runner executes Entra and diagnostics through the shared path", () => {
  assert.match(scenario, /Entra OIDC mock/);
  assert.match(scenario, /tests\/uat\/entra_scim_e2e\.mjs/);
  assert.match(scenario, /synchronous_write\) specs=\("tests-compose\/remember-attempts\.spec\.ts"\)/);
  assert.match(scenario, /DENSE_MEM_E2E_DIAGNOSTICS_FIXTURE_FILE/);
  assert.doesNotMatch(scenario, /parse_json_dream_statement|DENSE_MEM_E2E_DREAM_STATEMENT.*synchronous/);
});

test("production workflows use capability-matched runners and one OCI handoff", () => {
  assert.match(productionWorkflow, /runs-on: docker-runner/);
  assert.match(productionWorkflow, /runs-on: rootless-docker/);
  assert.match(productionWorkflow, /max-parallel: 4/);
  assert.match(productionWorkflow, /shared_project: \$\{\{ steps\.start\.outputs\.shared_project \}\}/);
  assert.match(productionWorkflow, /scripts\/e2e-scenario-registry\.mjs --validate-compatible/);
  assert.doesNotMatch(productionWorkflow, /rootless-docker-shared|runs-on:\s*pc|workflow_dispatch|actions\/download-artifact|actions\/upload-artifact/);
  assert.match(scenarioWorkflow, /runs-on: rootless-docker/);
  assert.match(scenarioWorkflow, /actions\/setup-node@v7/);
  assert.match(scenarioWorkflow, /Print failed scenario diagnostics/);
  assert.doesNotMatch(scenarioWorkflow, /actions\/upload-artifact/);
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
