import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

export const name = "conflict";

export async function run() {
  const script = fileURLToPath(new URL("../../conflict_mcp_e2e.mjs", import.meta.url));
  const result = spawnSync(process.execPath, [script], { encoding: "utf8", env: process.env });
  if (result.status !== 0) {
    throw new Error(`conflict synchronous-write case failed: ${result.stderr || result.stdout}`);
  }
  const output = JSON.parse(result.stdout);
  if (output.status !== "ok") {
    throw new Error("conflict synchronous-write case did not report success");
  }
  return { mode: name, conflict_run_id: output.run_id };
}
