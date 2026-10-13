export const name = "dream";

export async function run({ rpc, rawRPC, expect }) {
  const listed = await rpc("tools/list", {});
  const names = new Set(listed.tools.map((tool) => tool.name));
  for (const name of ["list_dreams", "get_dream", "resolve_dream_feedback"]) {
    expect(!names.has(name), `${name} must remain retired`);
    const response = await rawRPC("tools/call", { name, arguments: {} });
    expect(response.error?.code === -32601, `${name} must return tool not found`);
  }
  return { retired_tools: true, no_confirmation_writer: true };
}
