"use strict";

const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const previewPolicy = require("./image-release-policy.cjs");

const UPSTREAM = "markhuangai/dense-mem";
const SIGNER_WORKFLOW = `${UPSTREAM}/.github/workflows/fork-e2e.yml`;
const REQUEST_WORKFLOW = ".github/workflows/fork-e2e-request.yml";
const WAIT_MILLISECONDS = 120 * 60 * 1000;
const POLL_MILLISECONDS = 3 * 60 * 1000;

function requireForkPull(pull, sourceSha) {
  if (!pull || pull.state !== "open" || pull.draft || pull.base?.ref !== "main" ||
      pull.base?.repo?.full_name !== UPSTREAM || pull.head?.sha !== sourceSha ||
      !/^[0-9a-f]{40}$/.test(sourceSha || "") ||
      !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(pull.head?.repo?.full_name || "") ||
      pull.head.repo.full_name.toLowerCase() === UPSTREAM || pull.head.repo.private !== false) {
    throw new Error("fork E2E requires the current head of an open public fork PR against upstream main");
  }
}

async function approvedForkPreview({ github, pullNumber, sourceSha, runId, runAttempt }) {
  const [owner, repo] = UPSTREAM.split("/");
  const { data: pull } = await github.rest.pulls.get({ owner, repo, pull_number: pullNumber });
  requireForkPull(pull, sourceSha);
  const statuses = await github.paginate(github.rest.repos.listCommitStatusesForRef, { owner, repo, ref: sourceSha, per_page: 100 });
  const latest = statuses.filter((value) => value.context === previewPolicy.POLICY_STATUS_CONTEXT).sort((a, b) => b.id - a.id)[0];
  if (latest?.creator?.login !== "github-actions[bot]") throw new Error("the preview status was not published by GitHub Actions");
  const parsed = previewPolicy.parseSuccessfulPolicyStatus({ statuses, pullNumber, serverUrl: "https://github.com", repository: UPSTREAM });
  if (!parsed.status || parsed.status.runId !== String(runId) || parsed.status.runAttempt !== String(runAttempt)) {
    throw new Error("the PR has no current administrator-approved preview for this run attempt");
  }
  const { data: run } = await github.rest.actions.getWorkflowRun({ owner, repo, run_id: Number(runId) });
  if (run.event !== "pull_request_target" || run.path !== ".github/workflows/pr-test-image.yml" ||
      run.run_attempt !== Number(runAttempt) || run.display_title !== `PR test image: PR #${pullNumber}`) {
    throw new Error("the preview was not produced by the current trusted controller attempt");
  }
  const jobs = await github.paginate("GET /repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}/jobs", {
    owner, repo, run_id: Number(runId), attempt_number: Number(runAttempt), per_page: 100,
  });
  for (const name of ["Publish trusted preview", "Report preview attempt"]) {
    if (jobs.filter((job) => job.name === name && job.conclusion === "success").length !== 1) {
      throw new Error(`the trusted preview job ${name} did not pass`);
    }
  }
  const comments = await github.paginate(github.rest.issues.listComments, { owner, repo, issue_number: pullNumber, per_page: 100 });
  const receipt = previewPolicy.selectPreviewReceipt({ comments, pull, status: parsed.status, repository: UPSTREAM });
  if (!Number.isFinite(Date.parse(latest.created_at))) throw new Error("the preview approval timestamp is unavailable");
  return { ...receipt, source_repository: pull.head.repo.full_name, approved_at: latest.created_at };
}

function requireForkRun(run, approved, { terminal = true } = {}) {
  if (run.repository?.full_name !== approved.source_repository || run.head_sha !== approved.source_sha ||
      run.event !== "workflow_dispatch" || run.path !== REQUEST_WORKFLOW ||
      !Number.isSafeInteger(run.id) || !Number.isSafeInteger(run.run_attempt) || run.run_attempt < 1 ||
      (terminal && (run.status !== "completed" || run.conclusion !== "success"))) {
    throw new Error("the fork run is missing, stale, failed, or does not test the approved PR head");
  }
  if (!Number.isFinite(Date.parse(run.run_started_at)) || Date.parse(run.run_started_at) < Date.parse(approved.approved_at)) {
    throw new Error("the fork run started before this preview was approved");
  }
  const workflows = (run.referenced_workflows || []).filter((workflow) => workflow.path.startsWith(`${SIGNER_WORKFLOW}@`));
  if (workflows.length !== 1 || workflows[0].sha !== approved.trusted_revision) {
    throw new Error("the fork run did not use the approved immutable upstream workflow revision");
  }
}

function successReceipt(approved, run, scenarios) {
  requireForkRun(run, approved, { terminal: false });
  if (!Array.isArray(scenarios) || scenarios.length !== 27 || new Set(scenarios).size !== 27 ||
      scenarios.some((name) => !/^[a-z0-9_]+$/.test(name))) throw new Error("fork E2E requires all 27 trusted scenarios");
  return {
    version: 1, upstream_repository: UPSTREAM, source_repository: approved.source_repository,
    pr_number: approved.pr_number, source_sha: approved.source_sha, image: approved.image,
    preview_run_id: String(approved.run_id), preview_run_attempt: String(approved.run_attempt),
    trusted_revision: approved.trusted_revision, fork_run_id: String(run.id), fork_run_attempt: String(run.run_attempt),
    rootless_controller: "success", postgres_prechecks: ["success", "success", "success"],
    scenarios: scenarios.map((name) => ({ name, result: "success", cleanup: "success" })),
  };
}

function receiptBytes(receipt) { return `${JSON.stringify(receipt)}\n`; }

function requiredForkJobs(scenarios) {
  return ["Fork rootless controller precheck", ...[0, 1, 2].map((shard) => `Fork PostgreSQL precheck shard ${shard}`),
    ...scenarios.map((name) => `Fork scenario ${name === "full" ? "dreaming_telemetry_portal" : name}`),
    "Sign fork E2E receipt"];
}

function requireCompleteForkJobs(jobs, scenarios, { includeSigner = true } = {}) {
  for (const expected of requiredForkJobs(scenarios)) {
    if (!includeSigner && expected === "Sign fork E2E receipt") continue;
    const matches = jobs.filter((job) => job.name === expected || job.name.endsWith(` / ${expected}`));
    if (matches.length !== 1 || matches[0].conclusion !== "success") throw new Error(`required fork job is missing, skipped, failed, or ambiguous: ${expected}`);
    if (expected.startsWith("Fork scenario ")) {
      for (const name of ["Run scenario", "Stop scenario stack"]) {
        const steps = (matches[0].steps || []).filter((step) => step.name === name);
        if (steps.length !== 1 || steps[0].conclusion !== "success") throw new Error(`required fork scenario execution or cleanup is missing, skipped, failed, or ambiguous: ${name}`);
      }
    }
  }
}

function attestationArguments(receiptFile, bundleFile, approved) {
  return ["attestation", "verify", receiptFile, "--bundle", bundleFile, "--repo", approved.source_repository,
    "--signer-workflow", SIGNER_WORKFLOW, "--signer-digest", approved.trusted_revision,
    "--source-digest", approved.source_sha, "--deny-self-hosted-runners", "--format", "json"];
}

async function publicGitHubJson(route) {
  if (!/^repos\/[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+\//.test(route)) throw new Error("invalid public fork API route");
  const response = await fetch(`https://api.github.com/${route}`, { headers: {
    Accept: "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "dense-mem-fork-e2e-verifier",
  }, signal: AbortSignal.timeout(30000), redirect: "error" });
  if (!response.ok) throw new Error(`public fork metadata request failed with HTTP ${response.status}`);
  const content = await response.text();
  if (Buffer.byteLength(content) > 4 * 1024 * 1024) throw new Error("public fork metadata exceeds the verifier bound");
  return JSON.parse(content);
}

async function verifyForkAttestation({ approved, run, scenarios, directory }) {
  requireForkRun(run, approved);
  const receipt = successReceipt(approved, run, scenarios);
  const content = receiptBytes(receipt);
  const digest = crypto.createHash("sha256").update(content).digest("hex");
  const attestations = await publicGitHubJson(`repos/${approved.source_repository}/attestations/sha256:${digest}`);
  if (!Array.isArray(attestations.attestations) || attestations.attestations.length < 1 || attestations.attestations.length > 30) {
    throw new Error("the fork did not publish a bounded signed receipt for the exact expected result");
  }
  const receiptFile = path.join(directory, "fork-e2e-receipt.json");
  const bundleFile = path.join(directory, "fork-e2e-bundles.jsonl");
  fs.writeFileSync(receiptFile, content, { mode: 0o600 });
  fs.writeFileSync(bundleFile, attestations.attestations.map((value) => JSON.stringify(value.bundle)).join("\n"), { mode: 0o600 });
  const verification = spawnSync("gh", attestationArguments(receiptFile, bundleFile, approved), {
    encoding: "utf8", timeout: 60000, maxBuffer: 1024 * 1024,
  });
  if (verification.error || verification.status !== 0) throw new Error("fork receipt signature, source, or trusted workflow verification failed");
  const result = JSON.parse(verification.stdout);
  if (!Array.isArray(result) || result.length < 1) throw new Error("fork receipt verification returned no verified attestation");
  return receipt;
}

async function waitForForkE2E({ github, approved, scenarios, directory, report }) {
  const deadline = Date.now() + WAIT_MILLISECONDS;
  while (Date.now() < deadline) {
    const current = await approvedForkPreview({ github, pullNumber: approved.pr_number, sourceSha: approved.source_sha,
      runId: approved.run_id, runAttempt: approved.run_attempt });
    if (receiptBytes(current) !== receiptBytes(approved)) throw new Error("the approved preview changed while waiting for fork E2E");
    const { workflow_runs: runs } = await publicGitHubJson(`repos/${approved.source_repository}/actions/runs?event=workflow_dispatch&head_sha=${approved.source_sha}&per_page=30`);
    for (const run of runs || []) {
      if (run.path !== REQUEST_WORKFLOW || run.status !== "completed") continue;
      if (Date.parse(run.run_started_at) < Date.parse(approved.approved_at)) continue;
      requireForkRun(run, approved);
      const { jobs } = await publicGitHubJson(`repos/${approved.source_repository}/actions/runs/${run.id}/attempts/${run.run_attempt}/jobs?per_page=100`);
      requireCompleteForkJobs(jobs || [], scenarios);
      const receipt = await verifyForkAttestation({ approved, run, scenarios, directory });
      const finalApproval = await approvedForkPreview({ github, pullNumber: approved.pr_number, sourceSha: approved.source_sha,
        runId: approved.run_id, runAttempt: approved.run_attempt });
      if (receiptBytes(finalApproval) !== receiptBytes(approved)) throw new Error("the approved preview changed during receipt verification");
      return { receipt, url: `https://github.com/${approved.source_repository}/actions/runs/${run.id}/attempts/${run.run_attempt}` };
    }
    report("Waiting for the contributor to complete the approved fork run; upstream credentials remain withheld.");
    await new Promise((resolve) => setTimeout(resolve, Math.min(POLL_MILLISECONDS, deadline - Date.now())));
  }
  throw new Error("no verified current-head fork E2E receipt arrived within 120 minutes; start the approved fork workflow and request fresh PR validation");
}

module.exports = { UPSTREAM, SIGNER_WORKFLOW, REQUEST_WORKFLOW, requireForkPull, approvedForkPreview, requireForkRun,
  successReceipt, receiptBytes, requiredForkJobs, requireCompleteForkJobs, attestationArguments, verifyForkAttestation, waitForForkE2E };
