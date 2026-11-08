import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { assessmentRecords, captureSemanticAssessmentWindow, collectSemanticAssessmentProof } from "./ontology_assessment_diagnostics.mjs";

export async function automaticRefinementAcceptance({ control, mcp, postgresQuery, fixtureMode, canonicalFingerprints, directory }) {
  const since = new Date().toISOString();
  const assessments = [];
  const cases = [];
  const literal = (text) => `'${String(text).replaceAll("'", "''")}'`;
  const poll = async (read, label, attempts = 150) => {
    for (let i = 0; i < attempts; i++) { const value = await read(); if (value) return value; await new Promise((resolve) => setTimeout(resolve, 1000)); }
    assert.fail(`refinement acceptance timed out: ${label}`);
  };
  const pause = async () => {
    await control("/ontology/pause", { operation_key: randomUUID() });
    await poll(() => postgresQuery("SELECT count(*) FROM ontology_maintenance_batches WHERE status='running'") === "0", "drain");
  };
  try {
    await pause();
    await fixtureMode("normal");
    for (const mode of ["refine", "normal", "ambiguous", "refine-repair", "refine-malformed", "pin-race", "freshness-race"]) {
      const team = (await control("/teams", { name: `refinement-${mode}-${randomUUID()}` })).data;
      const owner = (await control(`/teams/${team.id}/credentials`, { name: "refinement manager", role: "manager", scopes: ["read", "write"], rate_limit: 300 })).data;
      const ui = async (path, body) => {
        const response = await fetch(`${process.env.DENSE_MEM_USER_URL}/ui/api/ontology${path}`, { method: body ? "POST" : "GET",
          headers: { Authorization: `Bearer ${owner.api_key}`, "Content-Type": "application/json" }, body: body ? JSON.stringify(body) : undefined });
        assert(response.ok, `refinement UI ${path}: ${response.status} ${(await response.clone().text()).slice(0, 500)}`);
        return (await response.json()).data;
      };
      const publish = async (edits) => {
        const draft = await ui("/drafts", { operation_key: randomUUID(), expected_version: 0, expected_revision: (await ui("/status")).revision, reason: "Refinement acceptance", edits });
        return ui(`/drafts/${draft.id}/publish`, { operation_key: randomUUID(), expected_version: draft.version, expected_revision: draft.base_revision, reason: draft.reason });
      };
      const sourceCanary = `RefinementV2 software project ${randomUUID()}`;
      const remembered = await mcp("remember", { target_space: "team_shared", idempotency_key: randomUUID(),
        evidence: [{ content: `${sourceCanary} uses PostgreSQL.`, source_type: "document", source_group: randomUUID() }],
        relationships: [{ ref: "use", subject: { name: sourceCanary, entity_kind: "project" }, predicate: { proposed_key: "uses" }, object: { entity: { name: "PostgreSQL", entity_kind: "product" } }, polarity: "+", evidence_indices: [0] }] }, owner.api_key);
      assert.equal(remembered.processing_state, "completed");
      const sourceID = postgresQuery(`SELECT entity_id::text FROM entity_records WHERE team_id=${literal(team.id)}::uuid AND entity_kind='project'`);
      assert.match(sourceID, /^[0-9a-f-]{36}$/);
      const queue = () => JSON.parse(postgresQuery(`SELECT jsonb_build_object('status',status,'assessment',assessment_id,'revision',revision) FROM ontology_maintenance_sources WHERE team_id=${literal(team.id)}::uuid AND source_kind='entity' AND source_id=${literal(sourceID)}`) || "null");
      cases.push({ mode, team, owner, ui, publish, sourceCanary, sourceID, queue });
    }
    await control("/ontology/resume", { operation_key: randomUUID() });
    await poll(() => cases.every(({ queue }) => queue()?.status === "organized") &&
      postgresQuery(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id IN (${cases.map(({ team }) => `${literal(team.id)}::uuid`).join(",")}) AND eligible AND status<>'organized'`) === "0",
    "initial automatic classifications", 300);
    await pause();
    for (const { mode, team, owner, ui, publish, sourceCanary, sourceID, queue } of cases) {
      console.log(JSON.stringify({ scenario: "ontology_refinement", mode, phase: "started", team_id: team.id, source_id: sourceID }));
      const initialAssessment = queue().assessment;
      const definitions = (await ui("/records?kind=entity_class&limit=200")).records;
      const parent = definitions.find((record) => record.definition.key === "project");
      assert(parent);
      await publish([{ action: "define", expected_version: 0, kind: "entity_class", definition: {
        key: "refinement-v2-project", label: "Software project", description: "Explicit software project supplied context", aliases: [], base_entity_kind: "project" } }]);
      const target = (await ui("/records?kind=entity_class&limit=200")).records.find((record) => record.definition.key === "refinement-v2-project");
      assert(target);
      const pin = (await ui("/records?kind=override&limit=200")).records.find((record) => !record.retired && record.override?.action === "pin_definition" && record.override.target_id === target.id);
      assert(pin);
      await publish([{ action: "remove_override", record_id: pin.id, expected_version: pin.version }]);
      const beforeRefresh = queue();
      const refreshed = JSON.parse(postgresQuery(`UPDATE ontology_maintenance_sources SET status='pending',reason='organization_policy_v2',revision=revision+1,pending_at=clock_timestamp(),updated_at=clock_timestamp()
        WHERE team_id=${literal(team.id)}::uuid AND source_kind='entity' AND source_id=${literal(sourceID)} AND eligible AND status='organized'
        RETURNING jsonb_build_object('status',status,'assessment',assessment_id,'revision',revision)`));
      assert.equal(refreshed.status, "pending");
      assert.equal(refreshed.assessment, initialAssessment);
      assert.equal(refreshed.revision, beforeRefresh.revision + 1);
      const racing = mode.endsWith("-race");
      await fixtureMode(racing ? "refine-hold" : mode);
      let canonical = canonicalFingerprints(team.id);
      await control("/ontology/resume", { operation_key: randomUUID() });
      if (racing) {
        await poll(async () => {
          const response = await fetch(`${process.env.DENSE_MEM_E2E_PROVIDER_URL}/health`);
          assert(response.ok); return (await response.json()).active_ontology_requests > 0;
        }, "held HTTP assessment");
        await control("/ontology/pause", { operation_key: randomUUID() });
        if (mode === "pin-race") await publish([{ action: "pin_definition", expected_version: 0, definition_id: target.id }]);
        else postgresQuery(`UPDATE entity_names SET display_name=display_name||' fresh alias' WHERE team_id=${literal(team.id)}::uuid AND entity_id=${literal(sourceID)}::uuid`);
        canonical = canonicalFingerprints(team.id);
        await fixtureMode("refine");
      }
      const receipt = await poll(() => {
        const rows = JSON.parse(postgresQuery(`SELECT COALESCE(jsonb_agg(jsonb_build_object('assessment',assessment.assessment_id,'result',assessment.body->'result','batch',batch.batch_id,'run',batch.run_id) ORDER BY assessment.created_at,assessment.operation_key),'[]')
          FROM ontology_assessments AS assessment JOIN ontology_maintenance_batches AS batch ON assessment.operation_key='maintenance:'||batch.batch_id::text
          WHERE assessment.team_id=${literal(team.id)}::uuid AND assessment.assessment_id<>${literal(initialAssessment)}::uuid
          AND assessment.body->'result'->'outcomes' @> ${literal(JSON.stringify([{ source: { kind: "entity", id: sourceID } }]))}::jsonb`));
        return rows[0];
      }, "durable refinement receipt");
      await pause();
      assert.deepEqual(canonicalFingerprints(team.id), canonical, "ontology refinement changed canonical facts");
      const current = await ui(`/records/${target.id}`);
      const assignment = (await ui("/records?kind=assignment&limit=200")).records.find((record) => record.assignment?.source.id === sourceID);
      assert(assignment);
      const failure = racing ? "stale_input" : mode === "refine-malformed" ? "provider_response_invalid" : "";
      assert.equal(receipt.result.failure_code ?? "", failure);
      if (failure) {
        assert(!receipt.result.publication);
        assert.equal(current.version, target.version);
        assert.equal(assignment.assignment.definition_id, parent.id);
      } else if (mode === "refine" || mode === "refine-repair") {
        assert.equal(current.definition.parent_relation, "subclass_of");
        assert.equal(current.definition.parent_id, parent.id);
        assert.equal(assignment.assignment.definition_id, current.id);
        assert.equal(assignment.current, true);
      } else {
        assert(!current.definition.parent_relation);
        assert.equal(assignment.assignment.definition_id, parent.id);
        if (mode === "ambiguous") assert(receipt.result.outcomes.some((outcome) => outcome.source.id === sourceID && outcome.status === "ambiguous" && outcome.record_ids.includes(assignment.id)));
      }
      assessments.push({ assessmentID: receipt.assessment, teamID: team.id, batchID: receipt.batch, runID: receipt.run, sourceID, sourceCanary,
        failure, regeneration: ["refine-repair", "refine-malformed"].includes(mode), credentials: [owner.api_key, process.env.DENSE_MEM_CONTROL_TOKEN, "dense-mem-e2e-verifier-key", "dense-mem-ontology-e2e-key"] });
      console.log(JSON.stringify({ scenario: "ontology_refinement", mode, phase: "completed", team_id: team.id, assessment_id: receipt.assessment, batch_id: receipt.batch }));
    }
    collectSemanticAssessmentProof({ project: process.env.DENSE_MEM_E2E_COMPOSE_PROJECT, composeFile: process.env.DENSE_MEM_E2E_COMPOSE_FILE, since, directory, assessments });
    console.log(JSON.stringify({ scenario: "ontology_organization", automatic_refinement: true, preference_reuse: true, ambiguity_preserved: true, complete_regeneration: true, atomic_rejection: true, pin_and_freshness_races: true, log_only_assessments: assessments.length }));
  } catch (error) {
    try {
      const window = captureSemanticAssessmentWindow({ project: process.env.DENSE_MEM_E2E_COMPOSE_PROJECT, composeFile: process.env.DENSE_MEM_E2E_COMPOSE_FILE, since, directory });
      for (const record of assessmentRecords(window).filter((record) => cases.some(({ team }) => record.team_id === team.id))) {
        console.log(`ontology_semantic_diagnostic_record ${JSON.stringify(record)}`);
      }
    } catch (captureError) { throw new AggregateError([error, captureError], "ontology refinement failure capture is incomplete"); }
    throw error;
  } finally { await fixtureMode("normal"); await pause(); }
}
