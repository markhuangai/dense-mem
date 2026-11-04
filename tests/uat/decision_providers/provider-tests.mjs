import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";

export async function verifyProviderTests({ base, token, memoryKey, fixture, project, compose }) {
  const headers = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
  const api = async (path, body) => {
    const response = await fetch(`${base}/control/api/${path}`, { headers, ...(body === undefined ? {} : { method: "POST", body: JSON.stringify(body) }) });
    assert(response.ok, `provider test control request HTTP ${response.status}`);
    return (await response.json()).data;
  };
  const setMode = async (mode) => {
    const response = await fetch(`${fixture}/decision-fixture`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode }) });
    assert(response.ok, "provider fixture control must succeed");
  };
  const receipts = [];
  const targets = await api("diagnostics/providers");
  assert.equal(new Set(targets.map((target) => `${target.endpoint}\0${target.model}\0${target.credential_configuration}`)).size, targets.length, "same endpoint/model/credentials must form one target");
  for (const operation of ["assess", "embed", "decide"]) assert(targets.some((target) => target.operation === operation && target.available), `${operation} must be configured for the production scenario`);
  const decision = targets.find((target) => target.operation === "decide" && target.available);
  for (const authorization of [undefined, `Bearer ${memoryKey}`]) {
    const response = await fetch(`${base}/control/api/diagnostics/provider-tests`, { method: "POST", headers: { "Content-Type": "application/json", ...(authorization ? { Authorization: authorization } : {}) }, body: JSON.stringify({ target_id: decision.target_id }) });
    assert.equal(response.status, 401, "ordinary memory credentials cannot invoke operator tests");
  }
  const invalid = await fetch(`${base}/control/api/diagnostics/provider-tests`, { method: "POST", headers, body: JSON.stringify({ target_id: decision.target_id, url: "http://arbitrary.invalid" }) });
  assert.equal(invalid.status, 422, "clients cannot select a destination");
  for (const target of targets.filter((entry) => entry.available)) {
    const result = await api("diagnostics/provider-tests", { target_id: target.target_id });
    assert.equal(result.outcome, "succeeded", `${target.operation} sample must validate`);
    assert.equal(result.http_status, 200);
    assert.equal(result.network_state, "http_response_received");
    assert.equal(result.capture_state, "captured");
    assert(result.test_id && result.correlation_id && result.server_instance);
    assert.equal(result.target.model, target.model);
    receipts.push(result);
  }

  const configResponse = await fetch(`${base}/control/api/config/ontology-maintenance`, { headers });
  assert(configResponse.ok);
  const config = (await configResponse.json()).data;
  const savedModel = config.items.find((item) => item.key === "ONTOLOGY_MAINTENANCE_MODEL").value;
  const updateModel = async (value) => {
    const response = await fetch(`${base}/control/api/config/ontology-maintenance`, { method: "PATCH", headers, body: JSON.stringify({ items: [{ key: "ONTOLOGY_MAINTENANCE_MODEL", value }] }) });
    assert(response.ok, `ontology diagnostic model configuration HTTP ${response.status}`);
  };
  try {
    const old = targets.find((target) => target.roles.includes("Ontology maintenance"));
    assert(old, "ontology effective model must appear in provider discovery");
    await updateModel("provider-test-model-override");
    const updated = await api("diagnostics/providers");
    const override = updated.find((target) => target.model === "provider-test-model-override");
    assert(override?.roles.some((role) => ["Ontology maintenance", "Ontology maintenance (next window)"].includes(role)));
    const stale = await fetch(`${base}/control/api/diagnostics/provider-tests`, { method: "POST", headers, body: JSON.stringify({ target_id: old.target_id }) });
    if (stale.status === 200) {
      const result = (await stale.json()).data;
      assert.equal(result.target.model, old.model, "a still-active target must retain its original model");
      assert.equal(result.outcome, "succeeded");
      receipts.push(result);
    } else {
      assert.equal(stale.status, 409, "a stale target cannot silently test another model");
    }
    const result = await api("diagnostics/provider-tests", { target_id: override.target_id });
    assert.equal(result.outcome, "succeeded");
    assert.equal(result.target.model, "provider-test-model-override");
    receipts.push(result);
  } finally { await updateModel(savedModel); }

  try {
    for (const [mode, reason, network] of [["401", "authentication_failed", "http_response_received"], ["429", "rate_limited", "http_response_received"], ["529", "provider_http_error", "http_response_received"], ["malformed", "invalid_response", "http_response_received"], ["disconnect", "transport_failed", "no_http_response"]]) {
      await setMode(mode);
      const result = await api("diagnostics/provider-tests", { target_id: decision.target_id });
      assert.equal(result.outcome, "failed");
      assert.equal(result.reason, reason);
      assert.equal(result.network_state, network);
      assert.equal(result.exchanges.length, mode === "malformed" ? 2 : 1, "only existing bounded regeneration may repeat the sample");
      receipts.push(result);
    }
  } finally { await setMode("normal"); }

  const logs = spawnSync("docker", ["compose", "-p", project, "-f", compose, "logs", "--no-color", "server"], { encoding: "utf8", maxBuffer: 32 * 1024 * 1024 });
  assert.equal(logs.status, 0, "full server console log collection must succeed");
  const text = `${logs.stdout}\n${logs.stderr}`;
  for (const secret of [token, "dense-mem-e2e-decision-key", process.env.AI_API_KEY].filter(Boolean)) assert(!text.includes(secret), "collected logs must protect operational credentials");
  const records = text.split("\n").flatMap((line) => { const start = line.indexOf("{"); if (start < 0) return []; try { return [JSON.parse(line.slice(start))]; } catch { return []; } });
  for (const receipt of receipts) {
    const related = records.filter((record) => record.correlation_id === receipt.correlation_id);
    const completed = related.find((record) => record.msg === "provider_test.completed" && record.test_id === receipt.test_id);
    assert(completed, "console logs alone must identify the test's terminal outcome");
    assert.equal(completed.level, receipt.outcome === "failed" ? "ERROR" : "INFO");
    assert.equal(completed.reason, receipt.reason);
    assert.equal(completed.network_state, receipt.network_state);
    assert(related.some((record) => record.msg === "provider_test.started" && record.test_id === receipt.test_id && record.level === "INFO"));
    const payloads = related.filter((record) => record.msg === "ai_exchange_payload" && record.payload_chunk);
    const groups = Map.groupBy(payloads, (record) => `${record.exchange_id}:${record.direction}`);
    const expected = new Set();
    const verifyChunks = (exchange, direction, state) => {
      const key = `${exchange.exchange_id}:${direction}`;
      expected.add(key);
      const entries = groups.get(key);
      assert(entries?.length, `missing ${direction} chunks for observed exchange ${exchange.exchange_id}`);
      assert(entries.every((entry) => entry.level === "DEBUG" && entry.transport_capture_state === state));
      const chunks = entries.map((entry) => entry.payload_chunk);
      assert(chunks.every((chunk) => chunk.state === "captured"));
      assert.equal(chunks.length, chunks[0].count);
      assert(chunks.every((chunk) => chunk.count === chunks[0].count && chunk.sha256 === chunks[0].sha256));
      assert.deepEqual(chunks.map((chunk) => chunk.index), chunks.map((_, index) => index));
      assert.equal(createHash("sha256").update(chunks.map((chunk) => chunk.body).join("")).digest("hex"), chunks[0].sha256);
      return chunks.map((chunk) => chunk.body).join("");
    };
    assert(receipt.exchanges.length > 0, "every dispatched sample must have observed exchanges");
    for (const exchange of receipt.exchanges) {
      const detail = related.find((record) => record.msg === "provider_test.exchange" && record.exchange.exchange_id === exchange.exchange_id);
      assert(detail && detail.level === "DEBUG", "every attempt must have network detail in DEBUG logs");
      verifyChunks(exchange, "request", exchange.request_capture_state);
      const response = verifyChunks(exchange, "response", exchange.response_capture_state);
      if (exchange.status_code === 0) {
        assert.equal(exchange.response_capture_state, "no_response");
        assert.equal(response, "");
        assert(exchange.failure_phase && exchange.error, "no-response attempts must record their observed failure");
        assert(verifyChunks(exchange, "transport_error", "captured"), "transport failure text must be reconstructable");
      }
    }
    assert.deepEqual(new Set(groups.keys()), expected, "payload groups must cover exactly the observed attempts and directions");
    if (receipt.reason === "authentication_failed") assert(payloads.some((record) => record.payload_chunk.body.includes("admitted provider detail")), "an ordinary token field must remain exact in protected response logs");
  }
  process.stdout.write(`${JSON.stringify({ status: "ok", provider_tests: receipts.map(({ target, outcome, reason, test_id, correlation_id }) => ({ operation: target.operation, model: target.model, outcome, reason, test_id, correlation_id })), log_only_diagnosis: "verified" })}\n`);
}
