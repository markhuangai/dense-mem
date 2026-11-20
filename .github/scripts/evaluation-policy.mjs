import { createHash } from "node:crypto";
import imagePolicy from "./image-release-policy.cjs";

export function manualEvaluationRequest({ image, mode = "live", repository, ref, runAttempt, actorPermission, triggeringPermission }) {
  if (ref !== "refs/heads/main" || Number(runAttempt) !== 1) throw new Error("Manual evaluation requires a fresh workflow dispatch from main");
  if (actorPermission !== "admin" || triggeringPermission !== "admin") throw new Error("Manual evaluation requires initiating and triggering repository admins");
  const reference = typeof image === "string" && typeof repository === "string" && !image.includes("/")
    ? `ghcr.io/${repository.toLowerCase()}:${image}`
    : image;
  const decision = imagePolicy.validateProductionImageReference(reference, repository);
  if (!decision.valid) throw new Error(decision.reason);
  if (!["fixtures", "live"].includes(mode)) throw new Error("Invalid evaluation mode");
  return { image: reference, mode };
}

export function resolveEvaluationImage(reference, repository, inspected) {
  const decision = imagePolicy.validateProductionImageReference(reference, repository);
  if (!decision.valid) throw new Error(decision.reason);
  const labels = inspected.Config?.Labels || {};
  if (inspected.Os !== "linux" || inspected.Architecture !== "amd64" ||
      labels["org.opencontainers.image.variant"] !== "production" ||
      labels["org.opencontainers.image.source"] !== `https://github.com/${repository}` ||
      !/^[0-9a-f]{40}$/.test(labels["org.opencontainers.image.revision"] || "")) throw new Error("Evaluation requires a Linux amd64 Dense-Mem production image with source metadata");
  const requestedDigest = reference.includes("@") ? reference.split("@")[1] : undefined;
  const matches = [...new Set(inspected.RepoDigests || [])].filter((candidate) => {
    return candidate.startsWith(`${decision.repository}@`) &&
      imagePolicy.validatePinnedProductionImageReference(candidate, repository).valid &&
      (!requestedDigest || candidate.split("@")[1] === requestedDigest);
  });
  if (matches.length !== 1) throw new Error("Evaluation image digest is missing, ambiguous or differs from the requested image");
  return { image: matches[0], revision: labels["org.opencontainers.image.revision"] };
}

export function requireEvaluationImportSupport(inspected, { skipAISecurityReview = false } = {}) {
  if (inspected.Config?.Labels?.["io.dense-mem.postgres-advisory-lock-concurrency"] !== "configurable") {
    throw new Error("Evaluation requires a newer production image with configurable import admission");
  }
  if (skipAISecurityReview && inspected.Config?.Labels?.["io.dense-mem.public-seed-admission"] !== "locked-requests-v1") {
    throw new Error("Evaluation requires a production image supporting locked public seed admission");
  }
}

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

export function resolveImportWorkers(value) {
  return Number(resolveSetting("DENSE_MEM_EVAL_IMPORT_WORKERS", [value], (v) => /^[1-9][0-9]*$/.test(v) && Number(v) <= 100));
}

export function resolveAnswerWorkers(value) {
  return Number(resolveSetting("EVAL_ANSWER_WORKERS", [value, "4"], (v) => /^(?:[4-9]|10)$/.test(v)));
}

export function configuration(env, { allowHTTPVerifier = false } = {}) {
  const model = resolveSetting("DENSE_MEM_EVAL_MODEL", [env.DENSE_MEM_EVAL_MODEL], (v) => /^[A-Za-z0-9@][A-Za-z0-9_./:-]{0,127}$/.test(v) && !v.includes("://"));
  const context = resolveSetting("DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS", [env.DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS], (v) => /^[1-9][0-9]*$/.test(v) && Number.isSafeInteger(Number(v)) && Number(v) >= 16384);
  const providerURL = (value, allowHTTP = false) => {
    try {
      const url = new URL(value);
      const schemeAllowed = url.protocol === "https:" || (allowHTTP && url.protocol === "http:");
      return schemeAllowed && !url.username && !url.password && !url.search && !url.hash;
    } catch { return false; }
  };
  const verifier = resolveSetting("DENSE_MEM_EVAL_VERIFIER_URL", [env.DENSE_MEM_EVAL_VERIFIER_URL], (value) => providerURL(value, allowHTTPVerifier));
  const embedding = resolveSetting("DENSE_MEM_EVAL_EMBED_URL", [env.DENSE_MEM_EVAL_EMBED_URL], (value) => providerURL(value) && /^https:\/\/api\.openai\.com\/v1\/?$/.test(value));
  for (const key of ["DENSE_MEM_EVAL_API_KEY", "DENSE_MEM_EVAL_EMBED_API_KEY"]) resolveSetting(key, [env[key]], (v) => v.length > 0 && !/[\r\n]/.test(v));
  if (env.WORKFLOW_EMBEDDING_MODEL !== "text-embedding-3-small" || env.WORKFLOW_EMBEDDING_DIMENSIONS !== "1536") throw new Error("Workflow embedding pins are invalid");
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

export function evaluationDatasets(registry, selection = "") {
  const { dataset: registered } = registryMatrix(registry);
  if (selection === "") return registry.datasets;
  let flags;
  try {
    flags = JSON.parse(selection);
  } catch {
    throw new Error("Dataset selection must be a JSON object of public dataset flags");
  }
  if (!flags || typeof flags !== "object" || Array.isArray(flags) ||
      Object.entries(flags).some(([id, enabled]) => !registered.includes(id) || typeof enabled !== "boolean")) {
    throw new Error("Dataset selection must name registered public datasets with boolean flags");
  }
  const selected = registry.datasets.filter(({ id }) => flags[id] === true);
  if (selected.length === 0) throw new Error("Dataset selection requires at least one public seed");
  return selected;
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
  const identities = new Set(rows.filter((row) => row.identity).map(({ identity }) => JSON.stringify([
    identity.source_revision, identity.image, identity.registry_sha256, identity.model, identity.embedding_model,
    identity.embedding_dimensions, identity.context_window_tokens, identity.workflow_run_id,
    resolveAnswerWorkers(identity.answer_workers),
  ])));
  if (identities.size > 1) throw new Error("Dataset jobs used different campaign identities");
  return { complete: rows.every((row) => row.complete), expected_answers: expected, completed_answers: rows.reduce((sum, row) => sum + row.completed, 0), datasets: rows };
}

export function ownedResource(resource, runID) {
  if (resource?.Labels?.["io.dense-mem.eval.run"] !== runID) throw new Error("Cleanup ownership differs from this dataset job");
  return true;
}
