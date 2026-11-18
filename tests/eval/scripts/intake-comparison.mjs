import fs from "node:fs";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { requireRunner, configuration, registryMatrix, sha256 } from "../../../.github/scripts/evaluation-policy.mjs";
import { callTool } from "../../uat/decision_providers/rpc.mjs";
import { securityIntakeCases } from "../../uat/security_intake_cases.mjs";

const baseline = {
  revision: "46d2c974b4720cb9783bda31ef4f78420518f3aa",
  image: "ghcr.io/markhuangai/dense-mem@sha256:4c93a73b2e9e3b6fcbc119528652c81e59a6ad25c6269695a3eb940dcf9da8d7",
};
const uuid = /^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/i;
const caseConcurrency = 4;

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

async function exactEvidence(docker, runtime, teamID, receipt, content, signal) {
  if (!uuid.test(teamID) || !uuid.test(receipt.submission_id)) return false;
  const output = await docker(["exec", "-i", `densemem-eval-${runtime.paths.runID}-postgres`,
    "psql", "-U", "densemem", "-d", "densemem", "-At", "-v", "ON_ERROR_STOP=1", "-v", `team=${teamID}`, "-v", `ingest=${receipt.submission_id}`,
  ], { signal, timeout: 30_000, input: "SELECT encode(convert_to(content, 'UTF8'), 'hex') FROM remember_submitted_items WHERE team_id = :'team'::uuid AND ingest_id = :'ingest'::uuid AND submitted_index = 0;\n" });
  return output === Buffer.from(content).toString("hex");
}

export function intakeReceiptOutcome({ receipt, isError }, content) {
  if (!isError && receipt.processing_state === "completed" && receipt.search_state === "current" && !receipt.errors?.length) {
    const partitions = receipt.evidence;
    if (!Array.isArray(partitions) || partitions.length === 0) return "evidence_mismatch";
    const runes = Array.from(content);
    const sourceItem = partitions[0]?.source_item_id;
    const occurrences = new Set();
    let start = 0;
    for (const [index, part] of partitions.entries()) {
      if (part?.disposition !== "stored" || part.evidence_index !== 0 || part.partition_index !== index ||
          part.span_start !== start || !Number.isInteger(part.span_end) || part.span_end <= start || part.span_end > runes.length ||
          part.source_item_id !== sourceItem || ![sourceItem, part.evidence_id, part.occurrence_id].every((id) => uuid.test(id)) ||
          occurrences.has(part.occurrence_id) || part.content_hash !== "sha256:" + sha256(runes.slice(start, part.span_end).join(""))) {
        return "evidence_mismatch";
      }
      occurrences.add(part.occurrence_id);
      start = part.span_end;
    }
    return start === runes.length ? "accepted" : "evidence_mismatch";
  }
  const error = receipt.errors?.[0];
  if (isError && receipt.processing_state === "failed" && error?.code === "submission_policy_rejected") {
    if (error.details?.component === "remember.initial_scan") return "initial_scan";
    if (error.details?.component === "remember.intake_review") return "intake_review";
  }
  return /^[a-z0-9_]{1,64}$/.test(error?.code || "") ? error.code : "missing_terminal_result";
}

export async function measureIntakeCases(cases, signal, measure, publish) {
  const stop = new AbortController();
  const workSignal = AbortSignal.any([signal, stop.signal]);
  const results = [];
  let next = 0;
  const worker = async () => {
    try {
      while (!workSignal.aborted && next < cases.length) {
        const index = next++;
        await measure(cases[index], index, workSignal, (result) => {
          results[index] = result;
          publish(results.filter(Boolean));
        });
      }
    } catch (error) {
      stop.abort(error);
    }
  };
  await Promise.all(Array.from({ length: Math.min(caseConcurrency, cases.length) }, worker));
  workSignal.throwIfAborted();
}

export async function compareIntake(env, paths, { setup, cleanup, control, mask, diagnostics, docker, downloadSources }) {
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
    source_lock_sha256: sha256(bytes), source_revision: registry.revision, model, case_concurrency: caseConcurrency,
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
    let runtime;
    try {
      runtime = await setup(runtimeEnv, { operation: "intake", signal });
      measurements.revision = runtime.manifest.source_revision;
      measurements.image = runtime.manifest.image;
      save();
      await measureIntakeCases(cases, signal, async (item, index, workSignal, publishCase) => {
        const correlationID = randomUUID();
        const result = { case: item.id, content_sha256: sha256(item.content), expected: item[arm] };
        let stage = "scope_setup";
        let apiKey, toolResult;
        try {
          const team = await control(runtime.state, "POST", "/teams", { name: `intake-case-${index}` }, workSignal);
          if (!uuid.test(team?.id)) throw new Error("Invalid disposable team identity");
          const credential = await control(runtime.state, "POST", `/teams/${team.id}/credentials`, {
            name: "intake comparison", role: "manager", scopes: ["read", "write"],
          }, workSignal);
          apiKey = credential?.api_key;
          if (typeof apiKey !== "string") throw new Error("Invalid disposable credential");
          mask(apiKey, runtimeEnv);
          stage = "remember";
          toolResult = await callTool(runtime.state.environment.EVAL_MCP_URL, apiKey, "remember", {
            target_space: "team_shared", idempotency_key: `intake:${arm}:${index}`,
            evidence: [{ content: item.content, source_type: "document", source: item.source }],
          }, { signal: workSignal, correlationID });
          result.actual = intakeReceiptOutcome(toolResult, item.content);
          if (result.actual === "accepted") {
            stage = "stored_evidence";
            if (!await exactEvidence(docker, runtime, team.id, toolResult.receipt, item.content, workSignal)) result.actual = "evidence_mismatch";
          }
        } catch (error) {
          result.actual = workSignal.aborted ? "deadline_expired" : "request_unavailable";
          const http = /^(?:Control operation failed with HTTP|MCP HTTP) ([1-5][0-9]{2})$/.exec(error?.message || "");
          if (http) result.http_status = Number(http[1]);
        }
        if (result.actual !== result.expected) {
          result.failure_stage = stage;
          result.correlation_id = correlationID;
          const failure = toolResult?.receipt?.errors?.[0];
          if (/^[a-z0-9_]{1,64}$/.test(failure?.reason_code || "")) result.reason_code = failure.reason_code;
          if (/^remember\.[a-z_]{1,40}$/.test(failure?.details?.component || "")) result.component = failure.details.component;
          result.protected_failure_log = "unavailable";
        }
        publishCase(result);
        if (result.actual !== result.expected && stage !== "scope_setup" && !workSignal.aborted) {
          try {
            if (await diagnostics(runtimeEnv, { correlationID, apiKey, signal: workSignal })) {
              publishCase({ ...result, protected_failure_log: "captured" });
            }
          } catch {
            process.stderr.write("Protected intake case log capture failed.\n");
          }
        }
      }, (results) => {
        measurements.results = results;
        save();
      });
    } catch (error) {
      if (!runtime) {
        measurements.failure = "runtime_setup_failed";
        save();
        process.stderr.write(`Intake comparison ${arm} runtime setup failed.\n`);
        try { await diagnostics(runtimeEnv); } catch { process.stderr.write("Protected intake startup log capture failed.\n"); }
      }
      throw error;
    } finally {
      await cleanup(runtimeEnv);
    }
  }
  report.complete = ["baseline", "candidate"].every((arm) => report[arm].results.length === cases.length && report[arm].results.every((result) => result.actual === result.expected));
  save();
  if (!report.complete) throw new Error("Pinned intake comparison did not meet its expected outcomes");
}
