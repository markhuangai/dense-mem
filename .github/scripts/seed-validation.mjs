import imagePolicy from "./image-release-policy.cjs";

const repository = "markhuangai/dense-mem";
const target = (id) => `https://github.com/${repository}/actions/runs/${id}`;

export async function seedGitHubPages(github, endpoint, field) {
  const items = [];
  for (let page = 1; ; page++) {
    const { data } = await github.request(`GET ${endpoint}`, { per_page: 100, page });
    const batch = field ? data[field] : data;
    items.push(...batch);
    if (batch.length < 100) return items;
  }
}

async function validationRun(github, run) {
  if (run.conclusion && run.conclusion !== "success") throw new Error("Seed validation failed or was cancelled");
  const jobs = await seedGitHubPages(github, `/repos/${repository}/actions/runs/${run.id}/attempts/${run.run_attempt}/jobs`, "jobs");
  if (jobs.some((job) => job.conclusion && !["success", "skipped", "neutral"].includes(job.conclusion))) throw new Error("Seed validation job failed or was cancelled");
  return { jobs, pending: run.status !== "completed" || run.conclusion !== "success" };
}

function isRepositoryCIRun(run, revision) {
  return run.head_sha === revision && run.path === ".github/workflows/ci-pr.yml" && ["pull_request", "pull_request_target"].includes(run.event);
}

async function repositoryCIPending(github, pull, proof, status, revision) {
  const automatic = imagePolicy.runsAutomaticRepositoryCI(pull, repository);
  if (status.target_url === target(proof.run_id)) {
    if (!automatic) return status.state === "pending";
    if (status.state !== "pending") throw new Error("Repository CI status uses the wrong workflow producer");
    const { data } = await github.request(`GET /repos/${repository}/actions/workflows/ci-pr.yml/runs`, { head_sha: revision, per_page: 100 });
    for (const run of data.workflow_runs.sort((a, b) => b.id - a.id)) {
      if (!isRepositoryCIRun(run, revision) || run.conclusion === "skipped") continue;
      const current = await validationRun(github, run);
      if (current.jobs.some((job) => job.name.startsWith("CI /") && job.conclusion !== "skipped")) return true;
    }
    throw new Error("Current-head automatic Repository CI is unavailable");
  }
  const match = new RegExp(`^https://github\\.com/${repository}/actions/runs/([1-9][0-9]*)$`).exec(status.target_url || "");
  if (!match) throw new Error("Repository CI status source is invalid");
  const { data: run } = await github.request(`GET /repos/${repository}/actions/runs/${match[1]}`);
  if (!isRepositoryCIRun(run, revision)) throw new Error("Repository CI workflow or source revision differs");
  const current = await validationRun(github, run);
  if (!current.jobs.some((job) => job.name.startsWith("CI /") && job.conclusion !== "skipped")) throw new Error("Repository CI did not execute its required jobs");
  return current.pending || status.state !== "success";
}

export async function seedValidationState(github, proof) {
  if (!/^[0-9a-f]{40}$/.test(proof.source_revision || "")) throw new Error("Seed validation identity is invalid");
  let pending = false;
  const revision = proof.source_revision;
  if (proof.main_revision) {
    const { data: main } = await github.request(`GET /repos/${repository}/branches/main`);
    if (main.commit.sha !== proof.main_revision) throw new Error("Seed checkout no longer matches current main");
    const { data } = await github.request(`GET /repos/${repository}/actions/workflows/ci-push.yml/runs`, { head_sha: proof.main_revision, branch: "main", per_page: 1 });
    const run = data.workflow_runs[0];
    if (!run || run.head_sha !== proof.main_revision || run.path !== ".github/workflows/ci-push.yml" || run.event !== "push") throw new Error("Seed main CI is missing");
    if ((await validationRun(github, run)).pending) pending = true;
  }
  if (!proof.pull_number) throw new Error("Seed validation PR receipt is missing");
  const { data: pull } = await github.request(`GET /repos/${repository}/pulls/${proof.pull_number}`);
  const expectedState = proof.main_revision ? pull.merged === true && pull.merge_commit_sha === proof.release_revision : pull.state === "open";
  if (!expectedState || pull.draft || pull.base.ref !== "main" || pull.head.sha !== revision || pull.head.repo?.full_name !== repository || pull.user.login !== proof.actor) throw new Error("Seed validation PR head or ownership changed");
  const { data: run } = await github.request(`GET /repos/${repository}/actions/runs/${proof.run_id}`);
  if (run.head_sha !== revision || run.run_attempt !== proof.validation_attempt || run.event !== "pull_request_target" || run.path !== ".github/workflows/pr-test-image.yml" || run.display_title !== `PR test image: PR #${proof.pull_number}`) throw new Error("Seed validation run identity changed");
  if ((await validationRun(github, run)).pending) pending = true;
  const statuses = await seedGitHubPages(github, `/repos/${repository}/commits/${revision}/statuses`);
  for (const name of ["PR test image policy", "Repository CI", "Production image E2E"]) {
    const status = statuses.filter((item) => item.context === name).sort((a, b) => b.id - a.id)[0];
    if (!status || status.creator?.login !== "github-actions[bot]" || !["success", "pending"].includes(status.state)) throw new Error("Seed validation status is missing, stale or failed");
    if (name === "Repository CI") {
      if (await repositoryCIPending(github, pull, proof, status, revision)) pending = true;
      continue;
    }
    if (status.target_url !== target(proof.run_id)) throw new Error("Seed validation status uses a different publisher run");
    if (name === "PR test image policy") {
      if (status.state !== "success" || status.description !== `Published test-${proof.pull_number} (run ${proof.run_id}, attempt ${proof.publisher_attempt}).`) throw new Error("Seed publisher receipt changed");
    } else if (status.state !== "success") pending = true;
  }
  const checks = await seedGitHubPages(github, `/repos/${repository}/commits/${revision}/check-runs`, "check_runs");
  for (const name of ["CodeQL analysis", "CodeQL"]) {
    const check = checks.filter((item) => item.name === name).sort((a, b) => b.id - a.id)[0];
    const app = name === "CodeQL" ? "github-advanced-security" : "github-actions";
    if (!check || check.head_sha !== revision || check.app?.slug !== app || check.status !== "completed" || check.conclusion !== "success") throw new Error("Seed building requires successful current-head CodeQL checks");
  }
  return pending ? "pending" : "success";
}

export async function ontologyFixtureValidated(github, proof) {
  const response = await github.request(`GET /repos/${repository}/actions/workflows/production-image-e2e.yml/runs`,
    { head_sha: proof.source_revision, event: "workflow_dispatch", per_page: 100 });
  for (const run of response.data.workflow_runs) {
    if (run.head_sha !== proof.source_revision || run.event !== "workflow_dispatch" || Number(run.run_attempt) !== 1 ||
        run.actor?.login !== proof.actor || run.path !== ".github/workflows/production-image-e2e.yml" || run.conclusion !== "success") continue;
    const jobs = await seedGitHubPages(github, `/repos/${repository}/actions/runs/${run.id}/attempts/1/jobs`, "jobs");
    const synthetic = jobs.find((job) => job.name === "Public RAG evaluation / Prepare and score synthetic pair");
    const aggregate = jobs.find((job) => job.name === "Public RAG evaluation / Aggregate selected dataset outcomes");
    if (synthetic?.conclusion === "success" && aggregate?.conclusion === "success" &&
        synthetic.steps?.some((step) => step.name === "Build and score frozen synthetic pair" && step.conclusion === "success")) return run.id;
  }
  return null;
}
