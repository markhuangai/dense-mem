// Materialized alongside the pinned native seed builder. This entry point never exports a seed.
import { createHash, randomBytes } from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { createInterface } from "node:readline";
import { localSeedEnvironment } from "./local-seeds.mjs";
import { cleanup, docker, location, runBinary, saveRuntimeJSON, setup } from "./workflow.mjs";

const owner = "io.dense-mem.eval.run";
const base = "214fee2f8d3ac53333857888dcdd2ce56a367fa9";
const canonical = "4596792fdf681b7e000b6b3fccad137fd0050efb4c28ede58f4bd08e0d92599a";
const dataSHA = "9c9653a9e5cb490bd074fbda44a97dcaea858f8baeff6836f7364760aa6ea9b3";
const parent = "ghcr.io/markhuangai/dense-mem-eval-postgres@sha256:bbb0c68334a97374cf6498de327028463b4cf619b736e084b290c4cc7df3edaf";
const copyImage = "sha256:f6a95d611e0a8780fabdb2aec43e31275b2720dfefa3e41b16835bfc42454478";
const repository = "markhuangai/dense-mem";
const read = (filename) => JSON.parse(fs.readFileSync(filename, "utf8"));

function requireTrial(condition, message) {
  if (!condition) throw new Error(message);
}

function verifiedGate(run, identity) {
  requireTrial(/^[1-9][0-9]+$/.test(run), "Invalid hosted study run identity");
  const remote = JSON.parse(execFileSync("gh", ["run", "view", run, "--repo", repository,
    "--json", "headSha,conclusion,event,workflowName,attempt,url"], { encoding: "utf8", timeout: 15_000 }));
  requireTrial(remote.conclusion === "success" &&
    remote.event === "push" && remote.workflowName === "Ontology receipt recheck study" && remote.attempt === 1 &&
    remote.headSha === identity.study_source, "Hosted ontology study run does not match the materialized source");
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "dense-mem-receipt-decision-"));
  let proof;
  try {
    execFileSync("gh", ["run", "download", run, "--repo", repository,
      "--name", "ontology-receipt-study-decision", "--dir", directory], { timeout: 60_000 });
    proof = read(path.join(directory, "comparison.json"));
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
  requireTrial(proof.evidence_complete === true && proof.advance === true && proof.study_source === identity.study_source &&
    /^[a-f0-9]{64}$/.test(identity.runtime_patch_sha256) && /^[a-f0-9]{64}$/.test(identity.migration_sha256) &&
    proof.base_source === base && proof.runtime_patch_sha256 === identity.runtime_patch_sha256 &&
    proof.migration_sha256 === identity.migration_sha256,
  "Hosted ontology study receipt has not qualified this candidate");
  return remote.url;
}

function verifyStudyImage(image, identity) {
  const [inspected] = JSON.parse(docker(["image", "inspect", image]));
  requireTrial(inspected.Id === image && inspected.Config.Labels?.["io.dense-mem.ontology-receipt-study"] === identity.runtime_patch_sha256,
    "Private trial image and tested runtime patch differ");
  const installedMigration = docker(["run", "--rm", "--pull=never", "--network=none", "--entrypoint", "sha256sum", image,
    "/app/migrations/postgres/v2_8/20991123010000_receipt_recheck_study.sql"], { timeout: 60_000 });
  requireTrial(installedMigration.split(/\s+/, 1)[0] === identity.migration_sha256,
    "Private trial image and tested migration differ");
}

function ownerOf(name) {
  return docker(["volume", "inspect", "--format", `{{index .Labels "${owner}"}}`, name]);
}

function createPrivateCopy(transfer, target, run) {
  const original = transfer.original_volume;
  requireTrial(/^local-[a-f0-9]{32}$/.test(transfer.original_id) &&
    original === `densemem-eval-${transfer.original_id}-hotpotqa-postgres` &&
    transfer.parent_image === parent && transfer.copied_files_sha256 === dataSHA &&
    transfer.drained?.canonical_sha256 === canonical && transfer.drained?.evidence === 13810 &&
    transfer.drained?.counts?.organized === 3295 && transfer.drained?.paused === true,
  "Inactive HotpotQA transfer identity differs from the archived baseline");
  requireTrial(ownerOf(original) === `${transfer.original_id}-hotpotqa` &&
    !docker(["ps", "-aq", "--filter", `volume=${original}`]), "The archived checkpoint is in use or its owner differs");
  const volume = `densemem-eval-${run}-postgres`;
  docker(["volume", "create", "--label", `${owner}=${run}`, volume]);
  requireTrial(ownerOf(volume) === run, "The copied checkpoint owner differs");
  const digests = docker(["run", "--rm", "--pull=never", "--network=none", "--label", `${owner}=${run}`,
    "--mount", `type=volume,source=${original},target=/previous,readonly,volume-nocopy`,
    "--mount", `type=volume,source=${volume},target=/clone,volume-nocopy`,
    copyImage, "sh", "-ec",
    "set -o pipefail; cp -a /previous/. /clone/; test -s /clone/18/docker/PG_VERSION; test ! -e /clone/18/docker/postmaster.pid; for dir in /previous /clone; do (cd \"$dir\" && find . -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum); done"],
  { timeout: 600_000 }).split("\n").map((line) => line.split(/\s+/)[0]);
  requireTrial(digests.length === 2 && digests.every((hash) => hash === dataSHA) &&
    !docker(["ps", "-aq", "--filter", `volume=${original}`]), "HotpotQA copy and original have different archived file sets");
  saveRuntimeJSON(path.join(target, "receipt-trial-copy.json"), { original, volume, run, data_sha256: dataSHA, canonical_sha256: canonical });
  return volume;
}

function resourceSample(run, filename) {
  try {
    const rows = docker(["ps", "--filter", `label=${owner}`, "--format", "{{.ID}} {{.Names}}"])
      .split("\n").map((line) => line.split(" ")).filter(([id, name]) => id && /-(server|postgres)$/.test(name));
    const numeric = rows.length ? docker(["stats", "--no-stream", "--format",
      "{{.Name}} {{.CPUPerc}} {{.MemUsage}}", ...rows.map(([id]) => id)], { timeout: 15_000 }) : "";
    fs.appendFileSync(filename, JSON.stringify({ run, observed: new Date().toISOString(), runtime_load: numeric }) + "\n", { mode: 0o600 });
  } catch {
    fs.appendFileSync(filename, JSON.stringify({ run, observed: new Date().toISOString(), runtime_load: null }) + "\n", { mode: 0o600 });
  }
}

async function recordRuntimeLog(trial, result) {
  const container = `densemem-eval-${trial.runID}-server`;
  const identity = docker(["container", "inspect", "--format", `{{index .Config.Labels "${owner}"}}`, container]);
  requireTrial(identity === trial.runID, "Trial server owner changed before log collection");
  const filename = path.join(trial.directory, "receipt-trial-server.log");
  const descriptor = fs.openSync(filename, "wx", 0o600);
  try {
    execFileSync("docker", ["logs", container], { stdio: ["ignore", descriptor, descriptor], timeout: 45_000 });
  } finally {
    fs.closeSync(descriptor);
  }
  const attempts = new Map(), holds = [];
  let receipts = 0, backfillChecks = 0, backfillMS = 0, discoveryMS = 0, discoveries = 0, backfills = 0;
  const from = Date.parse(result.Started), until = Date.parse(result.MeasuredEnd);
  const window = Number.isFinite(from) && Number.isFinite(until) ? "complete" : "partial";
  for await (const line of createInterface({ input: fs.createReadStream(filename), crlfDelay: Infinity })) {
    let event;
    try { event = JSON.parse(line); } catch { continue; }
    const timestamp = Date.parse(event.started_at || event.time || "");
    if (!Number.isFinite(timestamp) || (Number.isFinite(from) && timestamp < from) || (Number.isFinite(until) && timestamp > until)) continue;
    if (event.msg === "ai_exchange_payload" && event.direction === "response" &&
        event.batch_id && event.exchange_id && Number.isFinite(event.elapsed_ms)) {
      attempts.set(event.exchange_id, event.elapsed_ms);
    }
    if (event.msg === "ontology preparation rate limited" && Number.isFinite(Date.parse(event.hold_until))) {
      holds.push({ observed_at: event.time, until: event.hold_until, batch_id: event.batch_id });
    }
    if (event.msg === "ontology maintenance discovery" && event.committed &&
        Number.isSafeInteger(event.freshness_invocations) && Number.isFinite(event.duration_ms)) {
      receipts += event.freshness_invocations;
      backfillChecks += event.study_backfill_checks || 0;
      discoveryMS += event.duration_ms;
      discoveries++;
    }
    if (event.msg === "ontology receipt study backfill" && event.committed && Number.isFinite(event.duration_ms)) {
      backfillMS += event.duration_ms;
      backfills++;
    }
  }
  const hash = createHash("sha256");
  for await (const bytes of fs.createReadStream(filename)) hash.update(bytes);
  const span = [...attempts.values()].sort((left, right) => left - right);
  saveRuntimeJSON(path.join(trial.directory, "receipt-trial-telemetry.json"), {
    log_sha256: hash.digest("hex"), measured_window: window,
    provider_http_exchanges: span.length,
    provider_http_p50_ms: span.length ? span[Math.ceil(span.length / 2) - 1] : null,
    provider_hold_events: holds,
    full_receipt_checks: discoveries ? receipts + backfillChecks : null,
    discovery_receipt_checks: discoveries ? receipts : null,
    backfill_receipt_checks: discoveries ? backfillChecks : null,
    discovery_ms: discoveries ? discoveryMS : null,
    committed_backfill_ms: backfills ? backfillMS : null,
    committed_backfill_pages: backfills,
  });
}

async function main([run, transferPath, checkpointPath, output]) {
  const start = Date.now();
  requireTrial([transferPath, checkpointPath, output].every((file) => file && path.isAbsolute(file)), "Use absolute private study paths");
  const identity = read("ontology-receipt-study-identity.json");
  requireTrial(identity.mode === "private", "Materialize the private study checkout");
  const url = verifiedGate(run, identity);
  const env = {
    ...localSeedEnvironment(process.env, 8), EVAL_DATASET: "hotpotqa",
    EVAL_LOCAL_SEED_ID: "local-" + randomBytes(16).toString("hex"),
    EVAL_LOCAL_SEED_ROOT: output,
    EVAL_SOURCE_REVISION: base,
    EVAL_MODEL_DEADLINE_EPOCH: String(Math.floor((start + 100 * 60_000) / 1000)),
    ONTOLOGY_RECEIPT_PRIVATE_TRIAL: "1",
  };
  requireTrial(/^sha256:[a-f0-9]{64}$/.test(env.EVAL_IMAGE || "") && path.isAbsolute(env.EVAL_RUNNER_BINARY || ""),
    "Use the local immutable receipt-study image and runner");
  const trial = location(env);
  const checkpoint = read(checkpointPath);
  const transfer = read(transferPath);
  requireTrial(checkpoint.volume === transfer.original_volume && checkpoint.bootstrap_image === parent &&
    checkpoint.run_id === `${transfer.original_id}-hotpotqa` && checkpoint.scope?.generation > 0,
  "The archived build checkpoint does not match its transfer");

  fs.mkdirSync(output, { mode: 0o700, recursive: true });
  requireTrial((fs.statSync(output).mode & 0o077) === 0, "Trial output directory must be private");
  fs.mkdirSync(trial.directory, { mode: 0o700 });
  saveRuntimeJSON(path.join(trial.directory, "receipt-trial-lineage.json"), {
    ...identity, hosted_proof: url, candidate_image: env.EVAL_IMAGE, parent, model_deadline: env.EVAL_MODEL_DEADLINE_EPOCH,
    historical_runtime_source: base, historical_net_organized: 201, historical_elapsed_seconds: 3597.73,
  });
  const controller = new AbortController();
  const stop = () => controller.abort(new Error("Private study deadline or interruption"));
  const remaining = setTimeout(stop, Math.max(1, start + 120 * 60_000 - Date.now()));
  process.on("SIGINT", stop);
  process.on("SIGTERM", stop);
  let sampler, runtime, trialError, result, ready;
  try {
    verifyStudyImage(env.EVAL_IMAGE, identity);
    const volume = createPrivateCopy(transfer, trial.directory, trial.runID);
    saveRuntimeJSON(trial.checkpointFile, {
      version: 1, run_id: trial.runID, volume, source_revision: base, image: env.EVAL_IMAGE,
      registry_sha256: checkpoint.registry_sha256, bootstrap_image: parent, scope: checkpoint.scope,
      skip_ai_security_review: false, operation: "bake-ontology",
      model_deadline: new Date(Number(env.EVAL_MODEL_DEADLINE_EPOCH) * 1000).toISOString(),
    });
    runtime = await setup(env, {
      operation: "bake-ontology", seedImage: parent, resumable: true, receiptTrial: true, signal: controller.signal,
    });
    const samples = path.join(trial.directory, "receipt-trial-load.ndjson");
    resourceSample(trial.runID, samples);
    sampler = setInterval(() => resourceSample(trial.runID, samples), 5 * 60_000);
    ready = Date.now();
    await runBinary("bake-ontology", runtime, env, controller.signal);
    result = read(path.join(trial.directory, "receipt-trial-result.json"));
    requireTrial(result.Drained && result.Canonical === canonical && result.Image === env.EVAL_IMAGE &&
      result.RunID === trial.runID && Date.parse(result.MeasuredEnd) - Date.parse(result.Started) >= 3600_000 &&
      Date.now() - start < 120 * 60_000, "Private trial timing or drain proof is incomplete");
  } catch (error) {
    trialError = error;
  } finally {
    clearInterval(sampler);
    clearTimeout(remaining);
    process.off("SIGINT", stop);
    process.off("SIGTERM", stop);
    if (fs.existsSync(trial.stateFile)) {
      try {
        let capture = "complete";
        try {
          const partial = path.join(trial.directory, "receipt-trial-result.json");
          await recordRuntimeLog(trial, result || (fs.existsSync(partial) ? read(partial) : {}));
        } catch (error) {
          capture = "unavailable";
          trialError ||= error;
        }
        saveRuntimeJSON(path.join(trial.directory, "receipt-trial-capture.json"), {
          run_id: trial.runID, outcome: capture,
        });
        if (runtime && ready && result) {
          saveRuntimeJSON(path.join(trial.directory, "receipt-trial-setup.json"), {
            setup_ms: ready - start, native_validation_ms: Date.parse(result.Started) - ready,
            migration_ms: runtime.manifest.timings_ms.migration, readiness_ms: runtime.manifest.timings_ms.readiness,
          });
        }
      } catch (error) {
        process.stderr.write("Private trial could not save its diagnostic receipts.\n");
        trialError ||= error;
      } finally {
        let complete = false;
        try {
          await cleanup(env, { retainVolume: fs.existsSync(trial.checkpointFile) });
          complete = true;
        } catch (error) {
          process.stderr.write("Private trial cleanup failed.\n");
          trialError ||= error;
        }
        try {
          saveRuntimeJSON(path.join(trial.directory, "receipt-trial-cleanup.json"), { run_id: trial.runID, complete });
        } catch (error) {
          process.stderr.write("Private trial could not save its cleanup receipt.\n");
          trialError ||= error;
        }
      }
    }
  }
  if (trialError) throw trialError;
  requireTrial(Date.now() - start < 120 * 60_000, "Trial exceeded its two-hour total budget");
  process.stdout.write(JSON.stringify({ event: "receipt_trial_finished", directory: trial.directory,
    net_organized: result.NetOrganized, baseline: 201, elapsed_ms: Date.now() - start }) + "\n");
}

main(process.argv.slice(2)).catch((error) => {
  const safe = /^([A-Z][a-z]+ )*(trial|checkpoint|archived|HotpotQA|Hosted|[Ii]nvalid|The |Use |Materialize)/.test(error.message);
  process.stderr.write((safe ? error.message : "Private HotpotQA study failed. See the protected trial directory.") + "\n");
  process.exitCode = 1;
});
