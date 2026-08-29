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

test("compose runner filters the resolved default slice", async () => {
  const overlay = await readFile(new URL("../../../scripts/e2e-compose-synchronous-write.sh", import.meta.url), "utf8");
  const compose = await readFile(new URL("../../../scripts/e2e-compose.sh", import.meta.url), "utf8");
  assert.match(overlay, /local slice="\$\{DENSE_MEM_E2E_WRITE_SLICE:-legacy\}"/);
  assert.match(overlay, /local api_key="\$2"/);
  assert.match(overlay, /DENSE_MEM_E2E_WRITE_CASE="\$slice"/);
  assert.match(compose, /run_synchronous_write_e2e "\$team_id" "\$api_key"/);
});

test("remember case covers mixed object success and a late search-generation fence", async () => {
  const remember = await readFile(new URL("./cases/remember.mjs", import.meta.url), "utf8");
  assert.match(remember, /mixed-objects/);
  assert.match(remember, /search-generation-rotation/);
  assert.match(remember, /commit_conflict/);
});

test("correction slice contains executable success and provider-failure assertions", async () => {
  const correction = await readFile(new URL("./cases/correction.mjs", import.meta.url), "utf8");
  assert.doesNotMatch(correction, /reserved-for-adoption/);
  assert.match(correction, /correct_relationship/);
  assert.match(correction, /provider_failure_preserved/);
  assert.match(correction, /provider_timeout_preserved/);
  assert.match(correction, /embedding_timeout/);
});
