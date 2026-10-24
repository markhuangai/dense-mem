#!/usr/bin/env node
import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import { contextualIntakeCases, contextualIntakeAdaptations } from "./contextual_intake_v1_cases.mjs";

export function cohort() {
  assert.equal(contextualIntakeCases.length, 20);
  assert.equal(contextualIntakeCases.filter((test) => test.kind === "security").length, 12);
  return { version: "contextual_intake_v1", issues: [592, 598, 606, 609], fixture_adaptations: contextualIntakeAdaptations, cases: structuredClone(contextualIntakeCases) };
}

export function score(test, result, stored, conflicts) {
  const accepted = result?.processing_state === "completed" && result.evidence?.length === test.arguments.evidence.length && result.evidence.every((item) => item.disposition === "stored");
  const rejected = result?.processing_state === "failed" && result.errors?.some((error) => error.code === "submission_policy_rejected") && result.evidence?.every((item) => item.disposition === "not_stored");
  const exact = accepted ? stored.length === test.arguments.evidence.length && test.arguments.evidence.every((item, index) => stored[index] === item.content) : stored.length === 0;
  const opposing = !test.opposing_assertions || conflicts.length === 1 && opposingCitations(test, result, conflicts[0]);
  return { accepted, rejected, exact, opposing_citations: opposing, conflict_count: conflicts.length, passed: (test.accepted ? accepted : rejected) && exact && opposing && conflicts.length === test.conflicts };
}

function opposingCitations(test, result, conflict) {
  const positions = conflict.positions || [];
  const used = new Set();
  return test.opposing_assertions.every((expected) => {
    const evidence = result.evidence.find((item) => item.evidence_index === expected.evidence_index);
    const source = test.arguments.evidence[expected.evidence_index].content;
    const index = positions.findIndex((position, index) => !used.has(index) && position.evidence_id === evidence?.evidence_id &&
      position.quote === Array.from(source).slice(position.span_start, position.span_end).join("") &&
      position.quote.includes(expected.contains) && !position.quote.includes(expected.excludes));
    if (index < 0) return false;
    used.add(index);
    return true;
  });
}

export function compare(base, candidate) {
  assert.equal(base.cohort_sha256, candidate.cohort_sha256);
  assert.equal(base.model, "dense-mem-testing");
  assert.equal(candidate.model, base.model);
  for (const report of [base, candidate]) {
    assert.equal(report.complete, true);
    assert.equal(report.repetitions.length, 3);
    assert(report.repetitions.every((rep) => rep.samples.length === 20));
    for (const repetition of report.repetitions) assert.deepEqual(repetition.samples.map((sample) => sample.id).sort(), contextualIntakeCases.map((test) => test.id).sort());
    assert(/^[0-9a-f]{40}$/.test(report.source_sha));
    assert(/^[0-9a-f]{64}$/.test(report.source_state_sha256));
  }
  assert(candidate.repetitions.every((rep) => rep.samples.every((sample) => sample.passed)), "every candidate judgment must pass in every repetition");
  return { cohort: "contextual_intake_v1", cohort_sha256: base.cohort_sha256, model: candidate.model, base_source_sha: base.source_sha, candidate_source_sha: candidate.source_sha, candidate_source_state_sha256: candidate.source_state_sha256, repetitions: 3, cases_per_repetition: 20,
    base_passed: base.repetitions.map((rep) => rep.samples.filter((sample) => sample.passed).length), candidate_passed: [20, 20, 20], passed: true };
}

async function run(cohortPath, output, role) {
  assert(["base", "candidate"].includes(role));
  const raw = await readFile(cohortPath);
  const frozen = JSON.parse(raw);
  assert.deepEqual(frozen, cohort(), "cohort differs from the frozen source catalog");
  const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
  const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
  const controlToken = required("DENSE_MEM_CONTROL_TOKEN");
  const container = required("DENSE_MEM_EVAL_POSTGRES_CONTAINER");
  const proxyURL = required("DENSE_MEM_EVAL_PROXY_URL").replace(/\/$/, "");
  const model = required("AI_VERIFIER_MODEL");
  assert.equal(model, "dense-mem-testing");
  const report = { cohort: frozen.version, cohort_sha256: hash(raw), role, model, source_sha: required("DENSE_MEM_EVAL_SOURCE_SHA"), source_state_sha256: required("DENSE_MEM_EVAL_SOURCE_STATE_SHA256"), repetitions: [], complete: false, passed: false };
  const save = () => jsonFile(output, report);
  let rpcID = 0;
  async function control(path, body) {
    const response = await fetch(`${controlURL}/control/api${path}`, { method: body ? "POST" : "GET", headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" }, ...(body ? { body: JSON.stringify(body) } : {}) });
    assert(response.ok, `control request failed: HTTP ${response.status}`);
    return (await response.json()).data;
  }
  async function call(name, args, key) {
    const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json", Accept: "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name, arguments: args } }), signal: AbortSignal.timeout(185000) });
    assert.equal(response.status, 200);
    const rpc = await response.json();
    assert(!rpc.error, `unexpected RPC error: ${rpc.error?.code}`);
    const result = rpc.result?.structuredContent || JSON.parse(rpc.result?.content?.[0]?.text);
    assert(result && typeof result === "object");
    return result;
  }
  function query(sql) {
    assert(/^SELECT\b/.test(sql));
    const body = `BEGIN READ ONLY; SELECT set_config('app.tx_mode','system',true); ${sql}; COMMIT;`;
    const text = execFileSync("docker", ["exec", "-i", container, "psql", "-X", "-A", "-t", "-U", process.env.POSTGRES_USER || "densemem", "-d", process.env.POSTGRES_DB || "densemem", "-v", "ON_ERROR_STOP=1"], { input: body, encoding: "utf8" });
    const line = text.split("\n").find((value) => value.startsWith("["));
    assert(line, "missing PostgreSQL JSON result");
    return JSON.parse(line);
  }
  async function usage() { const response = await fetch(`${proxyURL}/metrics`); assert(response.ok); return response.json(); }
  const credential = (team, name) => control(`/teams/${uuid(team)}/credentials`, { name, scopes: ["read", "write"], rate_limit: 10000 });
  try {
    for (let repetition = 1; repetition <= 3; repetition++) {
      const samples = [];
      report.repetitions.push({ repetition, samples });
      for (const test of frozen.cases) {
        const team = await control("/teams", { name: `context-${role}-${repetition}-${test.id}-${randomUUID()}` });
        const actor = await credential(team.id, "owner-A");
        const args = { ...structuredClone(test.arguments), idempotency_key: randomUUID() };
        const before = await usage();
        const started = performance.now();
        const result = await call("remember", args, actor.api_key);
        assert(!result.errors?.some((error) => error.code !== "submission_policy_rejected"), `unexpected required failure for ${test.id}: ${result.errors?.map((error) => error.code)}`);
        const stored = result.submission_id ? query(`SELECT coalesce(json_agg(content ORDER BY evidence_index),'[]'::json) FROM evidence_fragments WHERE team_id='${uuid(team.id)}'::uuid AND ingest_id='${uuid(result.submission_id)}'::uuid`) : [];
        const cases = query(`SELECT coalesce(json_agg(conflict_id::text),'[]'::json) FROM evidence_conflict_cases WHERE team_id='${uuid(team.id)}'::uuid`);
        let projected = [];
        if (result.processing_state === "completed") {
          const recall = await call("recall_memory", { query: test.kind === "conflict" ? "capacity active pending window Atlas evaluation" : "Atlas PostgreSQL report", limit: 20, relationship_limit: 0, community_limit: 0 }, actor.api_key);
          projected = (recall.conflicts || []).filter((item) => item.kind === "evidence_conflict");
          assert.deepEqual(projected.map((item) => item.conflict_id).sort(), [...cases].sort(), "Recall must expose the durable cases");
        }
        const judgment = score(test, result, stored, projected);
        const sample = { id: test.id, ...judgment, duration_seconds: (performance.now() - started) / 1000, usage: difference(await usage(), before), citations_verified: false, replay_verified: false, isolation_verified: false };
        samples.push(sample);
        await save();
        if (judgment.accepted) {
          const replayUsage = await usage();
          const replay = await call("remember", args, actor.api_key);
          assert.deepEqual(replay, result, "matching retry must retain its terminal result");
          assert.equal((await usage()).chat_turns, replayUsage.chat_turns, "replay dispatched another assessment");
          const changed = structuredClone(args);
          changed.evidence[0].content += " Additional factual report.";
          const conflict = await call("remember", changed, actor.api_key);
          assert(conflict.errors?.some((error) => error.code === "idempotency_conflict"));
          sample.replay_verified = true;
          const sources = new Map(result.evidence.map((item) => [item.evidence_id, args.evidence[item.evidence_index].content]));
          for (const conflictCase of projected) {
            assert(conflictCase.positions.length >= 2);
            for (const position of conflictCase.positions) {
              const source = sources.get(position.evidence_id);
              assert(source, "conflict cites unsubmitted evidence");
              assert.equal(Array.from(source).slice(position.span_start, position.span_end).join(""), position.quote);
            }
          }
          sample.citations_verified = true;
          const foreign = await control("/teams", { name: `context-isolated-${randomUUID()}` });
          const outsider = await credential(foreign.id, "owner-C");
          const outside = await call("recall_memory", { query: "capacity Atlas PostgreSQL report", limit: 20, relationship_limit: 20, community_limit: 0 }, outsider.api_key);
          assert.equal(outside.results.length, 0);
          assert.equal(outside.related_relationships.length, 0);
          assert.equal(outside.conflicts.length, 0);
          sample.isolation_verified = true;
        } else {
          assert.equal(stored.length, 0);
          assert.equal(cases.length, 0);
          if (!test.accepted) assert.equal(sample.usage.chat_turns, 0, "initial security rejection dispatched a provider request");
        }
        await save();
        console.log(JSON.stringify({ role, repetition, id: test.id, passed: sample.passed }));
      }
    }
    report.complete = true;
    report.passed = report.repetitions.every((rep) => rep.samples.every((sample) => sample.passed));
    await save();
    if (role === "candidate") assert(report.passed, "candidate cohort contains incorrect judgments");
  } catch (error) { report.failure = error.message; await save(); throw error; }
  return report;
}

function required(name) { assert(process.env[name], `${name} is required`); return process.env[name]; }
function uuid(value) { assert(/^[0-9a-f-]{36}$/i.test(value)); return value; }
function hash(value) { return createHash("sha256").update(value).digest("hex"); }
function difference(after, before) { return Object.fromEntries(Object.keys(after).map((key) => [key, after[key] - before[key]])); }
async function jsonFile(path, value) { await mkdir(dirname(path), { recursive: true }); await writeFile(path, JSON.stringify(value, null, 2) + "\n"); }

async function main() {
  const [mode, ...args] = process.argv.slice(2);
  const { values } = parseArgs({ args, options: Object.fromEntries(["out", "cohort", "role", "base", "candidate", "lock"].map((name) => [name, { type: "string" }])) });
  assert(values.out, "--out is required");
  if (mode === "generate") {
    const data = cohort();
    await jsonFile(values.out, data);
    if (values.lock) await jsonFile(values.lock, { version: data.version, cases: 20, security_cases: 12, conflict_cases: 8, repetitions: 3, model: "dense-mem-testing", generator_sha256: hash(await readFile(new URL(import.meta.url))), catalog_sha256: hash(await readFile(new URL("./contextual_intake_v1_cases.mjs", import.meta.url))), cohort_sha256: hash(await readFile(values.out)), witness_provenance: { quota_request_sha256: "2ea015c44be211b7e31354aa0370217e0254216eac251ebcc993a3cb31c43ab9", identifier_request_sha256: "f7e987b863ad26f3209622589cb73ba105edff2f7a5c969c0099639439926258", observation_sha256: "53ec2c3eddd7c89c2a8b9e6e9dfecc77c3575ba7335405a8e1fff1014e7556d0", issues: data.issues } });
  } else if (mode === "run") await run(values.cohort, values.out, values.role);
  else if (mode === "compare") await jsonFile(values.out, compare(JSON.parse(await readFile(values.base)), JSON.parse(await readFile(values.candidate))));
  else throw new Error("usage: contextual_intake_v1.mjs generate|run|compare --out PATH [--cohort PATH --role base|candidate | --base PATH --candidate PATH | --lock PATH]");
}

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) await main();
