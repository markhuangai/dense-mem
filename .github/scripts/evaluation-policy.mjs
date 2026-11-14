import { createHash } from "node:crypto";

export function requireRunner(env = process.env) {
  if (env.GITHUB_ACTIONS !== "true" || env.RUNNER_ENVIRONMENT !== "github-hosted" || !env.GITHUB_RUN_ID || env.GITHUB_RUN_ATTEMPT !== "1") {
    throw new Error("Evaluation requires a fresh GitHub-hosted workflow run. Local execution and job reruns are unsupported.");
  }
}

export function resolveSetting(name, candidates, validate) {
  const value = candidates.find((candidate) => candidate !== undefined && candidate !== "");
  if (value === undefined) throw new Error(`Missing required Actions setting: ${name}`);
  if (!validate(value)) throw new Error(`Invalid Actions setting: ${name}`);
  return value;
}

export function configuration(env) {
  const model = resolveSetting("DENSE_MEM_EVAL_MODEL", [env.DENSE_MEM_EVAL_MODEL], (v) => /^[A-Za-z0-9@][A-Za-z0-9_./:-]{0,127}$/.test(v) && !v.includes("://"));
  const context = resolveSetting("DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS", [env.DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS], (v) => /^[1-9][0-9]*$/.test(v) && Number.isSafeInteger(Number(v)) && Number(v) >= 16384);
  const httpsURL = (v) => { try { const url = new URL(v); return url.protocol === "https:" && !url.username && !url.password && !url.search && !url.hash; } catch { return false; } };
  const verifier = resolveSetting("DENSE_MEM_EVAL_VERIFIER_URL", [env.DENSE_MEM_EVAL_VERIFIER_URL], httpsURL);
  const embedding = resolveSetting("DENSE_MEM_EVAL_EMBED_URL", [env.DENSE_MEM_EVAL_EMBED_URL], (v) => httpsURL(v) && /^https:\/\/api\.cloudflare\.com\/client\/v4\/accounts\/[a-f0-9]{32}\/ai\/v1\/?$/.test(v));
  for (const key of ["DENSE_MEM_EVAL_API_KEY", "DENSE_MEM_EVAL_EMBED_API_KEY"]) resolveSetting(key, [env[key]], (v) => v.length > 0 && !/[\r\n]/.test(v));
  if (env.WORKFLOW_EMBEDDING_MODEL !== "@cf/baai/bge-m3" || env.WORKFLOW_EMBEDDING_DIMENSIONS !== "1024") throw new Error("Workflow embedding pins are invalid");
  return { model, context: Number(context), verifier, embedding };
}

export const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");

export function registryMatrix(registry) {
  if (registry.version !== 1 || registry.revision !== "5ec05b38deecc3318bb432c69865959c56058990" || !registry.datasets?.length) throw new Error("Invalid public dataset registry");
  const ids = new Set();
  for (const dataset of registry.datasets) {
    if (!/^[a-z0-9_]+$/.test(dataset.id) || ids.has(dataset.id)) throw new Error("Invalid or duplicate dataset registration");
    ids.add(dataset.id);
    for (const source of [dataset.corpus, dataset.questions]) {
      if (!/^[a-z0-9_.]+\.json$/.test(source.path) || !/^[a-f0-9]{64}$/.test(source.sha256) || !Number.isSafeInteger(source.rows) || source.rows < 1) throw new Error("Invalid source lock");
    }
  }
  return { dataset: [...ids] };
}

export function aggregate(registry, reports) {
  const expected = registry.datasets.reduce((sum, dataset) => sum + dataset.questions.rows * 4, 0);
  const rows = registry.datasets.map((dataset) => {
    const matches = reports.filter((report) => report.identity?.dataset === dataset.id);
    if (matches.length !== 1) return { dataset: dataset.id, expected: dataset.questions.rows * 4, completed: 0, complete: false, unavailable: matches.length ? "duplicate reports" : "missing or timed-out axis" };
    const report = matches[0];
    if (report.expected_answers !== dataset.questions.rows * 4 || report.corpus_rows !== dataset.corpus.rows) throw new Error("Report denominator differs from source registry");
    return { dataset: dataset.id, expected: report.expected_answers, completed: report.completed_answers, complete: report.complete === true && report.capture_complete === true, matrix: report.comparison_matrix, ontology_delta: report.ontology_delta, identity: report.identity, failure: report.failure || "" };
  });
  const identities = new Set(rows.filter((row) => row.identity).map((row) => JSON.stringify([row.identity.source_revision, row.identity.image, row.identity.registry_sha256, row.identity.model, row.identity.embedding_model, row.identity.embedding_dimensions, row.identity.context_window_tokens, row.identity.workflow_run_id])));
  if (identities.size > 1) throw new Error("Dataset jobs used different campaign identities");
  return { complete: rows.every((row) => row.complete), expected_answers: expected, completed_answers: rows.reduce((sum, row) => sum + row.completed, 0), datasets: rows };
}

export function ownedResource(resource, runID) {
  if (resource?.Labels?.["io.dense-mem.eval.run"] !== runID) throw new Error("Cleanup ownership differs from this dataset job");
  return true;
}
