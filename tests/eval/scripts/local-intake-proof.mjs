import fs from "node:fs";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { spawnSync } from "node:child_process";
import { parseArgs } from "node:util";
import { registryMatrix, requireEvaluationImportSupport } from "../../../.github/scripts/evaluation-policy.mjs";
import { resolveSeedImage, seedReference } from "../../../.github/scripts/evaluation-seeds.mjs";
import { docker, downloadSources, setup, runBinary } from "./workflow.mjs";
import { localSeedEnvironment, prepareSeedExport, withOwnedRuntime } from "./local-seeds.mjs";

const read = (file) => JSON.parse(fs.readFileSync(file, "utf8"));
const save = (file, value) => fs.writeFileSync(file, JSON.stringify(value, null, 2) + "\n", { mode: 0o600 });
const localID = () => "local-" + randomBytes(16).toString("hex");

async function main(args) {
  const { values } = parseArgs({ args, options: { image: { type: "string" }, runner: { type: "string" }, "source-cache": { type: "string" } } });
  const env = localSeedEnvironment(process.env, 4);
  if (!/^sha256:[a-f0-9]{64}$/.test(values.image || "") || !values.runner) throw new Error("Intake proof requires the local production image ID and the working tree's eval-runner binary");
  const [image] = JSON.parse(docker(["image", "inspect", values.image]));
  requireEvaluationImportSupport(image, { skipAISecurityReview: true });
  const labels = image.Config.Labels;
  const sourceRevision = labels["org.opencontainers.image.revision"];
  const sourceFingerprint = labels["io.dense-mem.eval.source-sha256"];
  if (image.Id !== values.image || image.Os !== "linux" || image.Architecture !== "amd64" || labels["org.opencontainers.image.variant"] !== "production" || labels["org.opencontainers.image.source"] !== "https://github.com/markhuangai/dense-mem" || !/^[a-f0-9]{40}$/.test(sourceRevision || "") || !/^[a-f0-9]{64}$/.test(sourceFingerprint || "")) throw new Error("Local intake proof image is missing the Dense-Mem source identity");

  const root = path.resolve("tmp/eval-seeds", "intake-proof-" + localID());
  const dataDirectory = path.join(root, "data");
  fs.mkdirSync(root, { recursive: true, mode: 0o700 });
  fs.mkdirSync(dataDirectory, { mode: 0o700 });
  process.stdout.write(`Intake proof receipts: ${path.relative(process.cwd(), root)}\n`);
  const registry = read("tests/eval/datasets.json");
  registryMatrix(registry);
  const runner = path.resolve(values.runner);
  const controller = new AbortController();
  const stop = () => controller.abort();
  for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, stop);
  try {
    for (const dataset of registry.datasets) {
      if (values["source-cache"]) {
        for (const source of [dataset.corpus, dataset.questions]) {
          fs.copyFileSync(path.join(values["source-cache"], source.path), path.join(dataDirectory, source.path));
        }
      }
      await downloadSources(registry, dataset, dataDirectory, controller.signal);
    }
    const frozen = spawnSync(runner, ["freeze-intake-proof", dataDirectory], { stdio: "inherit", timeout: 180_000 });
    if (frozen.status !== 0) throw new Error("Could not freeze the complete local intake proof inputs");
    const bootstrap = await resolveSeedImage(seedReference("bootstrap"), {});
    if (!bootstrap) throw new Error("Local intake proof requires the published evaluation bootstrap");
    const report = { image_id: image.Id, source_revision: sourceRevision, source_sha256: sourceFingerprint, bootstrap, inputs: read(path.join(dataDirectory, "intake-proof-inputs.json")), ordinary: [], seed: [], complete: false };
    const writeReport = () => save(path.join(root, "comparison.json"), report);
    writeReport();
    const signal = controller.signal;
    const options = { intakeProof: true, dataDirectory, signal };
    for (const skipAISecurityReview of [false, true]) {
      const armID = localID();
      for (const dataset of registry.datasets) {
        signal.throwIfAborted();
        const runtimeEnv = { ...env, EVAL_DATASET: dataset.id, EVAL_SOURCE_REVISION: sourceRevision, EVAL_IMAGE: image.Id, EVAL_SEED_IMAGE: bootstrap, EVAL_LOCAL_SEED_ID: armID, EVAL_LOCAL_SEED_ROOT: root, EVAL_RUNNER_BINARY: runner, EVAL_MODEL_DEADLINE_EPOCH: String(Math.floor(Date.now() / 1000) + 20 * 60) };
        await withOwnedRuntime(runtimeEnv, async () => {
          if (skipAISecurityReview) {
            const { seed } = await prepareSeedExport(runtimeEnv, signal, { ...options, skipAISecurityReview });
            report.seed.push({ dataset: dataset.id, imported: seed.corpus_rows, restored: seed.corpus_rows, canonical_sha256: seed.canonical_sha256, search_sha256: seed.search_sha256, ai_security_review: seed.ai_security_review });
          } else {
            const runtime = await setup(runtimeEnv, { ...options, operation: "prepare" });
            await runBinary("prepare", runtime, runtimeEnv, signal);
            report.ordinary.push(read(path.join(runtime.manifest.output_dir, "ordinary-intake.json")));
          }
          writeReport();
        });
      }
    }
    const ordinary = report.ordinary.reduce((count, dataset) => count + dataset.outcomes.length, 0);
    const imported = report.seed.reduce((count, dataset) => count + dataset.imported, 0);
    const restored = report.seed.reduce((count, dataset) => count + dataset.restored, 0);
    report.complete = ordinary === 100 && imported === 100 && restored === 100;
    writeReport();
    if (!report.complete) throw new Error("Local intake proof is incomplete");
    process.stdout.write(JSON.stringify({ event: "intake_proof_complete", ordinary, imported, restored }) + "\n");
  } finally {
    for (const signal of ["SIGINT", "SIGTERM"]) process.removeListener(signal, stop);
  }
}

main(process.argv.slice(2)).catch((error) => {
  process.exitCode = 1;
  const known = /^(Missing local seed settings:|Invalid Actions setting:|Intake proof requires|Local intake proof|Could not freeze)/.test(error.message);
  process.stderr.write((known ? error.message : "Local intake proof failed. Inspect the protected logs and the private comparison receipt.") + "\n");
});
