import assert from "node:assert/strict";
import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { fixtureChatResponse } from "./provider-fixture.mjs";
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

test("provider fixture implements the community and dream schemas used by verifier scenarios", () => {
  const relationshipID = "11111111-1111-4111-8111-111111111111";
  const evidenceID = "22222222-2222-4222-8222-222222222222";
  const community = fixtureChatResponse(structuredRequest("community_summary", {
    community_id: "community_1",
    summary_input_hash: "sha256:community-input",
    relationships: [{
      relationship_id: relationshipID,
      evidence_ids: [evidenceID],
      support_quotes: [{ evidence_id: evidenceID, quote: "Dense-Mem uses PostgreSQL." }],
      subject: "Dense-Mem",
      predicate: "uses",
      object: "PostgreSQL",
    }],
  }));
  assert.deepEqual(community.admitted_relationship_ids, [relationshipID]);
  assert.deepEqual(community.admitted_evidence_ids, [evidenceID]);
  assert.deepEqual(community.admitted_support_quotes, [{ evidence_id: evidenceID, quote: "Dense-Mem uses PostgreSQL." }]);
  assert.deepEqual(community.top_entities, ["Dense-Mem", "PostgreSQL"]);
  assert.deepEqual(community.top_predicates, ["uses"]);

  const dream = fixtureChatResponse(structuredRequest("dense_mem_dream_generation_response", {
    request_id: "dream_request_1",
    max_outputs: 1,
    paths: [{
      path_ref: "path_1",
      subject: { ref: "node_1", display: "Dense-Mem", kind: "project" },
      middle: { ref: "node_2", display: "Runtime", kind: "product" },
      object: { ref: "node_3", display: "PostgreSQL", kind: "product" },
      premises: [
        { evidence: [{ evidence_ref: "evidence_1" }] },
        { evidence: [{ evidence_ref: "evidence_2" }] },
      ],
      allowed_predicates: [{ predicate_ref: "predicate_1", label: "uses" }],
    }],
  }));
  assert.equal(dream.request_id, "dream_request_1");
  assert.equal(dream.proposals.length, 1);
  assert.equal(dream.proposals[0].path_ref, "path_1");
  assert.equal(dream.proposals[0].predicate_ref, "predicate_1");
  assert.deepEqual(dream.proposals[0].evidence_refs, ["evidence_1", "evidence_2"]);
});

function structuredRequest(schemaName, input) {
  return {
    messages: [{ role: "user", content: JSON.stringify(input) }],
    response_format: { json_schema: { name: schemaName } },
  };
}

test("synchronous-write provider remains a project-scoped Compose helper", async () => {
  const compose = await readFile(new URL("../../../scripts/e2e-stack.yml", import.meta.url), "utf8");
  const controller = await readFile(new URL("../../../scripts/e2e-host-controller.sh", import.meta.url), "utf8");
  const stack = await readFile(new URL("../../../scripts/e2e-host-controller-stack.sh", import.meta.url), "utf8");
  const processor = await readFile(new URL("../../../cmd/internal/serverapp/remember_processor_integration_test.go", import.meta.url), "utf8");
  assert.match(compose, /synchronous-write-provider-files/);
  assert.match(compose, /profiles: \[synchronous_write, verifier\]/);
  assert.match(stack, /provider-fixture\.mjs/);
  assert.match(stack, /DENSE_MEM_E2E_PROVIDER_TIMEOUT_DELAY_MS/);
  assert.match(controller, /TestConflictSnapshotScopeSerializesPlacementReviewAndWrite/);
  assert.match(controller, /TestConflictSnapshotScopeLocksCorrectionBeforeReviewRowLock/);
  assert.doesNotMatch(controller, /TestConflictSnapshotScopeSerializesCorrectionBeforeReviewRowLock/);
  assert.doesNotMatch(stack, /TestConflictSnapshotScopeSerializesPlacementReviewAndWrite/);
  assert.match(stack, /TestRememberServiceRejectsHistoricalOutcomesThroughPostgres/);
  assert.match(processor, /func TestRememberServiceRejectsHistoricalOutcomesThroughPostgres/);
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
