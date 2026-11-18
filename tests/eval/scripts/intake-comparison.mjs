import fs from "node:fs";
import path from "node:path";
import { requireRunner, configuration, registryMatrix, sha256 } from "../../../.github/scripts/evaluation-policy.mjs";
import { callTool } from "../../uat/decision_providers/rpc.mjs";
import { securityIntakeCases } from "../../uat/security_intake_cases.mjs";

const baseline = {
  revision: "46d2c974b4720cb9783bda31ef4f78420518f3aa",
  image: "ghcr.io/markhuangai/dense-mem@sha256:4c93a73b2e9e3b6fcbc119528652c81e59a6ad25c6269695a3eb940dcf9da8d7",
};

const pinnedRows = {
  hotpotqa: ["1338", "3430", "9465", "9797", "9798"],
  musique: ["row:3913", "row:4083", "row:5488", "row:5803", "row:9656"],
  "2wikimultihopqa": ["row:941", "row:5775"],
  nq_rear: ["2464", "6643", "6839", "8185", "8710"],
  popqa: ["19940295", "10787165", "18698113", "8554867", "30826023", "6457075", "17728447", "7567292", "13343698", "row:6645", "24091860"],
};

function controls() {
  const { structuralRejections, rejectedCases, acceptedCases } = securityIntakeCases("intake-control");
  return [
    ...rejectedCases.map((entry) => ({
      id: entry.name, content: `Project uses Store. ${entry.content}`, source: entry.clientComment || "intake-control",
      baseline: "initial_scan", candidate: structuralRejections.has(entry.name) ? "initial_scan" : "intake_review",
    })),
    ...acceptedCases.map((entry) => ({
      id: entry.name, content: `Project uses Store. ${entry.payload}`, source: entry.clientComment || "intake-control",
      baseline: "accepted", candidate: "accepted",
    })),
  ];
}

async function lockedCases(registry, directory, downloadSources, signal) {
  const cases = [];
  for (const dataset of registry.datasets) {
    const wanted = new Set(pinnedRows[dataset.id]);
    if (!wanted.size) throw new Error("Missing pinned intake rows");
    await downloadSources(registry, dataset, directory, signal);
    const rows = JSON.parse(fs.readFileSync(path.join(directory, dataset.corpus.path)));
    for (const [index, row] of rows.entries()) {
      const id = row.idx === undefined ? `row:${index}` : String(row.idx);
      if (!wanted.delete(id)) continue;
      cases.push({
        id: `${dataset.id}:${id}`, content: `${row.title}\n${row.text}`, source: dataset.id,
        baseline: "initial_scan", candidate: "accepted",
      });
    }
    if (wanted.size) throw new Error("Pinned intake row is missing from the verified source");
  }
  if (cases.length !== 28) throw new Error("Pinned intake comparison must cover 28 source rows");
  return cases;
}

function exactEvidence(docker, runtime, receipt, content) {
  if (!/^[a-f0-9-]{36}$/.test(receipt.submission_id)) return false;
  const output = docker(["exec", "-i", `densemem-eval-${runtime.paths.runID}-postgres`,
    "psql", "-U", "densemem", "-d", "densemem", "-At", "-v", "ON_ERROR_STOP=1", "-v", `ingest=${receipt.submission_id}`,
  ], { input: "SELECT encode(convert_to(content, 'UTF8'), 'hex') FROM remember_submitted_items WHERE ingest_id = :'ingest'::uuid AND submitted_index = 0;\n" });
  return output === Buffer.from(content).toString("hex");
}

function outcome(receipt, docker, runtime, content) {
  if (receipt.processing_state === "completed" && receipt.search_state === "current") {
    const rejected = receipt.evidence?.some((item) => item.disposition === "not_stored");
    return !rejected && exactEvidence(docker, runtime, receipt, content) ? "accepted" : "evidence_mismatch";
  }
  const error = receipt.errors?.[0];
  if (error?.code === "submission_policy_rejected") {
    if (error.details?.component === "remember.initial_scan") return "initial_scan";
    if (error.details?.component === "remember.intake_review") return "intake_review";
  }
  return /^[a-z0-9_]{1,64}$/.test(error?.code || "") ? error.code : "missing_terminal_result";
}

export async function compareIntake(env, paths, { setup, cleanup, docker, downloadSources }) {
  requireRunner(env);
  const { model } = configuration(env);
  const remaining = Number(env.EVAL_MODEL_DEADLINE_EPOCH) * 1000 - Date.now();
  if (!Number.isFinite(remaining) || remaining <= 0 || remaining > 20 * 60_000) throw new Error("Invalid intake comparison deadline");
  const signal = AbortSignal.timeout(remaining);
  const bytes = fs.readFileSync("tests/eval/datasets.json");
  const registry = JSON.parse(bytes);
  registryMatrix(registry);
  const publicDir = path.join(paths.directory, "public");
  fs.mkdirSync(publicDir, { recursive: true, mode: 0o700 });
  const reportPath = path.join(publicDir, "intake-comparison.json");
  const report = {
    source_lock_sha256: sha256(bytes), source_revision: registry.revision, model,
    baseline: { ...baseline, results: [] },
    candidate: { results: [] },
    complete: false,
  };
  const save = () => fs.writeFileSync(reportPath, JSON.stringify(report, null, 2) + "\n", { mode: 0o600 });
  save();
  const cases = [...await lockedCases(registry, path.join(paths.directory, "data"), downloadSources, signal), ...controls()];
  for (const arm of ["baseline", "candidate"]) {
    const measurements = report[arm];
    const runtimeEnv = arm === "baseline" ? { ...env, EVAL_SOURCE_REVISION: baseline.revision, EVAL_IMAGE: baseline.image } : env;
    try {
      const runtime = await setup(runtimeEnv, { operation: "intake", signal });
      measurements.revision = runtime.manifest.source_revision;
      measurements.image = runtime.manifest.image;
      save();
      for (const [index, item] of cases.entries()) {
        signal.throwIfAborted();
        let actual;
        try {
          const receipt = await callTool(runtime.state.environment.EVAL_MCP_URL, runtime.state.environment.EVAL_API_KEY, "remember", {
            target_space: "team_shared", idempotency_key: `intake:${arm}:${index}`,
            evidence: [{ content: item.content, source_type: "document", source: item.source }],
          }, signal);
          actual = outcome(receipt, docker, runtime, item.content);
        } catch {
          actual = signal.aborted ? "deadline_expired" : "request_unavailable";
        }
        measurements.results.push({ case: item.id, content_sha256: sha256(item.content), expected: item[arm], actual });
        save();
      }
    } finally {
      await cleanup(runtimeEnv);
    }
  }
  report.complete = ["baseline", "candidate"].every((arm) => report[arm].results.length === cases.length && report[arm].results.every((result) => result.actual === result.expected));
  save();
  if (!report.complete) throw new Error("Pinned intake comparison did not meet its expected outcomes");
}
