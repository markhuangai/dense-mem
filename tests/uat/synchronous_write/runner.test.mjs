import assert from "node:assert/strict";
import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { discoverCases } from "./runner.mjs";

test("synchronous-write cases are sorted and filterable", async () => {
  const directory = join(tmpdir(), "dense-mem-synchronous-write-" + Date.now() + "-" + process.pid);
  await mkdir(join(directory, "nested"), { recursive: true });
  await writeFile(join(directory, "zeta.mjs"), "export const name = 'zeta'; export const run = () => ({});\\n");
  await writeFile(join(directory, "alpha.mjs"), "export const name = 'alpha'; export const run = () => ({});\\n");
  await writeFile(join(directory, "README.md"), "ignored\\n");
  try {
    const all = await discoverCases(directory, "");
    assert.deepEqual(all.map((url) => url.split("/").pop()), ["alpha.mjs", "zeta.mjs"]);
    const filtered = await discoverCases(directory, "zeta");
    assert.deepEqual(filtered.map((url) => url.split("/").pop()), ["zeta.mjs"]);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("shared scenario runner owns synchronous-write diagnostics coverage", async () => {
  const scenario = await readFile(new URL("../../../scripts/e2e-scenario.sh", import.meta.url), "utf8");
  assert.match(scenario, /run_node_case tests\/uat\/synchronous_write\/runner\.mjs/);
  assert.match(scenario, /synchronous_write\) specs=\("tests-compose\/remember-attempts\.spec\.ts"\)/);
  assert.match(scenario, /DENSE_MEM_E2E_DIAGNOSTICS_FIXTURE_FILE/);
  assert.doesNotMatch(scenario, /parse_json_dream_statement/);
});

test("provider fixture retains bounded timeout and fault behavior", async () => {
  const fixture = await readFile(new URL("./provider-fixture.mjs", import.meta.url), "utf8");
  assert.match(fixture, /timeoutDelayMs/);
  assert.match(fixture, /embeddingCallsByFault/);
  assert.match(fixture, /routeFault === "embedding-cancel" && embeddingFaultCall === 1/);
});

test("synchronous-write provider remains a project-scoped Compose helper", async () => {
  const compose = await readFile(new URL("../../../scripts/e2e-stack.yml", import.meta.url), "utf8");
  const stack = await readFile(new URL("../../../scripts/e2e-host-controller-stack.sh", import.meta.url), "utf8");
  assert.match(compose, /synchronous-write-provider-files/);
  assert.match(compose, /profiles: \[synchronous_write, verifier\]/);
  assert.match(stack, /provider-fixture\.mjs/);
  assert.match(stack, /DENSE_MEM_E2E_PROVIDER_TIMEOUT_DELAY_MS/);
});

test("legacy local Compose fragments are no longer imported", async () => {
  const entries = await readdir(new URL("../../../scripts", import.meta.url));
  assert.equal(entries.some((entry) => entry.startsWith("e2e-compose")), false);
});

test("remember case covers semantic duplicate reuse and unauthorized candidates", async () => {
  const remember = await readFile(new URL("./cases/remember.mjs", import.meta.url), "utf8");
  const fixture = await readFile(new URL("./provider-fixture.mjs", import.meta.url), "utf8");
  assert.match(remember, /runSemanticDuplicateCase/);
  assert.match(remember, /semantic-reuse/);
  assert.match(remember, /occurrences_preserved/);
  assert.match(fixture, /semantic-reuse-unauthorized/);
});
