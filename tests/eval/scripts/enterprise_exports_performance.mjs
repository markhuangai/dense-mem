#!/usr/bin/env node
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";

const fixturePath = new URL("../fixtures/enterprise_exports.json", import.meta.url);
const digest = (value) => createHash("sha256").update(value).digest("hex");
const fixtureBytes = fs.readFileSync(fixturePath);
const fixture = JSON.parse(fixtureBytes);

function normalize(value) {
  if (Array.isArray(value)) return value.map(normalize);
  if (typeof value === "string") return value.replace(/(?:rec_)?[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi, "<id>").replace(/\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z/g, "<time>");
  if (!value || typeof value !== "object") return value;
  return Object.fromEntries(Object.keys(value).sort().filter((key) => !["correlation_id", "recall_id", "recall_event_id"].includes(key)).map((key) => [key, normalize(value[key])]));
}
function runProcess(args) {
  return new Promise((resolve, reject) => {
    const child = spawn("docker", args, { stdio: ["ignore", "pipe", "pipe"] });
    let output = ""; child.stdout.on("data", (chunk) => output += chunk);
    child.on("error", reject); child.on("close", (code) => code === 0 ? resolve(output.trim()) : reject(new Error(`Docker ${args[0]} failed`)));
  });
}
const quantile = (values, q) => [...values].sort((a, b) => a - b)[Math.ceil(q * values.length) - 1];
const median = (values) => quantile(values, .5);

export async function measure() {
  const required = (key) => { assert.ok(process.env[key], `${key} is required`); return process.env[key]; };
  const state = required("DENSE_MEM_E2E_EXPORT_PERF_STATE"); assert.ok(fixture.states.includes(state));
  const variant = required("DENSE_MEM_E2E_EXPORT_PERF_VARIANT"); assert.ok(["base", "candidate"].includes(variant));
  const repetition = Number(required("DENSE_MEM_E2E_EXPORT_PERF_REPETITION")); assert.ok(repetition >= 1 && repetition <= 5);
  const project = required("DENSE_MEM_E2E_COMPOSE_PROJECT"); assert.match(project, /^densemem-ci-[a-z0-9-]+$/);
  const composeArgs = ["compose", "-f", required("DENSE_MEM_E2E_COMPOSE_FILE"), "-f", required("DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE"), "-p", project];
  const serverID = await runProcess([...composeArgs, "ps", "-q", "server"]);
  const collectorID = await runProcess([...composeArgs, "ps", "-q", "enterprise-collector"]);
  const apiKey = required("DENSE_MEM_E2E_API_KEY");
  const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
  let rpcID = 0;
  async function tool(name, args) {
    const id = ++rpcID, started = performance.now();
    const correlation = randomUUID();
    let status;
    try {
      const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json", "X-Correlation-ID": correlation }, body: JSON.stringify({ jsonrpc: "2.0", id, method: "tools/call", params: { name, arguments: args } }), signal: AbortSignal.timeout(15000) });
      status = response.status;
      assert.equal(status, 200); const body = await response.json(); assert.ok(body.result && !body.result.isError && !body.error, "unexpected memory failure"); return body.result.structuredContent;
    } catch (error) {
      const phase = id === 1 ? "seed" : id <= fixture.warmup_requests + 1 ? "warmup" : id <= fixture.warmup_requests + fixture.measured_requests + 1 ? "measured" : "saturation";
      const kind = ["TimeoutError", "AbortError", "SyntaxError", "AssertionError"].includes(error?.name) ? error.name : "transport_failure";
      throw new Error(`performance request failed: tool=${name} phase=${phase} id=${id} correlation=${correlation} elapsed_ms=${Math.round(performance.now() - started)} status=${status ?? "unavailable"} kind=${kind}`);
    }
  }
  const seed = { idempotency_key: "enterprise-export-perf-frozen-seed", evidence: [{ content: fixture.content, source_type: "manual" }], relationships: [{ ref: "durable-store", evidence_indices: [0], subject: { name: fixture.subject, entity_kind: "project" }, predicate: { proposed_key: fixture.predicate }, object: { value: { type: "string", value: fixture.value } }, polarity: "+" }] };
  const remembered = await tool("remember", seed); assert.equal(remembered.processing_state, "completed");
  const relationshipID = remembered.relationship_results[0].splits[0].relationship_id;
  const actions = [(index) => tool("remember", seed), (index) => tool("recall_memory", { query: fixture.query }), async (index) => { const result = await tool("trace_memory", { relationship_id: relationshipID }); assert.equal(result.relationship.relationship_id, relationshipID); return result; }];
  async function workload(count, concurrency, saturation = false) {
    const durations = new Array(count), signatures = new Array(count); let index = 0;
    const started = performance.now();
    await Promise.all(Array.from({ length: concurrency }, async () => {
      while (index < count) { const current = index++; const before = performance.now(); const result = await actions[saturation ? 2 : current % actions.length](current); durations[current] = performance.now() - before; signatures[current] = digest(JSON.stringify(normalize(result))); }
    }));
    const elapsed = performance.now() - started;
    return { count, concurrency, p95_ms: quantile(durations, .95), throughput_rps: count / (elapsed / 1000), signatures };
  }
  let stopSampling = false, peakMemory = 0, sampleError;
  const sampler = (async () => { while (!stopSampling) { try { const status = await runProcess(["exec", serverID, "cat", "/proc/1/status"]); const match = status.match(/^VmRSS:\s+(\d+) kB$/m); assert.ok(match, "server RSS is unavailable"); peakMemory = Math.max(peakMemory, Number(match[1]) * 1024); } catch (err) { sampleError = err; return; } await delay(100); } })();
  let changedCollector = false;
  try {
    if (state === "slow") { await runProcess(["pause", collectorID]); changedCollector = true; }
    if (state === "unavailable") { await runProcess(["stop", "--time", "5", collectorID]); changedCollector = true; }
    await workload(fixture.warmup_requests, fixture.concurrency);
    const measured = await workload(fixture.measured_requests, fixture.concurrency);
    const saturation = await workload(fixture.saturation_requests, fixture.saturation_concurrency, true);
    const capture = path.join(os.tmpdir(), `exports-perf-${process.pid}.json`);
    const copied = await new Promise((resolve) => { const child = spawn("docker", ["cp", `${collectorID}:/telemetry.json`, capture], { stdio:"ignore" }); child.on("error", () => resolve(false)); child.on("close", (code) => resolve(code === 0)); });
    if (copied) {
      const payload = fs.readFileSync(capture, "utf8");
      for (const value of [fixture.content, "enterprise-export-performance-content-canary", apiKey, process.env.DENSE_MEM_CONTROL_TOKEN]) assert.ok(!payload.includes(value), "performance exporter leaked prohibited content");
      fs.unlinkSync(capture);
    } else assert.ok(variant === "base" || state !== "healthy", "healthy candidate Collector capture is missing");
    let exporters;
    if (variant === "candidate") {
      const response = await fetch(`${required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "")}/control/api/diagnostics/bundle`, { headers: { Authorization: `Bearer ${required("DENSE_MEM_CONTROL_TOKEN")}` }, signal: AbortSignal.timeout(10000) }); assert.equal(response.status, 200); exporters = (await response.json()).exporters; assert.ok(exporters.pending_spans <= 2048);
    }
    stopSampling = true; await sampler; if (sampleError) throw sampleError; assert.ok(peakMemory > 0);
    const result = { status: "ok", state, variant, repetition, fixture_sha256: digest(fixtureBytes), runner_sha256: digest(fs.readFileSync(new URL(import.meta.url))), server_image: (JSON.parse(await runProcess(["inspect", serverID]))[0]).Image, prohibited_content_found:false, measured, saturation: { count: saturation.count, concurrency: saturation.concurrency, semantic_signatures: [...new Set(saturation.signatures)] }, peak_rss_bytes: peakMemory, exporters, unexpected_failures: 0 };
    fs.writeFileSync(required("DENSE_MEM_E2E_RESULT_FILE"), `${JSON.stringify(result)}\n`, { mode: 0o600 });
    console.log(JSON.stringify({ status: "ok", state, variant, repetition, measured_requests: measured.count, p95_ms: measured.p95_ms, throughput_rps: measured.throughput_rps, peak_rss_bytes: peakMemory }));
  } finally {
    stopSampling = true; await sampler;
    if (changedCollector) await runProcess([state === "slow" ? "unpause" : "start", collectorID]);
  }
}

export function compare(receipts) {
  const summary = [];
  for (const state of fixture.states) {
    const pairs = Array.from({ length: fixture.paired_repetitions }, (_, offset) => {
      const pair = receipts.filter((value) => value.state === state && value.repetition === offset + 1);
      const base = pair.find((value) => value.variant === "base"), candidate = pair.find((value) => value.variant === "candidate");
      assert.ok(base && candidate && pair.length === 2, "five complete paired repetitions are required");
      for (const receipt of pair) { assert.equal(receipt.status, "ok"); assert.equal(receipt.fixture_sha256, digest(fixtureBytes)); assert.equal(receipt.runner_sha256,digest(fs.readFileSync(new URL(import.meta.url)))); assert.equal(receipt.prohibited_content_found,false); assert.equal(receipt.measured.count, 1000); assert.equal(receipt.measured.concurrency, 8); assert.equal(receipt.unexpected_failures, 0); }
      assert.ok(candidate.exporters.pending_spans <= 2048);
      assert.deepEqual(candidate.measured.signatures, base.measured.signatures, "semantic results differ"); assert.deepEqual(candidate.saturation.semantic_signatures, base.saturation.semantic_signatures, "saturation results differ");
      assert.ok(candidate.peak_rss_bytes - base.peak_rss_bytes <= 64 * (1 << 20), "candidate exceeded the 64 MiB memory regression bound");
      return { base, candidate };
    });
    const baseP95 = median(pairs.map(({ base }) => base.measured.p95_ms)), candidateP95 = median(pairs.map(({ candidate }) => candidate.measured.p95_ms));
    const baseThroughput = median(pairs.map(({ base }) => base.measured.throughput_rps)), candidateThroughput = median(pairs.map(({ candidate }) => candidate.measured.throughput_rps));
    assert.ok(candidateP95 - baseP95 <= Math.max(5, baseP95 * .1), `${state} p95 regression exceeded the approved bound`);
    assert.ok(candidateThroughput >= baseThroughput * .9, `${state} throughput loss exceeded 10%`);
    summary.push({ state, paired_repetitions: 5, measured_requests_per_run: 1000, concurrency: 8, base_p95_ms: baseP95, candidate_p95_ms: candidateP95, base_throughput_rps: baseThroughput, candidate_throughput_rps: candidateThroughput, max_rss_regression_bytes: Math.max(...pairs.map(({ base, candidate }) => candidate.peak_rss_bytes - base.peak_rss_bytes)), semantic_differences: 0, unexpected_failures: 0 });
  }
  return { version: 1, fixture_sha256: digest(fixtureBytes), states: summary };
}

if (process.argv[1] && new URL(`file://${process.argv[1]}`).href === import.meta.url) {
  const [directory, destination] = process.argv.slice(2); assert.ok(directory && destination);
  const receipts = fs.readdirSync(directory).filter((name) => name.endsWith("-result.json")).map((name) => JSON.parse(fs.readFileSync(`${directory}/${name}`, "utf8")));
  fs.writeFileSync(destination, `${JSON.stringify(compare(receipts), null, 2)}\n`);
}
