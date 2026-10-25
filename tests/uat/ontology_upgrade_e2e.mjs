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

const initial = await control("/ontology/status");
assert.equal(initial.paused, false, "startup must restore its automatic pause");
assert.equal(initial.enabled, true, "upgrade must retain saved enablement");
assert.equal(initial.configuration_error, "configuration_invalid", "missing model capacity remains visible");
assert.equal(initial.historical_usage.unavailable_usage_attempts, 1);
assert.equal(initial.usage.unreconciled_attempts, 0);
const provider = await fetch("http://synchronous-write-provider:8787/health", { signal: AbortSignal.timeout(10000) });
assert.equal(provider.status, 200);
assert.equal((await provider.json()).assessment_calls, 0, "upgrade preparation must not call a model");

await control("/config/ontology-maintenance", {
  items: [{ key: "ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "300000" }],
});
let status;
for (let attempt = 0; attempt < 120; attempt++) {
  status = await control("/ontology/status");
  if (status.counts.organized > 0 && status.usage.attempts > 0) break;
  await new Promise((resolve) => setTimeout(resolve, 1000));
}
assert.equal(status.paused, false);
assert(status.counts.organized > 0 && status.usage.attempts > 0, "normal maintenance must progress after configuration without an upgrade command");
assert.equal(status.usage.unreconciled_attempts, 0);
console.log(JSON.stringify({ scenario: "ontology_upgrade", status: "ok", control_available: true, missing_capacity_explicit: true, automatic_progress: true }));
