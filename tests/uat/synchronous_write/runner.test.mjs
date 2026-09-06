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

test("provider fixture implements the community, dream, and evidence-discovery schemas", () => {
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

  const evidence = fixtureChatResponse(structuredRequest("dense_mem_evidence_discovery_response", {
    request_id: "evidence_request_1",
    max_outputs: 1,
    contexts: [{
      evidence_ref: "evidence_target",
      boundary_text: "⟦bfixture_0⟧Dense-Mem uses PostgreSQL.⟦bfixture_1⟧",
    }],
    nodes: [
      { ref: "node_1", display: "Dense-Mem", kind: "project" },
      { ref: "node_2", display: "PostgreSQL", kind: "product" },
    ],
    allowed_predicates: [{ ref: "predicate_1", label: "uses", version: 1 }],
  }));
  assert.equal(evidence.request_id, "evidence_request_1");
  assert.equal(evidence.proposals.length, 1);
  assert.equal(evidence.proposals[0].predicate_ref, "predicate_1");
  assert.deepEqual(evidence.proposals[0].derivations, [{
    evidence_ref: "evidence_target", start_ref: "bfixture_0", end_ref: "bfixture_1",
  }]);
  const duplicateSuppressed = fixtureChatResponse(structuredRequest("dense_mem_evidence_discovery_response", {
    request_id: "evidence_request_2",
    max_outputs: 1,
    contexts: [{
      evidence_ref: "evidence_target",
      boundary_text: "⟦bfixture_0⟧Dense-Mem uses PostgreSQL.⟦bfixture_1⟧",
    }],
    nodes: [
      { ref: "node_1", display: "Dense-Mem", kind: "project" },
      { ref: "node_2", display: "PostgreSQL", kind: "product" },
    ],
    allowed_predicates: [{ ref: "predicate_1", label: "uses", version: 1 }],
    related_hypotheses: [{ subject_ref: "node_1", predicate: "predicate_1", object_ref: "node_2" }],
  }));
  assert.equal(duplicateSuppressed.proposals.length, 0);
});

test("team-dreaming Compose UAT covers hourly evidence discovery and adverse eligibility", async () => {
  const scenario = await readFile(new URL("../team_dreaming_e2e.mjs", import.meta.url), "utf8");
  assert.match(scenario, /waitForHourlyEvidenceRun/);
  assert.match(scenario, /lane, "evidence_discovery"/);
  assert.match(scenario, /evidence_targets/);
  assert.match(scenario, /quarantinedContent/);
  assert.match(scenario, /confirm_true/);
  assert.match(scenario, /createAdverseEvidenceTeam/);
  assert.match(scenario, /evidence_failure_team_name/);
  assert.match(scenario, /provider_failed/);
  assert.match(scenario, /evidence search-document seed/);
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
  const processor = await readFile(new URL("../../../cmd/internal/serverapp/remember_processor_integration.e2e", import.meta.url), "utf8");
  assert.match(compose, /synchronous-write-provider-files/);
  assert.match(compose, /profiles: \[synchronous_write, verifier\]/);
  assert.match(stack, /provider-fixture\.mjs/);
  assert.match(stack, /DENSE_MEM_E2E_PROVIDER_TIMEOUT_DELAY_MS/);
  assert.match(controller, /go -C cmd\/e2e run \. --root \/workspace/);
  assert.match(stack, /--scenario synchronous_write_primitives/);
  assert.match(stack, /--capability repository,service,server/);
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

test("remember case covers bounded assessor context selection and replay", async () => {
  const remember = await readFile(new URL("./cases/remember.mjs", import.meta.url), "utf8");
  assert.match(remember, /runBudgetContextCase/);
  assert.match(remember, /optional duplicate candidates were omitted/);
  assert.match(remember, /bounded candidate selection replay must be byte-equivalent/);
  assert.match(remember, /semantic_assessments/);
});

test("remember case covers cited evidence conflict creation, recurrence, resolution, dismissal, and similarity-only rejection", async () => {
  const remember = await readFile(new URL("./cases/remember.mjs", import.meta.url), "utf8");
  const fixture = await readFile(new URL("./provider-fixture.mjs", import.meta.url), "utf8");
  assert.match(remember, /runEvidenceConflictCase/);
  assert.match(remember, /cited-evidence-conflict/);
  assert.match(remember, /similarity-only evidence must not create/);
  assert.match(fixture, /evidenceConflictResults/);
});

test("remember PostgreSQL fixtures use the consolidated production runner handoff", async () => {
  const remember = await readFile(new URL("./cases/remember.mjs", import.meta.url), "utf8");
  const scenario = await readFile(new URL("../../../scripts/e2e-scenario.sh", import.meta.url), "utf8");
  const controller = await readFile(new URL("../../../scripts/e2e-host-controller-runtime.sh", import.meta.url), "utf8");
  assert.match(remember, /fileURLToPath\(new URL\("\.\.\/\.\.\/\.\.\/\.\.", import\.meta\.url\)\)/);
  assert.match(remember, /DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE/);
  assert.match(remember, /SET LOCAL app\.tx_mode = 'system'/);
  assert.match(remember, /only permits read queries/);
  assert.match(remember, /only permits alias setup/);
  assert.match(remember, /psql -X -q -v ON_ERROR_STOP=1/);
  assert.match(remember, /Remember PostgreSQL fixture failed \(\$\{result\.status\}\):/);
  assert.match(remember, /requiredEnv\("DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE"\)/);
  assert.match(scenario, /export DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE=/);
  assert.match(scenario, /specs\+=\("tests-compose\/compose-evidence-conflict\.spec\.ts"\)/);
  assert.match(controller, /DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE=\/ci\/helper-compose\.yml/);
});
