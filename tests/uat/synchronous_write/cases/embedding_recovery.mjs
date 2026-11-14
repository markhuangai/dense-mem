import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { rememberReceipt, captureRememberAttempt } from "../remember_retry.mjs";
import { progressCall } from "./progress.mjs";
import { rememberCanonicalCounts, postgresQuery } from "./remember.mjs";

export const name = "embedding_recovery";

export async function run({ runStep }) {
  const teamID = required("DENSE_MEM_E2E_TEAM_ID");
  const apiKey = required("DENSE_MEM_E2E_API_KEY");
  const results = [];
  for (const repeated of [false, true]) await runStep(repeated ? "second-timeout-fails" : "timeout-recovery-and-replay", async () => {
    const key = randomUUID();
    const args = { target_space: "team_shared", idempotency_key: key,
      evidence: [{ content: `Retained embedding recovery ${key}. [fixture-fault:${repeated ? "embedding-timeout" : "embedding-once-timeout"}]`, source_type: "manual", force_insert: true }] };
    const frozen = JSON.stringify(args);
    const attempts = [];
    const execute = () => progressCall({ apiKey, args, token: '"embedding-recovery"',
      onAttempt: (attempt) => {
        captureRememberAttempt(attempt);
        attempts.push(attempt);
        assert.equal(JSON.stringify(args), frozen);
        const receipt = rememberReceipt(attempt.response);
        if (receipt.processing_state === "failed") {
          assert.equal(receipt.errors[0].code, "request_timeout");
          assert.equal(receipt.errors[0].details.component, "remember.embedding");
          assert(rememberCanonicalCounts(teamID, receipt.submission_id).every((count) => count === 0), "a timed-out attempt must commit zero canonical state");
        }
      } });
    if (repeated) await assert.rejects(execute(), /both retained E2E attempts/);
    else {
      const recovered = rememberReceipt(await execute());
      assert.equal(recovered.processing_state, "completed");
      assert(rememberCanonicalCounts(teamID, recovered.submission_id).every((count) => count === 1), "recovery must commit one complete Evidence and its search state");
      const before = await health();
      const replay = rememberReceipt(await progressCall({ apiKey, args, token: '"embedding-replay"', retryEmbedding: false }));
      assert.deepEqual(replay, recovered);
      const after = await health();
      assert.equal(after.embedding_calls, before.embedding_calls, "replay must not repeat embedding work");
      assert.equal(after.assessment_calls, before.assessment_calls, "replay must not repeat assessment work");
    }
    assert.equal(attempts.length, 2);
    assert.equal(attempts[0].requestHash, attempts[1].requestHash);
    for (const attempt of attempts) {
      assert(attempt.response.progress.length > 0);
      assert(attempt.response.raw.includes('"notifications/progress"'));
    }
    const outcomes = postgresQuery(`SELECT outcome FROM remember_attempts WHERE team_id='${teamID}'::uuid AND idempotency_key='${key}' ORDER BY created_at, attempt_id`).split(/\r?\n/);
    assert.deepEqual(outcomes, ["failed", repeated ? "failed" : "completed"], "both attempt outcomes must remain durable");
    results.push({ repeated, attempts: attempts.map((attempt) => ({ submission_id: rememberReceipt(attempt.response).submission_id,
      correlation_id: rememberReceipt(attempt.response).correlation_id, progress_count: attempt.response.progress.length,
      outcome: rememberReceipt(attempt.response).processing_state })) });
  });
  return { results };
}

async function health() {
  const response = await fetch(required("DENSE_MEM_E2E_PROVIDER_URL").replace(/\/$/, "") + "/health");
  assert(response.ok);
  return response.json();
}
function required(name) { assert(process.env[name], `${name} is required`); return process.env[name]; }
