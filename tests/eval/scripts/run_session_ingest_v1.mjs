import assert from "node:assert/strict";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import { createHash, randomUUID } from "node:crypto";
import { execFileSync } from "node:child_process";
import { performance } from "node:perf_hooks";

const [mode, cohortPath, outputPath] = process.argv.slice(2);
assert(["quality", "remember"].includes(mode) && cohortPath && outputPath, "usage: node run_session_ingest_v1.mjs quality|remember COHORT OUTPUT");
const raw = await readFile(cohortPath);
const cohort = JSON.parse(raw);
assert.equal(cohort.quality_cases.length, 32);
assert.equal(cohort.remember_controls.length, 12);
const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = required("DENSE_MEM_CONTROL_TOKEN");
const container = required("DENSE_MEM_EVAL_POSTGRES_CONTAINER");
const proxyURL = required("DENSE_MEM_EVAL_PROXY_URL").replace(/\/$/, "");
const sourceSHA = required("DENSE_MEM_EVAL_SOURCE_SHA");
assert(/^[0-9a-f]{40}$/.test(sourceSHA));
const tokenAccounting = { input_tokens: "provider-reported chat prompt tokens", output_tokens: "provider-reported chat completion tokens", embedding_input_tokens: "provider-reported embedding tokens when available", embedding_usage_unavailable: "embedding calls whose provider omitted token usage; no token estimate is substituted" };
const report = { cohort: cohort.cohort, cohort_sha256: createHash("sha256").update(raw).digest("hex"), source_sha: sourceSHA, token_accounting: tokenAccounting, mode, repetitions: [], passed: false };
try {
 for (let repetition = 1; repetition <= (mode === "quality" ? 3 : 1); repetition++) {
  const samples = [];
  let truePositive = 0, predicted = 0, expected = 0;
  for (const test of mode === "quality" ? cohort.quality_cases : cohort.remember_controls) {
   const team = (await control("/teams", { name: `session-eval-${repetition}-${test.id}-${randomUUID()}` })).data;
   const actor = await credential(team.id, "owner-A", mode === "quality" ? "credential_private" : "shared_only");
   const metricsBefore = await metrics();
   const started = performance.now();
   const ids = new Set();
   const events = new Map();
   const sessionID = randomUUID();
   const calls = mode === "quality" ? test.calls : [null];
   const durations = [];
   let controlOutcome;
   for (const batch of calls) {
    const start = performance.now();
    let args;
    if (mode === "quality") {
     for (const event of batch) events.set(event.event_id, event);
     args = { idempotency_key: randomUUID(), framework: "eval", app_name: "session_ingest_v1", user_id: "external-user", session_id: sessionID, events: batch };
    } else args = { idempotency_key: randomUUID(), ...test.arguments };
    if (mode === "remember" && test.operation === "invalid") {
     const response = await rpc("remember", args, actor.api_key);
     assert.equal(response.error?.code, test.error.rpc_code);
     assert.equal(response.error?.data?.code, test.error.code);
     assert.equal(response.error?.data?.reason_code, test.error.reason_code);
     controlOutcome = test.error;
     durations.push((performance.now() - start) / 1000);
     continue;
    }
    let result;
    if (mode === "remember" && test.operation === "concurrent") {
     const pair = await Promise.all([call("remember",args,actor.api_key),call("remember",args,actor.api_key)]);
     assert.deepEqual(pair[0],pair[1], "concurrent requests must reuse one receipt"); result = pair[0];
    } else result = await call(mode === "quality" ? "ingest_session" : "remember", args, actor.api_key);
    if (mode === "remember" && test.operation === "conflict") {
     const changed = structuredClone(args);
     changed.evidence[0].content = "Dense-Mem stores durable memory in PostgreSQL. [fixture:conflict-b]";
     const response = await rpc("remember",changed,actor.api_key);
     assert.equal(response.result?.isError,true);
     const failure = response.result.structuredContent || JSON.parse(response.result.content[0].text);
     assert.equal(failure.errors[0].code,"idempotency_conflict");
     assert(failure.evidence.every((item) => item.disposition === "not_stored" && !item.evidence_id));
     assert.deepEqual(await call("remember",args,actor.api_key),result);
    }
    const duration = (performance.now() - start) / 1000;
    durations.push(duration);
    assert(duration <= 180, `${test.id} exceeded the processing deadline`);
    assert.equal(result.processing_state, "completed", `${test.id} failed processing`);
    if (mode === "remember") {
     assert.equal(result.search_state, "current", `${test.id} stored control lost current search readiness`);
     controlOutcome = { processing_state: result.processing_state, search_state: result.search_state };
    }
    for (const disposition of result.relationship_results) for (const split of disposition.splits || []) if (split.relationship_id) ids.add(split.relationship_id);
    if (!test.expected.length) { assert.equal(result.search_state, "not_required"); assert.equal(ids.size, 0); }
   }
   const metricsAfter = await metrics();
   const usage = Object.fromEntries(Object.keys(metricsAfter).map((key) => [key, metricsAfter[key] - metricsBefore[key]]));
   assert.equal(usage.missing_chat_usage, 0, "provider chat token usage is required");
   const rows = knowledge([...ids]);
   const facts = rows.map((row) => ({ subject: row.subject, predicate: row.predicate, object: row.object, polarity: row.polarity, ...(row.valid_from ? { valid_from: new Date(row.valid_from).toISOString() } : {}), ...(row.valid_to ? { valid_to: new Date(row.valid_to).toISOString() } : {}) }));
   const unmatched = [...facts];
   let matched = 0;
   for (const fact of test.expected) {
    const index = unmatched.findIndex((actual) => sameFact(actual, fact));
    if (index >= 0) { matched++; unmatched.splice(index, 1); }
   }
   if (test.designated_boundary) assert.equal(matched, test.expected.length, `${test.id} lost a designated boundary fact`);
   if (test.distinct_subject_ids) assert.equal(new Set(rows.map((row) => row.subject_id)).size, 2, "homonyms must retain separate identities");
   for (const id of ids) {
    const trace = await call("trace_memory", { relationship_id: id }, actor.api_key);
    if (mode === "quality") {
     assert(trace.evidence.length > 0);
     for (const evidence of trace.evidence) {
      const provenance = evidence.session;
      assert(provenance, "trace dropped exact event provenance");
      const event = events.get(provenance.event_id);
      assert(event, "trace cited an unsubmitted event");
      assert.equal(provenance.framework, "eval"); assert.equal(provenance.app_name, "session_ingest_v1");
      assert.equal(provenance.user_id, "external-user"); assert.equal(provenance.session_id, sessionID);
      assert.equal(provenance.occurred_at, event.occurred_at);
      const excerpt = Array.from(event.text).slice(provenance.span_start, provenance.span_end).join("");
      assert.equal(evidence.content, Array.from(excerpt).slice(0, 999).join(""));
      assert.equal(evidence.content_hash, `sha256:${createHash("sha256").update(excerpt).digest("hex")}`);
     }
    }
   }
   if (mode === "quality") {
    const b = await credential(team.id, "owner-B");
    const foreign = (await control("/teams", { name: `session-eval-C-${randomUUID()}` })).data;
    const c = await credential(foreign.id, "owner-C");
    for (const isolated of [b, c]) {
     for (const id of ids) {
      const response = await rpc("trace_memory", { relationship_id: id }, isolated.api_key);
      assert(response.error || response.result?.isError, "private trace crossed an actor boundary");
     }
     const recall = await call("recall_memory", { query: test.expected.map((fact) => `${fact.subject} ${fact.object}`).join(" ") || "session facts", limit: 20, relationship_limit: 20, community_limit: 0 }, isolated.api_key);
     assert.equal(recall.results.length, 0, "private evidence leaked"); assert.equal(recall.related_relationships.length, 0, "private knowledge leaked");
    }
   }
   truePositive += matched; predicted += facts.length; expected += test.expected.length;
   const sample = { id: test.id, duration_seconds: (performance.now() - started) / 1000, processing_seconds: durations, expected: test.expected.length, predicted: facts.length, matched, usage, facts, ...(mode === "remember" ? { source: test.source, operation: test.operation, outcome: controlOutcome } : {}) };
   samples.push(sample);
   console.log(JSON.stringify({ repetition, id: test.id, matched, expected: test.expected.length, predicted: facts.length }));
   if (mode === "remember") { assert.equal(matched, test.expected.length); assert.equal(facts.length, test.expected.length); }
  }
  const precision = predicted ? truePositive / predicted : 1;
  const recall = expected ? truePositive / expected : 1;
  const latencies = samples.flatMap((sample) => sample.processing_seconds).sort((a,b) => a-b);
  const percentile = (p) => latencies[Math.ceil(p * latencies.length)-1];
  report.repetitions.push({ repetition, precision, recall, latency_seconds: { p50: percentile(.5), p95: percentile(.95), p99: percentile(.99) }, usage: samples.reduce((sum,sample) => { for (const [key,value] of Object.entries(sample.usage)) sum[key] = (sum[key] || 0) + value; return sum; }, {}), samples });
  await save();
  assert(precision >= .98 && recall >= .95, "frozen fact precision/recall gate failed");
 }
 report.passed = true;
 await save();
} catch (error) { report.failure = error.message; await save(); throw error; }

function required(name) { assert(process.env[name], `${name} is required`); return process.env[name]; }
async function save() { await mkdir(dirname(outputPath), { recursive: true }); await writeFile(outputPath, JSON.stringify(report, null, 2) + "\n"); }
async function control(path, body) { const response = await fetch(`${controlURL}/control/api${path}`, { method: "POST", headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" }, body: JSON.stringify(body) }); assert(response.ok, `control ${path}: ${response.status}`); return response.json(); }
async function credential(team, name, binding = "credential_private") { return (await control(`/teams/${team}/credentials`, { name, scopes: ["read","write"], rate_limit: 10000, memory_binding: binding })).data; }
async function rpc(name, args, key) { const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json", Accept: "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name, arguments: args } }), signal: AbortSignal.timeout(185000) }); assert.equal(response.status, 200); return response.json(); }
async function call(name, args, key) { const response = await rpc(name,args,key); assert(!response.error && !response.result?.isError, `${name}: ${JSON.stringify(response.error || response.result).slice(0,2000)}`); return response.result.structuredContent || JSON.parse(response.result.content[0].text); }
async function metrics() { const response = await fetch(`${proxyURL}/metrics`); assert(response.ok); return response.json(); }
function sameFact(actual, expected) { return ["subject","predicate","object","polarity"].every((key) => actual[key] === expected[key]) && ["valid_from","valid_to"].every((key) => !expected[key] || new Date(actual[key]).getTime() === new Date(expected[key]).getTime()); }
function knowledge(ids) {
 if (!ids.length) return [];
 for (const id of ids) assert(/^[0-9a-f-]{36}$/.test(id));
 const sql = `BEGIN; SELECT set_config('app.tx_mode','system',true); SELECT coalesce(jsonb_agg(row),'[]'::jsonb) FROM (SELECT r.relationship_id::text, r.subject_entity_id::text AS subject_id, s.display_name AS subject, r.predicate_key AS predicate, coalesce(o.display_name,v.canonical_value) AS object, r.polarity, r.valid_from, r.valid_to FROM relationship_records r JOIN LATERAL (SELECT display_name FROM entity_names WHERE team_id=r.team_id AND entity_id=r.subject_entity_id AND name_kind='canonical' AND valid_to IS NULL ORDER BY created_at DESC LIMIT 1) s ON true LEFT JOIN LATERAL (SELECT display_name FROM entity_names WHERE team_id=r.team_id AND entity_id=r.object_entity_id AND name_kind='canonical' AND valid_to IS NULL ORDER BY created_at DESC LIMIT 1) o ON true LEFT JOIN value_records v ON v.team_id=r.team_id AND v.value_id=r.object_value_id WHERE r.relationship_id IN (${ids.map((id) => `'${id}'::uuid`).join(",")})) row; COMMIT;`;
 const output = execFileSync("docker", ["exec", "-i", container, "psql", "-X", "-A", "-t", "-U", process.env.POSTGRES_USER || "densemem", "-d", process.env.POSTGRES_DB || "densemem", "-v", "ON_ERROR_STOP=1"], { input: sql, encoding: "utf8" });
 const json = output.split("\n").find((line) => line.startsWith("[")); assert(json, "missing canonical knowledge projection"); return JSON.parse(json);
}
