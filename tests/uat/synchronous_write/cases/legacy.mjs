export const name = "legacy";

export async function run({ rpc, expect }) {
  const listed = await rpc("tools/list", {});
  const names = new Set((listed.tools || []).map((tool) => tool.name));
  expect(names.has("remember"), "legacy surface must expose remember");
  expect(names.has("get_submission_status"), "legacy surface must expose status polling");

  const result = await rpc("tools/call", {
    name: "remember",
    arguments: {
      evidence: [{ content: "synchronous-write legacy fixture", source_type: "manual" }],
      relationships: [{
        ref: "fixture-relation",
        subject: { name: "Dense-Mem", entity_kind: "project" },
        predicate: { proposed_key: "uses" },
        object: { value: { type: "string", value: "PostgreSQL" } },
        polarity: "+",
        evidence_indices: [0],
      }],
      idempotency_key: `synchronous-write-legacy-${Date.now()}`,
    },
  });
  const payload = result?.content?.[0]?.text ? JSON.parse(result.content[0].text) : result;
  expect(payload?.processing_state === "queued" || payload?.processing_state === "processing" || payload?.processing_state === "completed", "legacy remember must return a processing receipt");
  expect(payload?.status_tool === "get_submission_status", "legacy receipt must advertise polling");
  return { mode: "legacy", processing_state: payload.processing_state };
}
