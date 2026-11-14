#!/usr/bin/env node
import { readdir } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { rememberWithEmbeddingRetry } from "./remember_retry.mjs";

const root = dirname(fileURLToPath(import.meta.url));
const casesRoot = join(root, "cases");
const requested = (process.env.DENSE_MEM_E2E_WRITE_CASE || "").trim();

export async function discoverCases(directory = casesRoot, filter = requested) {
  const names = (await readdir(directory, { withFileTypes: true }))
    .filter((entry) => entry.isFile() && entry.name.endsWith(".mjs"))
    .map((entry) => entry.name)
    .sort((left, right) => left.localeCompare(right));
  const selected = filter ? names.filter((name) => name === `${filter}.mjs`) : names;
  if (filter && selected.length === 0) throw new Error(`synchronous-write case ${filter} was not found`);
  return selected.map((name) => pathToFileURL(join(directory, name)).href);
}

export async function runCases({ rpc = liveRPC, rawRPC = liveRawRPC, expect = assert } = {}) {
  const modules = [];
  for (const url of await discoverCases()) {
    const module = await import(url);
    if (typeof module.run !== "function" || typeof module.name !== "string") throw new Error(`invalid synchronous-write case ${url}`);
    modules.push({ name: module.name, result: await runStep(module.name,
      () => module.run({ rpc, rawRPC, expect, runStep: (label, fn) => runStep(`${module.name}/${label}`, fn) })) });
  }
  return modules;
}

export async function runStep(label, fn) {
  const started = performance.now();
  const report = (outcome) => process.stderr.write(JSON.stringify({
    event: "e2e_case_progress", level: outcome === "failed" ? "ERROR" : "INFO", time: new Date().toISOString(),
    run_id: process.env.DENSE_MEM_E2E_RUN_ID || "", run_attempt: process.env.DENSE_MEM_E2E_RUN_ATTEMPT || "",
    source_revision: process.env.DENSE_MEM_E2E_COMMIT_SHA || "", scenario: process.env.DENSE_MEM_E2E_SCENARIO || "",
    case: label, outcome, elapsed_ms: Math.round(performance.now() - started),
  }) + "\n");
  report("started");
  let result;
  try {
    result = await fn();
  } catch (error) {
    report("failed");
    throw error;
  }
  report("passed");
  return result;
}

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) {
  const results = await runCases({ rpc: liveRPC });
  process.stdout.write(`${JSON.stringify({ status: "ok", cases: results }, null, 2)}\n`);
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

async function liveRPC(method, params, { retryEmbedding = true, signal } = {}) {
  const payload = await liveRawRPC(method, params, signal, { retryEmbedding });
  if (payload.error) throw new Error(`MCP ${method} returned a bounded error: ${payload.error.message || "unknown"}`);
  return payload.result ?? payload;
}

async function liveRawRPC(method, params, signal, { retryEmbedding = false } = {}) {
  const base = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
  const key = required("DENSE_MEM_E2E_API_KEY");
  const call = async (arguments_) => {
  const response = await fetch(`${base}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: Date.now(), method, params: arguments_ ? { ...params, arguments: arguments_ } : params }),
    signal,
  });
  const body = await response.text();
  if (!response.ok) throw new Error(`MCP request failed with HTTP ${response.status}`);
  return body ? JSON.parse(body) : {};
  };
  if (method === "tools/call" && params.name === "remember") {
    return rememberWithEmbeddingRetry(params.arguments, call, { signal, retry: retryEmbedding });
  }
  return call();
}

function required(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}
