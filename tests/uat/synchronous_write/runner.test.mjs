import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { discoverCases } from "./runner.mjs";

test("synchronous-write cases are sorted and filterable", async () => {
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-synchronous-write-"));
  await mkdir(join(directory, "nested"));
  await writeFile(join(directory, "zeta.mjs"), "export const name = 'zeta'; export const run = () => ({});\n");
  await writeFile(join(directory, "alpha.mjs"), "export const name = 'alpha'; export const run = () => ({});\n");
  await writeFile(join(directory, "README.md"), "ignored\n");
  const all = await discoverCases(directory, "");
  assert.deepEqual(all.map((url) => url.split("/").pop()), ["alpha.mjs", "zeta.mjs"]);
  const filtered = await discoverCases(directory, "zeta");
  assert.deepEqual(filtered.map((url) => url.split("/").pop()), ["zeta.mjs"]);
});

test("provider timeout fault is longer than the pinned E2E request caps", async () => {
  const overlay = await readFile(new URL("../../../scripts/e2e-compose-synchronous-write.sh", import.meta.url), "utf8");
  const fixture = await readFile(new URL("./provider-fixture.mjs", import.meta.url), "utf8");
  assert.match(overlay, /AI_API_EMBEDDING_TIMEOUT_SECONDS: "2"/);
  assert.match(overlay, /AI_VERIFIER_TIMEOUT_SECONDS: "2"/);
  assert.match(overlay, /AI_VERIFIER_API_KEY: dense-mem-e2e-verifier-key/);
  assert.match(overlay, /DENSE_MEM_E2E_PROVIDER_TIMEOUT_DELAY_MS: "5000"/);
  assert.match(overlay, /provider_dimensions=.*compose_server_environment_value AI_API_EMBEDDING_DIMENSIONS/);
  assert.match(fixture, /timeoutDelayMs/);
});

test("embedding cancellation delay is scoped by provider fault", async () => {
  const fixture = await readFile(new URL("./provider-fixture.mjs", import.meta.url), "utf8");
  assert.match(fixture, /const embeddingCallsByFault = new Map\(\)/);
  assert.match(fixture, /embeddingCallsByFault\.get\(embeddingFaultKey\)/);
  assert.match(fixture, /routeFault === "embedding-cancel" && embeddingFaultCall === 1/);
  assert.doesNotMatch(fixture, /routeFault === "embedding-cancel" && embeddingCalls === 1/);
});

test("provider fixture uses a Compose volume instead of a worktree bind mount", async () => {
  const overlay = await readFile(new URL("../../../scripts/e2e-compose-synchronous-write.sh", import.meta.url), "utf8");
  assert.match(overlay, /e2e-synchronous-write-provider-files:\/e2e/);
  assert.match(overlay, /prepare_synchronous_write_provider_fixture_volume/);
  assert.match(overlay, /docker cp/);
  assert.doesNotMatch(overlay, /provider-fixture\.mjs:\/e2e\/provider-fixture\.mjs:ro/);
});

test("compose runner filters the requested test case without a runtime write slice", async () => {
  const overlay = await readFile(new URL("../../../scripts/e2e-compose-synchronous-write.sh", import.meta.url), "utf8");
  const conflict = await readFile(new URL("../../../scripts/e2e-compose-conflict.sh", import.meta.url), "utf8");
  const compose = await readFile(new URL("../../../scripts/e2e-compose.sh", import.meta.url), "utf8");
  assert.doesNotMatch(overlay, /DENSE_MEM_E2E_WRITE_SLICE/);
  assert.match(overlay, /local api_key="\$2"/);
  assert.match(overlay, /DENSE_MEM_E2E_WRITE_CASE="\$case_name"/);
  assert.match(overlay, /if \[\[ -z "\$case_name" \|\| "\$case_name" == "conflict" \]\]/);
  assert.match(conflict, /conflict_provider_required\(\) \{[\s\S]*-z "\$\{DENSE_MEM_E2E_WRITE_CASE:-\}"/);
  assert.match(conflict, /conflict_server_provider_required\(\)/);
  assert.match(compose, /run_synchronous_write_e2e "\$team_id" "\$api_key"/);
});

test("compose primitives case runs the internal PostgreSQL driver and public Remember regression", async () => {
  const overlay = await readFile(new URL("../../../scripts/e2e-compose-synchronous-write.sh", import.meta.url), "utf8");
  const compose = await readFile(new URL("../../../scripts/e2e-compose.sh", import.meta.url), "utf8");
  const driver = await readFile(new URL("../../../internal/repository/remember_primitives_compose_e2e_test.go", import.meta.url), "utf8");
  const processor = await readFile(new URL("../../../cmd/internal/serverapp/remember_processor_integration_test.go", import.meta.url), "utf8");
  const assessorDriver = await readFile(new URL("../../../internal/service/memoryservice/synchronous_assessment_compose_e2e_test.go", import.meta.url), "utf8");
  assert.match(overlay, /run_synchronous_write_primitives_e2e\(\)/);
  assert.match(overlay, /public_case_name="remember"/);
  assert.match(compose, /DENSE_MEM_E2E_WRITE_CASE:-\}.*primitives/);
  assert.match(compose, /run_synchronous_write_primitives_e2e/);
  assert.doesNotMatch(compose, /if \[\[ "\$E2E_SCENARIO" == "synchronous_write_primitives" \]\]; then\s+exit 0/);
  assert.match(compose, /if \[\[ "\$E2E_SCENARIO" == "synchronous_write" \|\| "\$E2E_SCENARIO" == "synchronous_write_primitives" \]\]; then[\s\S]*run_synchronous_write_e2e "\$team_id" "\$api_key"/);
  assert.match(driver, /go:build compose_e2e/);
  assert.match(driver, /TestComposeRememberPrimitives/);
  assert.match(overlay, /TestComposeSynchronousEvidenceOnlyAssessorBatch/);
  assert.match(assessorDriver, /TestComposeSynchronousEvidenceOnlyAssessorBatch/);
  assert.match(overlay, /TestRememberServiceRejectsHistoricalOutcomesThroughPostgres/);
  assert.match(processor, /TestRememberServiceRejectsHistoricalOutcomesThroughPostgres/);
});

test("remember case covers mixed object success and idempotency conflict behavior", async () => {
  const remember = await readFile(new URL("./cases/remember.mjs", import.meta.url), "utf8");
  assert.match(remember, /mixed-objects/);
  assert.match(remember, /changed-hash/);
});

test("remember PostgreSQL fixtures follow the production runner handoff", async () => {
  const remember = await readFile(new URL("./cases/remember.mjs", import.meta.url), "utf8");
  const localOverlay = await readFile(new URL("../../../scripts/e2e-compose-synchronous-write.sh", import.meta.url), "utf8");
  const scenario = await readFile(new URL("../../../scripts/e2e-ci-scenario.sh", import.meta.url), "utf8");
  const controller = await readFile(new URL("../../../scripts/e2e-host-controller-runtime.sh", import.meta.url), "utf8");
  assert.match(remember, /fileURLToPath\(new URL\("\.\.\/\.\.\/\.\.\/\.\.", import\.meta\.url\)\)/);
  assert.match(remember, /DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE/);
  assert.match(remember, /SET LOCAL app\.tx_mode = 'system'/);
  assert.match(remember, /only permits read queries/);
  assert.match(remember, /only permits alias setup/);
  assert.match(remember, /psql -X -q -v ON_ERROR_STOP=1/);
  assert.match(remember, /Remember PostgreSQL fixture failed \(\$\{result\.status\}\):/);
  assert.match(remember, /requiredEnv\("DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE"\)/);
  assert.match(localOverlay, /DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE="\$SYNCHRONOUS_WRITE_COMPOSE_OVERLAY_FILE"/);
  assert.match(scenario, /export DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE=/);
  assert.match(controller, /DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE=\/ci\/helper-compose\.yml/);
});

test("correction slice contains executable success and provider-failure assertions", async () => {
  const correction = await readFile(new URL("./cases/correction.mjs", import.meta.url), "utf8");
  assert.doesNotMatch(correction, /reserved-for-adoption/);
  assert.match(correction, /correct_relationship/);
  assert.match(correction, /provider_failure_preserved/);
  assert.match(correction, /provider_timeout_preserved/);
  assert.match(correction, /request_timeout/);
});

test("diagnostics slice contains control API and scrubbed artifact assertions", async () => {
  const diagnostics = await readFile(new URL("./cases/diagnostics.mjs", import.meta.url), "utf8");
  const overlay = await readFile(new URL("../../../scripts/e2e-compose-synchronous-write.sh", import.meta.url), "utf8");
  const compose = await readFile(new URL("../../../scripts/e2e-compose.sh", import.meta.url), "utf8");
  assert.doesNotMatch(diagnostics, /reserved-for-adoption/);
  assert.match(diagnostics, /remember-attempts/);
  assert.match(diagnostics, /\{"phase":"assessment","code":"provider_unavailable"\}/);
  assert.match(diagnostics, /Cache-Control|cache-control|no-store/);
  assert.match(overlay, /run_compose_playwright_tests remember_attempts/);
  assert.match(compose, /remember-attempts\.spec\.ts/);
});

test("contract slice asserts target catalog, terminal errors, correction, and parity", async () => {
  const contract = await readFile(new URL("./cases/contract.mjs", import.meta.url), "utf8");
  assert.doesNotMatch(contract, /reserved-for-adoption/);
  assert.match(contract, /exactly ten tools/);
  assert.match(contract, /get_submission_status/);
  assert.match(contract, /structuredContent/);
  assert.match(contract, /correct_relationship/);
});

test("contract ownership callers validate the user endpoint before sending credentials", async () => {
  const contract = await readFile(new URL("./cases/contract.mjs", import.meta.url), "utf8");
  assert.match(contract, /validatedUserURL\(\);/);
  assert.match(contract, /validatedEndpointURL\("DENSE_MEM_USER_URL"\)/);
  assert.match(contract, /must use HTTPS or loopback HTTP/);
  assert.match(contract, /const baseURL = validatedUserURL\(\);/);
});
