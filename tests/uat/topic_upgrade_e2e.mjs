import assert from "node:assert/strict";

const controlURL = process.env.DENSE_MEM_CONTROL_URL;
const controlToken = process.env.DENSE_MEM_CONTROL_TOKEN;
assert(controlURL && controlToken, "ontology upgrade requires the control endpoint");

async function control(path, body) {
  const response = await fetch(`${controlURL}/control/api${path}`, {
    method: body ? "PATCH" : "GET",
    headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
    signal: AbortSignal.timeout(10000),
  });
  assert.equal(response.status, 200, `control request failed: ${path}`);
  return (await response.json()).data;
}

const initial = await control("/topic-maintenance/status");
assert.equal(initial.paused, false, "offline upgrade must retain maintenance admission");
assert.equal(initial.usage.unreconciled_attempts, 0);
const provider = await fetch("http://synchronous-write-provider:8787/health", { signal: AbortSignal.timeout(10000) });
assert.equal(provider.status, 200);
assert.equal((await provider.json()).assessment_calls, 0, "upgrade must not call a model");
console.log(JSON.stringify({ scenario: "ontology_upgrade", status: "ok", control_available: true, unreconciled_attempts: 0 }));
