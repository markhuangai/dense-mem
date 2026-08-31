export const name = "reconciliation";
export async function run({ rpc, expect }) {
  const listed = await rpc("tools/list", {});
  const tools = new Set((listed.tools ?? []).map((tool) => tool.name));
  expect(tools.has("remember"), "reconciliation requires the supported Remember entry point");
  expect(!tools.has("get_submission_status"), "reconciliation must not expose the removed status surface");
  return { mode: name, status: "document-centric-reconciliation", tool_count: tools.size };
}
