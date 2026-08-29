export const name = "reconciliation";
export async function run({ rpc, expect }) {
  const listed = await rpc("tools/list", {});
  const tools = new Set((listed.tools ?? []).map((tool) => tool.name));
  expect(tools.has("remember"), "reconciliation compatibility requires the supported Remember entry point");
  expect(tools.has("get_submission_status"), "reconciliation compatibility requires the legacy status surface until final cutover");
  return { mode: name, status: "legacy-reconciliation-compatible", tool_count: tools.size };
}
