#!/usr/bin/env node
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
let rpcID = 0;
const ownerProfileID = await apiCredentialOwnerID();
const adverseTeam = await createAdverseEvidenceTeam();
const seeded = await seedSchedulerInputs(ownerProfileID);
const evidenceSeeded = await seedEvidenceDiscoveryInputs(ownerProfileID);
const historical = seedHistoricalEvidenceHypothesis(ownerProfileID, evidenceSeeded, seeded);
const captureID = randomUUID();
postgresQuery(`INSERT INTO dream_diagnostic_captures(team_id,capture_id,run_id,phase,outcome,capture_state,details,payload,expires_at)
 VALUES(${sqlLiteral(teamID)}::uuid,${sqlLiteral(captureID)}::uuid,${sqlLiteral(historical.runID)}::uuid,'run','completed','captured','{"historical":true}','{"historical":true}',now()+interval '1 day')`);
const historyBefore = postgresQuery(`SELECT jsonb_agg(to_jsonb(hypothesis))::text FROM hypotheses AS hypothesis WHERE team_id=${sqlLiteral(teamID)}::uuid`);
const status = (await controlJSON(`/teams/${teamID}/dreaming/status`)).data;
assert.equal(status.retired, true);
assert.equal(status.historical_proposed_count, 1);
const history = (await controlJSON(`/teams/${teamID}/dreams`)).data.items;
assert(history.some((item) => item.dream_id === historical.hypothesisID && item.evidence_derivations.length === 1));
const operatorDenied = await fetch(`${controlURL}/control/api/teams/${teamID}/dreams`, {headers:{Authorization:`Bearer ${apiKey}`}});
assert.equal(operatorDenied.status,401);
for (const name of ["list_dreams", "get_dream", "resolve_dream_feedback"]) {
 const response = await fetch(`${userURL}/mcp`,{method:"POST",headers:{Authorization:`Bearer ${apiKey}`,Accept:"application/json","Content-Type":"application/json"},body:JSON.stringify({jsonrpc:"2.0",id:++rpcID,method:"tools/call",params:{name,arguments:{}}})});
 assert.equal((await response.json()).error.code,-32601);
}
const recalled = await mcpTool(apiKey,"recall_memory",{query:"Dense-Mem PostgreSQL",limit:10});
assert(!Object.hasOwn(recalled,"related_hypotheses"));
assert(recalled.results.some((item)=>item.evidence_id===evidenceSeeded.targetID));
assert.equal(postgresQuery(`SELECT jsonb_agg(to_jsonb(hypothesis))::text FROM hypotheses AS hypothesis WHERE team_id=${sqlLiteral(teamID)}::uuid`),historyBefore);
const provider = await fetch(`${requiredEnv("DENSE_MEM_E2E_PROVIDER_URL")}/health`);
assert.equal(provider.status,200);
assert.equal(((await provider.json()).chat_requests||[]).filter((item)=>["dense_mem_dream_generation_response","dense_mem_evidence_discovery_response","community_summary"].includes(item.schema_name)).length,0);
console.log(JSON.stringify({status:"ok",statement:historical.statement,evidence_hypothesis_id:historical.hypothesisID,evidence_dream_statement:historical.statement,evidence_target_id:evidenceSeeded.targetID,evidence_target_content:evidenceSeeded.targetContent,evidence_failure_run_id:historical.failureRunID,evidence_failure_team_name:adverseTeam.teamName,history_retained:true,clients_retired:true}));

async function apiCredentialOwnerID() {
  const session = await userJSON("/ui/api/session");
  const credentialID = session.data?.credential?.id;
  if (typeof credentialID !== "string" || !credentialID) {
    throw new Error(`user session did not return a credential ID: ${JSON.stringify(session)}`);
  }
  return credentialID;
}


async function seedSchedulerInputs(ownerProfileID) {
  const subjectID = randomUUID();
  const middleID = randomUUID();
  const objectID = randomUUID();
  const firstRelationshipID = randomUUID();
  const secondRelationshipID = randomUUID();
  const ingestID = randomUUID();
  const firstFragmentID = randomUUID();
  const secondFragmentID = randomUUID();
  const firstObservationID = randomUUID();
  const secondObservationID = randomUUID();
  const firstVerificationID = randomUUID();
  const secondVerificationID = randomUUID();
  const firstSupportID = randomUUID();
  const secondSupportID = randomUUID();
  const firstQuote = "Dense-Mem uses the Runtime service to process memory requests.";
  const secondQuote = "The Runtime service uses PostgreSQL to store durable memory records.";
  const firstHash = sha256Hash(firstQuote);
  const secondHash = sha256Hash(secondQuote);
  const searchDocuments = await evidenceSearchDocumentsSQL(teamID, ownerProfileID, [
    { id: firstFragmentID, content: firstQuote, hash: firstHash },
    { id: secondFragmentID, content: secondQuote, hash: secondHash },
  ]);
  postgresQuery(`
    INSERT INTO team_predicate_definitions (
      team_id, predicate_key, version, aliases, allowed_subject_kinds,
      allowed_object_kinds, relationship_kind, current_cardinality,
      lifecycle_state, origin, metadata, created_at
    )
    SELECT ${sqlLiteral(teamID)}::uuid, predicate_key, version, aliases,
           allowed_subject_kinds, allowed_object_kinds, relationship_kind,
           current_cardinality, lifecycle_state, 'built_in',
           metadata || jsonb_build_object('source', 'compose_uat'), created_at
    FROM predicate_definitions
    WHERE (predicate_key, version) = ('uses', 1)
    ON CONFLICT (team_id, predicate_key, version) DO NOTHING;

    INSERT INTO entity_records (team_id, entity_id, entity_kind)
    VALUES
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(subjectID)}::uuid, 'project'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(middleID)}::uuid, 'product'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(objectID)}::uuid, 'product');

    INSERT INTO entity_names (
      team_id, entity_id, owner_profile_id, display_name, normalized_name, name_kind
    ) VALUES
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(subjectID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 'Dense-Mem', 'dense-mem', 'canonical'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(middleID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 'Runtime service', 'runtime service', 'canonical'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(objectID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 'PostgreSQL', 'postgresql', 'canonical');

    INSERT INTO knowledge_ingests (
      team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
      source_summary, status, proposal, metadata, completed_at
    ) VALUES (
      ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(ingestID)}::uuid,
      ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-evidence',
      'sha256:compose-e2e-evidence', 'Evidence-backed relationships for scheduled dreaming',
      'completed', '{}'::jsonb, '{}'::jsonb, now()
    );

    INSERT INTO evidence_fragments (
      team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
      content, content_hash, source_type, authority, source_ref
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(firstFragmentID)}::uuid,
        ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 0,
        ${sqlLiteral(firstQuote)}, ${sqlLiteral(firstHash)}, 'manual', 'primary', 'compose-e2e-first'
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondFragmentID)}::uuid,
        ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 1,
        ${sqlLiteral(secondQuote)}, ${sqlLiteral(secondHash)}, 'manual', 'primary', 'compose-e2e-second'
      );

    INSERT INTO relationship_records (
      team_id, relationship_id, owner_profile_id, semantic_group_key,
      subject_entity_id, predicate_key, predicate_version, object_entity_id,
      relationship_kind, current_cardinality, status, support_count,
      source_group_count
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(firstRelationshipID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-dense-runtime',
        ${sqlLiteral(subjectID)}::uuid, 'uses', 1, ${sqlLiteral(middleID)}::uuid,
        'state', 'many', 'active', 1, 1
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondRelationshipID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-runtime-postgres',
        ${sqlLiteral(middleID)}::uuid, 'uses', 1, ${sqlLiteral(objectID)}::uuid,
        'state', 'many', 'active', 1, 1
      );

    INSERT INTO relationship_observations (
      team_id, observation_id, relationship_id, ingest_id, owner_profile_id,
      subject_ref, original_predicate, object_ref, subject_entity_id,
      predicate_key, predicate_version, object_entity_id, evidence, metadata
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(firstObservationID)}::uuid,
        ${sqlLiteral(firstRelationshipID)}::uuid, ${sqlLiteral(ingestID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'Dense-Mem', 'uses',
        'Runtime service', ${sqlLiteral(subjectID)}::uuid, 'uses', 1,
        ${sqlLiteral(middleID)}::uuid,
        jsonb_build_array(jsonb_build_object('fragment_id', ${sqlLiteral(firstFragmentID)}, 'start', 0, 'end', char_length(${sqlLiteral(firstQuote)}))), '{}'::jsonb
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondObservationID)}::uuid,
        ${sqlLiteral(secondRelationshipID)}::uuid, ${sqlLiteral(ingestID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'Runtime service', 'uses',
        'PostgreSQL', ${sqlLiteral(middleID)}::uuid, 'uses', 1,
        ${sqlLiteral(objectID)}::uuid,
        jsonb_build_array(jsonb_build_object('fragment_id', ${sqlLiteral(secondFragmentID)}, 'start', 0, 'end', char_length(${sqlLiteral(secondQuote)}))), '{}'::jsonb
      );

    INSERT INTO verification_events (
      team_id, verification_event_id, observation_id, owner_profile_id, evidence_verdict,
      confidence, rationale, model, response_hash, metadata
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(firstVerificationID)}::uuid,
        ${sqlLiteral(firstObservationID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
        'entailed', 0.95, 'The exact excerpt supports the first premise.',
        'compose-e2e', 'sha256:compose-e2e-first-verification', '{}'::jsonb
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondVerificationID)}::uuid,
        ${sqlLiteral(secondObservationID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
        'entailed', 0.95, 'The exact excerpt supports the second premise.',
        'compose-e2e', 'sha256:compose-e2e-second-verification', '{}'::jsonb
      );

    INSERT INTO relationship_evidence_supports (
      team_id, support_id, relationship_id, observation_id, verification_event_id,
      fragment_id, owner_profile_id, source_group_key, span_start, span_end,
      quote, authority, metadata
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(firstSupportID)}::uuid,
        ${sqlLiteral(firstRelationshipID)}::uuid, ${sqlLiteral(firstObservationID)}::uuid,
        ${sqlLiteral(firstVerificationID)}::uuid, ${sqlLiteral(firstFragmentID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-runtime', 0,
        char_length(${sqlLiteral(firstQuote)}), ${sqlLiteral(firstQuote)}, 'primary', '{}'::jsonb
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondSupportID)}::uuid,
        ${sqlLiteral(secondRelationshipID)}::uuid, ${sqlLiteral(secondObservationID)}::uuid,
        ${sqlLiteral(secondVerificationID)}::uuid, ${sqlLiteral(secondFragmentID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-postgres', 0,
        char_length(${sqlLiteral(secondQuote)}), ${sqlLiteral(secondQuote)}, 'primary', '{}'::jsonb
      );

    INSERT INTO relationship_support_decision_events (
      team_id, support_id, relationship_id, owner_profile_id, actor_profile_id,
      decision, reason, metadata
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(firstSupportID)}::uuid,
        ${sqlLiteral(firstRelationshipID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'grant', 'compose e2e support', '{}'::jsonb
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondSupportID)}::uuid,
        ${sqlLiteral(secondRelationshipID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'grant', 'compose e2e support', '{}'::jsonb
      );
    ${searchDocuments}
  `);
  const indexed = postgresQuery(`
    SELECT count(*) FROM search_documents
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND source_kind = 'evidence'
      AND source_id IN (${sqlLiteral(firstFragmentID)}::uuid, ${sqlLiteral(secondFragmentID)}::uuid)
  `);
  assertEqual(indexed, "2", "scheduled premise search-document seed");
  return {
    firstRelationshipID,
    secondRelationshipID,
    firstQuote,
    secondQuote,
    subjectID,
    middleID,
    objectID,
  };
}


async function seedEvidenceDiscoveryInputs(ownerProfileID, targetTeamID = teamID, targetContentOverride = "") {
  const targetID = randomUUID();
  const quarantinedID = randomUUID();
  const ingestID = randomUUID();
  const targetSecurityID = randomUUID();
  const quarantinedSecurityID = randomUUID();
  const quarantineID = randomUUID();
  const targetContent = targetContentOverride || "Dense-Mem uses PostgreSQL for durable memory in an hourly evidence target.";
  const quarantinedContent = "This quarantined evidence must never enter evidence discovery.";
  const targetContentHash = sha256Hash(targetContent);
  const quarantinedContentHash = sha256Hash(quarantinedContent);
  const searchDocuments = await evidenceSearchDocumentsSQL(targetTeamID, ownerProfileID, [
    { id: targetID, content: targetContent, hash: targetContentHash },
  ]);
  postgresQuery(`
    INSERT INTO team_predicate_definitions (
      team_id, predicate_key, version, aliases, allowed_subject_kinds,
      allowed_object_kinds, relationship_kind, current_cardinality,
      lifecycle_state, origin, metadata, created_at
    )
    SELECT ${sqlLiteral(targetTeamID)}::uuid, predicate_key, version, aliases,
           allowed_subject_kinds, allowed_object_kinds, relationship_kind,
           current_cardinality, lifecycle_state, 'built_in',
           metadata || jsonb_build_object('source', 'compose_uat'), created_at
    FROM predicate_definitions
    WHERE (predicate_key, version) = ('uses', 1)
    ON CONFLICT (team_id, predicate_key, version) DO NOTHING;

    INSERT INTO knowledge_ingests (
      team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
      source_summary, status, proposal, metadata, completed_at
    ) VALUES (
      ${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
      'hourly-evidence-discovery', 'sha256:hourly-evidence-discovery',
      'Hourly evidence-discovery UAT fixture', 'completed', '{}'::jsonb, '{}'::jsonb, now()
    );
    INSERT INTO evidence_fragments (
      team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
      content, content_hash, source_type, authority, source_ref, metadata
    ) VALUES
      (${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(targetID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 0,
       ${sqlLiteral(targetContent)}, ${sqlLiteral(targetContentHash)}, 'manual', 'primary', 'hourly-evidence-target', '{}'::jsonb),
      (${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(quarantinedID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 1,
       ${sqlLiteral(quarantinedContent)}, ${sqlLiteral(quarantinedContentHash)}, 'manual', 'primary', 'hourly-evidence-quarantined', '{}'::jsonb);
    INSERT INTO evidence_security_events (
      team_id, security_event_id, fragment_id, ingest_id, owner_profile_id,
      event_kind, decision, reason, metadata
    ) VALUES
      (${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(targetSecurityID)}::uuid, ${sqlLiteral(targetID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
       'deterministic_scan', 'pass', 'hourly evidence target passed security', '{}'::jsonb),
      (${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(quarantinedSecurityID)}::uuid, ${sqlLiteral(quarantinedID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
       'deterministic_scan', 'quarantine', 'hourly adverse evidence is quarantined', '{}'::jsonb);
    INSERT INTO evidence_quarantines (
      team_id, quarantine_id, fragment_id, ingest_id, owner_profile_id, status, reason
    ) VALUES (
      ${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(quarantineID)}::uuid, ${sqlLiteral(quarantinedID)}::uuid,
      ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 'active', 'hourly adverse evidence fixture'
    );
    ${searchDocuments}
  `);
  const indexed = postgresQuery(`
    SELECT count(*)
    FROM search_documents
    WHERE team_id = ${sqlLiteral(targetTeamID)}::uuid
      AND source_kind = 'evidence'
      AND source_id = ${sqlLiteral(targetID)}::uuid
  `);
  assertEqual(indexed, "1", `evidence search-document seed for ${targetTeamID}`);
  const quarantinedIndexed = postgresQuery(`
    SELECT count(*) FROM search_documents
    WHERE team_id = ${sqlLiteral(targetTeamID)}::uuid
      AND source_kind = 'evidence'
      AND source_id = ${sqlLiteral(quarantinedID)}::uuid
  `);
  assertEqual(quarantinedIndexed, "0", "quarantined evidence has no search document");
  return { targetID, targetContent, ingestID };
}


function seedHistoricalEvidenceHypothesis(ownerProfileID, evidence, graph) {
  const runID = randomUUID(), failureRunID = randomUUID(), hypothesisID = randomUUID(), subjectID = randomUUID();
  const statement = "Legacy Dense-Mem may use PostgreSQL.";
  const targetIdentity = sha256Hash([teamID, subjectID, "uses", `entity:${graph.objectID}`].join("\u0000"));
  postgresQuery(`
    INSERT INTO entity_records(team_id, entity_id, entity_kind)
    VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(subjectID)}::uuid, 'project');
    INSERT INTO entity_names(team_id, entity_id, owner_profile_id, display_name, normalized_name, name_kind)
    VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(subjectID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 'Legacy Dense-Mem', 'legacy dense-mem', 'canonical');
    INSERT INTO dream_cycle_runs(team_id, run_id, space_id, space_generation, run_date, window_key, lane, status, provider_model, started_at, completed_at, evidence_targets, evaluated_evidence_targets, created_hypotheses, provider_turns, outcome_summary)
    VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(runID)}::uuid, dense_mem_team_shared_space(${sqlLiteral(teamID)}::uuid), dense_mem_team_shared_generation(${sqlLiteral(teamID)}::uuid),
      '2026-09-04', 'hour:historical-fixture', 'evidence_discovery', 'completed', 'legacy-v2.6.3', '2026-09-04T00:00:00Z', '2026-09-04T00:01:00Z', 1, 1, 1, 1, '{"created_hypotheses":1}');
    INSERT INTO dream_cycle_runs(team_id, run_id, space_id, space_generation, run_date, window_key, lane, status, provider_model, started_at, completed_at, evidence_targets, outcome_summary, error)
    VALUES (${sqlLiteral(adverseTeam.teamID)}::uuid, ${sqlLiteral(failureRunID)}::uuid, dense_mem_team_shared_space(${sqlLiteral(adverseTeam.teamID)}::uuid), dense_mem_team_shared_generation(${sqlLiteral(adverseTeam.teamID)}::uuid),
      '2026-09-04', 'hour:historical-failure', 'evidence_discovery', 'failed', 'legacy-v2.6.3', '2026-09-04T00:00:00Z', '2026-09-04T00:01:00Z', 1, '{"provider_failed":1}', 'Historical provider failure.');
    INSERT INTO hypotheses(team_id, hypothesis_id, space_id, space_generation, created_by_profile_id, lane, status,
      statement, subject_entity_id, predicate_key, predicate_version, object_entity_id, source_refs, source_versions,
      source_owner_profile_ids, content_hash, target_identity, cycle_run_id, generator_kind, generator_version)
    VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(hypothesisID)}::uuid, dense_mem_team_shared_space(${sqlLiteral(teamID)}::uuid), dense_mem_team_shared_generation(${sqlLiteral(teamID)}::uuid), ${sqlLiteral(ownerProfileID)}::uuid,
      'evidence_discovery', 'proposed', ${sqlLiteral(statement)}, ${sqlLiteral(subjectID)}::uuid, 'uses', 1, ${sqlLiteral(graph.objectID)}::uuid,
      '[]'::jsonb, '{}'::jsonb, ARRAY[${sqlLiteral(ownerProfileID)}::uuid], ${sqlLiteral(sha256Hash(statement))}, ${sqlLiteral(targetIdentity)}, ${sqlLiteral(runID)}::uuid, 'provider', 'legacy-v2.6.3');
    INSERT INTO hypothesis_evidence_derivation_sources(team_id, hypothesis_id, space_id, space_generation,
      evidence_id, fragment_id, source_group_key, span_start, span_end, quote, authority)
    VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(hypothesisID)}::uuid, dense_mem_team_shared_space(${sqlLiteral(teamID)}::uuid), dense_mem_team_shared_generation(${sqlLiteral(teamID)}::uuid),
      ${sqlLiteral(evidence.targetID)}::uuid, ${sqlLiteral(evidence.targetID)}::uuid, ${sqlLiteral(`ingest:${evidence.ingestID}`)}, 0, ${evidence.targetContent.length}, ${sqlLiteral(evidence.targetContent)}, 'primary');
  `);
  return { runID, failureRunID, hypothesisID, statement };
}


async function evidenceSearchDocumentsSQL(targetTeamID, ownerProfileID, documents) {
  const model = requiredEnv("AI_API_EMBEDDING_MODEL");
  const dimensions = Number(requiredEnv("AI_API_EMBEDDING_DIMENSIONS"));
  const response = await fetch(`${requiredEnv("AI_API_URL").replace(/\/$/, "")}/embeddings`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${requiredEnv("AI_API_KEY")}`,
      "content-type": "application/json",
    },
    body: JSON.stringify({ model, dimensions, input: documents.map((document) => document.content) }),
    signal: AbortSignal.timeout(60_000),
  });
  if (!response.ok) throw new Error(`evidence fixture embedding failed with HTTP ${response.status}`);
  const payload = await response.json();
  if (!Array.isArray(payload.data) || payload.data.length !== documents.length ||
      payload.data.some((item) => !item || !Array.isArray(item.embedding) || item.embedding.length !== dimensions ||
        item.embedding.some((value) => typeof value !== "number" || !Number.isFinite(value)))) {
    throw new Error("evidence fixture embedding response has invalid vectors");
  }
  const vectors = new Array(documents.length);
  for (const item of payload.data) {
    if (!Number.isInteger(item.index) || item.index < 0 || item.index >= documents.length ||
        vectors[item.index] !== undefined) {
      throw new Error("evidence fixture embedding response has invalid indexes");
    }
    vectors[item.index] = item.embedding;
  }
  const values = documents.map((document, index) => `(
    ${sqlLiteral(randomUUID())}::uuid, ${sqlLiteral(document.id)}::uuid,
    ${sqlLiteral(document.content)}, ${sqlLiteral(document.hash)},
    ${sqlLiteral(JSON.stringify(vectors[index]))}::vector
  )`).join(",");
  return `
    INSERT INTO search_documents (
      team_id, search_document_id, owner_profile_id, source_kind, source_id, source_version,
      document_version, embedding_contract_id, embedding_dimensions, search_state,
      document_text, document_hash, projection_format_version, metadata, embedding
    )
    SELECT ${sqlLiteral(targetTeamID)}::uuid, fixture.search_document_id, ${sqlLiteral(ownerProfileID)}::uuid,
           'evidence', fixture.source_id, 1, 1, contract.embedding_contract_id,
           contract.dimensions, 'current', fixture.document_text, fixture.document_hash,
           1, '{}'::jsonb, fixture.embedding
    FROM (VALUES ${values}) AS fixture(search_document_id, source_id, document_text, document_hash, embedding)
    CROSS JOIN (
      SELECT embedding_contract.embedding_contract_id, embedding_contract.dimensions
      FROM search_index_generations AS generation
      JOIN embedding_contracts AS embedding_contract
        ON embedding_contract.embedding_contract_id = generation.embedding_contract_id
       AND embedding_contract.dimensions = generation.embedding_dimensions
      WHERE generation.activation_state = 'active' AND embedding_contract.lifecycle_state = 'active'
      ORDER BY embedding_contract.version DESC, generation.generation DESC, generation.created_at DESC
      LIMIT 1
    ) AS contract;
  `;
}


async function createAdverseEvidenceTeam() {
  const teamName = `Historical discovery failure ${Date.now()}`;
  const team = await controlJSON("/teams", {
    method: "POST",
    body: JSON.stringify({ name: teamName, description: "historical discovery provider failure fixture" }),
  });
  const targetTeamID = team.data?.id;
  if (typeof targetTeamID !== "string" || !targetTeamID) {
    throw new Error("adverse evidence team creation did not return an id");
  }
  const credential = await controlJSON(`/teams/${targetTeamID}/credentials`, {
    method: "POST",
    body: JSON.stringify({ name: "Historical discovery failure owner", role: "member", scopes: ["read", "write"], rate_limit: 300 }),
  });
  const ownerProfileID = credential.data?.credential?.id;
  if (typeof ownerProfileID !== "string" || !ownerProfileID) {
    throw new Error("adverse evidence credential did not return an owner profile id");
  }
  const apiKey = credential.data?.api_key;
  if (typeof apiKey !== "string" || !apiKey) {
    throw new Error("adverse evidence credential did not return an API key");
  }
  return { teamID: targetTeamID, teamName, ownerProfileID, apiKey };
}


async function controlJSON(path, options = {}, retryTransport = (options.method ?? "GET") === "GET") {
  return httpJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${controlToken}`,
      "Content-Type": "application/json",
      ...(options.headers ?? {}),
    },
  }, retryTransport);
}


async function controlResponse(path, options = {}) {
  const response = await fetchWithTransportRetry(`${controlURL}/control/api${path}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${controlToken}`,
      "Content-Type": "application/json",
      ...(options.headers ?? {}),
    },
  });
  const text = await response.text();
  return {
    status: response.status,
    body: text ? JSON.parse(text) : {},
    cacheControl: response.headers.get("cache-control"),
    contentTypeOptions: response.headers.get("x-content-type-options"),
  };
}


async function userJSON(path) {
  return httpJSON(`${userURL}${path}`, {
    headers: {
      Authorization: `Bearer ${apiKey}`,
    },
  }, true);
}


async function mcpTool(token, name, args, retryTransport = false, expectError = false) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: ++rpcID,
      method: "tools/call",
      params: { name, arguments: args },
    }),
  }, retryTransport);
  if (response.error) {
    if (expectError) return response.error.data ?? response.error;
    throw new Error(`MCP ${name} error: ${JSON.stringify(response.error)}`);
  }
  if (expectError && response.result?.isError !== true) throw new Error(`MCP ${name} unexpectedly succeeded`);
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") {
    throw new Error(`MCP ${name} result missing text: ${JSON.stringify(response)}`);
  }
  return JSON.parse(text);
}


async function httpJSON(url, options, retryTransport = false) {
  const response = await fetchWithTransportRetry(url, options, retryTransport);
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`HTTP ${response.status} ${url}: ${redactHTTPBody(text)}`);
  }
  return text ? JSON.parse(text) : {};
}


async function fetchWithTransportRetry(url, options, retryTransport = true) {
  let response;
  const attempts = retryTransport ? 3 : 1;
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      response = await fetch(url, options);
      break;
    } catch (error) {
      if (attempt === attempts - 1) {
        throw error;
      }
      await delay(1_000);
    }
  }
  return response;
}


function postgresQuery(sql) {
  const scopedSQL = [
    "BEGIN",
    "SET LOCAL app.tx_mode = 'system'",
    "SET LOCAL app.current_team_id = ''",
    "SET LOCAL app.current_profile_id = ''",
    "SET LOCAL app.allowed_space_ids = ''",
    sql,
    "COMMIT",
  ].join(";\n");
  const result = spawnSync("docker", [
    "compose",
    "-p",
    composeProject,
    "-f",
    composeFile,
    "exec",
    "-T",
    "postgres",
    "sh",
    "-ec",
    'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "team-dreaming-e2e",
    scopedSQL,
  ], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`postgres query failed (${result.status}): ${result.error?.message || result.stderr || result.stdout || result.signal || "no process output"}`);
  }
  return result.stdout.trim();
}


function assertEqual(actual, expected, label) {
  if (actual !== expected) {
    throw new Error(`${label} = ${JSON.stringify(actual)}; want ${JSON.stringify(expected)}`);
  }
}


function sqlLiteral(value) {
  return `'${String(value).replace(/'/g, "''")}'`;
}


function sha256Hash(value) {
  return createHash("sha256").update(value).digest("hex");
}


function redactHTTPBody(text) {
  return text.replace(/"api_key"\s*:\s*"[^"]*"/g, "\"api_key\":\"<redacted>\"");
}


function requiredEnv(name) {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}


function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
