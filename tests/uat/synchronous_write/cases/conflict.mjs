import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

export const name = "conflict";

export async function run() {
  const script = fileURLToPath(new URL("../../conflict_mcp_e2e.mjs", import.meta.url));
  const conflictProviderURL = process.env.DENSE_MEM_E2E_CONFLICT_PROVIDER_URL;
  const embeddingModel = process.env.DENSE_MEM_E2E_SERVER_EMBEDDING_MODEL || "dense-mem-conflict-e2e-embedding";
  const embeddingDimensions = process.env.DENSE_MEM_E2E_SERVER_EMBEDDING_DIMENSIONS || "1536";
  const env = conflictProviderURL
    ? {
        ...process.env,
        AI_API_URL: conflictProviderURL,
        AI_API_KEY: "dense-mem-conflict-e2e-key",
        AI_API_EMBEDDING_MODEL: embeddingModel,
        AI_API_EMBEDDING_DIMENSIONS: embeddingDimensions,
        AI_API_EMBEDDING_TIMEOUT_SECONDS: "10",
        AI_VERIFIER_API_URL: conflictProviderURL,
        AI_VERIFIER_API_KEY: "dense-mem-conflict-e2e-key",
        AI_VERIFIER_MODEL: "dense-mem-conflict-e2e-verifier",
        AI_VERIFIER_TIMEOUT_SECONDS: "2",
        AI_VERIFIER_DISABLE_TEMPERATURE: "true",
      }
    : process.env;
  const result = spawnSync(process.execPath, [script], { encoding: "utf8", env });
  if (result.status !== 0) {
    throw new Error(`conflict synchronous-write case failed: ${result.stderr || result.stdout}`);
  }
  const output = JSON.parse(result.stdout);
  if (output.status !== "ok") {
    throw new Error("conflict synchronous-write case did not report success");
  }
  return { mode: name, conflict_run_id: output.run_id };
}
