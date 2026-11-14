import { createHash } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";

export function rememberReceipt(response) {
  const result = response?.terminal?.result ?? response?.result ?? response;
  return result?.structuredContent ?? result;
}

function retryableEmbeddingTimeout(response, args) {
  const receipt = rememberReceipt(response);
  const envelope = response?.terminal?.result ?? response?.result ?? response;
  if (envelope?.structuredContent) {
    if (envelope.isError !== true) return false;
    const text = envelope.content?.find((item) => item.type === "text")?.text;
    try {
      if (JSON.stringify(JSON.parse(text)) !== JSON.stringify(receipt)) return false;
    } catch { return false; }
  }
  if (response?.error || response?.terminal?.error || envelope?.isError === false ||
      !args?.idempotency_key || !Array.isArray(args.evidence) || args.evidence.length === 0 ||
      receipt?.processing_state !== "failed" || receipt.search_state !== "not_required" ||
      receipt.submission_kind !== "remember" || receipt.space_kind !== args.target_space ||
      receipt.contract_version !== "dense-mem.v2.8.0" || !receipt.submission_id || !receipt.correlation_id ||
      !Array.isArray(receipt.evidence) || receipt.evidence.length !== args.evidence.length ||
      !Array.isArray(receipt.errors) || receipt.errors.length === 0) return false;
  const indices = new Set();
  for (const item of receipt.evidence) {
    if (!Number.isInteger(item.evidence_index) || item.evidence_index < 0 || item.evidence_index >= args.evidence.length ||
        indices.has(item.evidence_index) || item.disposition !== "not_stored" || item.search_state !== "not_required" || item.evidence_id ||
        !Array.isArray(item.superseded_evidence_ids) || item.superseded_evidence_ids.length ||
        item.splits?.some((split) => split.evidence_id || split.disposition !== "not_stored")) return false;
    indices.add(item.evidence_index);
  }
  return receipt.errors.every((error) => error.code === "request_timeout" && error.retryable === true &&
    error.next_action === "retry_same_request" && error.details?.component === "remember.embedding" &&
    error.details.server_owned === true && typeof error.message === "string" && error.message.length > 0 &&
    typeof error.remediation === "string" && error.remediation.length > 0);
}

export async function rememberWithEmbeddingRetry(args, call, { signal, retry = true, onAttempt = captureRememberAttempt } = {}) {
  const request = JSON.stringify(args);
  const requestHash = createHash("sha256").update(request).digest("hex");
  for (let attempt = 1; attempt <= 2; attempt++) {
    signal?.throwIfAborted();
    const started = performance.now();
    let response;
    try {
      response = await call(JSON.parse(request));
    } catch (error) {
      await onAttempt({ attempt, requestHash, elapsed_ms: performance.now() - started,
        response: { capture_state: "interrupted", error: { name: error.name, message: error.message } }, retrying: false, exhausted: false });
      throw error;
    }
    const timeout = retryableEmbeddingTimeout(response, JSON.parse(request));
    await onAttempt({ attempt, requestHash, elapsed_ms: performance.now() - started, response,
      retrying: retry && timeout && attempt === 1 && !signal?.aborted, exhausted: retry && timeout && attempt === 2 });
    signal?.throwIfAborted();
    if (!retry || !timeout) return response;
    if (attempt === 2) throw new Error("Remember embedding timed out on both retained E2E attempts");
    await delay(500, undefined, { signal });
  }
}

export function captureRememberAttempt({ attempt, requestHash, elapsed_ms, response, retrying, exhausted }) {
  const receipt = rememberReceipt(response);
  const payload = JSON.stringify(response?.terminal ? {
    terminal: response.terminal, progress: response.progress, raw: response.raw,
  } : response);
  if (Buffer.byteLength(payload) > 4 << 20) throw new Error("Remember attempt capture exceeds 4 MiB");
  const points = Array.from(payload);
  const digest = createHash("sha256").update(payload).digest("hex");
  const count = Math.max(1, Math.ceil(points.length / 2048));
  for (let index = 0; index < count; index++) process.stderr.write(JSON.stringify({
    event: "e2e_remember_attempt", level: exhausted ? "ERROR" : retrying ? "WARN" : "INFO",
    run_id: process.env.DENSE_MEM_E2E_RUN_ID || "", source_revision: process.env.DENSE_MEM_E2E_COMMIT_SHA || "",
    scenario: process.env.DENSE_MEM_E2E_SCENARIO || "", logical_request_hash: requestHash,
    attempt, elapsed_ms, correlation_id: receipt?.correlation_id || "", submission_id: receipt?.submission_id || "",
    retrying, exhausted, retry_delay_ms: retrying ? 500 : 0,
    payload_chunk: { index, count, sha256: digest, state: "captured", body: points.slice(index * 2048, (index + 1) * 2048).join("") },
  }) + "\n");
}
