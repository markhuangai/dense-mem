"use strict";

const PREVIEW_LABEL = "deploy-test-image";
const POLICY_STATUS_CONTEXT = "PR test image policy";
const TRUSTED_LABEL_PERMISSIONS = new Set(["admin", "maintain", "write"]);
const PRODUCTION_E2E_ACTOR = "Z-M-Huang";

function normalizeLabelName(label) {
  return typeof label === "string" ? label : label?.name;
}

function hasLabel(labels, name) {
  return labels.some((label) => normalizeLabelName(label) === name);
}

function validateProductionImageReference(image, repository) {
  if (typeof repository !== "string" || repository.trim() !== repository || repository.length === 0) {
    return { valid: false, reason: "the image repository is invalid" };
  }
  const expected = `ghcr.io/${repository.toLowerCase()}`;
  if (typeof image !== "string" || image.trim() !== image || image.length === 0) {
    return { valid: false, reason: "the image reference is empty or contains whitespace" };
  }
  if (!image.startsWith(`${expected}:`) && !image.startsWith(`${expected}@`)) {
    return { valid: false, reason: "the image is outside the Dense-Mem GHCR repository" };
  }
  const tag = image.slice(expected.length + 1);
  if (image.startsWith(`${expected}:`) && !/^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(tag)) {
    return { valid: false, reason: "the image tag is invalid" };
  }
  if (image.startsWith(`${expected}@`) && !/^sha256:[0-9a-f]{64}$/.test(tag)) {
    return { valid: false, reason: "the image digest is invalid" };
  }
  return { valid: true, repository: expected, reference: image };
}

function decideManualProductionE2E({ actor, triggeringActor, ref, image, repository }) {
  const imageDecision = validateProductionImageReference(image, repository);
  if (!imageDecision.valid) return { authorized: false, reason: imageDecision.reason };
  if (actor !== PRODUCTION_E2E_ACTOR || triggeringActor !== PRODUCTION_E2E_ACTOR) {
    return { authorized: false, reason: "manual production E2E is restricted to the owner" };
  }
  if (ref !== "refs/heads/main") {
    return { authorized: false, reason: "manual production E2E must use the main workflow definition" };
  }
  return { authorized: true, reason: "", ...imageDecision };
}

function decideAutomaticProductionE2E({
  actor,
  pullRequestAuthor,
  pullRequestNumber,
  pullRequestState,
  pullRequestBase,
  hasPreviewLabel,
  currentHead,
  expectedHead,
  currentMain,
  expectedMain,
  previewRunId,
  previewRunAttempt,
  workflowRun,
  publishJob,
  image,
  repository,
}) {
  const imageDecision = validateProductionImageReference(image, repository);
  if (!imageDecision.valid) return { authorized: false, reason: imageDecision.reason };
  if (actor !== PRODUCTION_E2E_ACTOR || pullRequestAuthor !== PRODUCTION_E2E_ACTOR) {
    return { authorized: false, reason: "automatic production E2E is restricted to the owner PR" };
  }
  if (pullRequestState !== "open" || pullRequestBase !== "main") {
    return { authorized: false, reason: "the pull request is not open against main" };
  }
  if (!hasPreviewLabel) return { authorized: false, reason: "the preview label is absent" };
  if (currentHead !== expectedHead || currentMain !== expectedMain) {
    return { authorized: false, reason: "the preview or main revision advanced" };
  }
  if (!/^[1-9][0-9]*$/.test(String(previewRunId)) || !/^[1-9][0-9]*$/.test(String(previewRunAttempt))) {
    return { authorized: false, reason: "the preview workflow receipt is invalid" };
  }
  if (
    !workflowRun ||
    workflowRun.id !== Number(previewRunId) ||
    workflowRun.run_attempt !== Number(previewRunAttempt) ||
    workflowRun.head_sha !== expectedHead ||
    workflowRun.event !== "pull_request_target" ||
    workflowRun.path !== ".github/workflows/pr-test-image.yml" ||
    workflowRun.display_title !== `PR test image: PR #${pullRequestNumber}`
  ) {
    return { authorized: false, reason: "the preview workflow run receipt is invalid" };
  }
  const workflowStillPublishing = workflowRun.status === "in_progress" && workflowRun.conclusion == null;
  const workflowCompletedSuccessfully = workflowRun.status === "completed" && workflowRun.conclusion === "success";
  if (!workflowStillPublishing && !workflowCompletedSuccessfully) {
    return { authorized: false, reason: "the preview workflow run receipt is invalid" };
  }
  if (
    !publishJob ||
    publishJob.name !== "Publish trusted preview" ||
    publishJob.run_id !== Number(previewRunId) ||
    publishJob.run_attempt !== Number(previewRunAttempt) ||
    publishJob.head_sha !== expectedHead ||
    publishJob.status !== "completed" ||
    publishJob.conclusion !== "success"
  ) {
    return { authorized: false, reason: "the preview publication job receipt is invalid" };
  }
  return { authorized: true, reason: "", ...imageDecision };
}

function decidePreviewEvent({
  action,
  eventLabel,
  hasPreviewLabel,
  isFork,
  triggerHeadMatches,
  actorPermission,
  allowForkBuilds = false,
}) {
  if (!triggerHeadMatches) {
    return { mode: "noop", reason: "stale_event" };
  }

  if (action === "labeled" && eventLabel !== PREVIEW_LABEL) {
    return { mode: "noop", reason: "irrelevant_label" };
  }
  if (action === "unlabeled" && eventLabel !== PREVIEW_LABEL) {
    return { mode: "noop", reason: "irrelevant_label" };
  }

  if (action === "unlabeled") {
    return { mode: "skipped", reason: "label_absent" };
  }

  if (!hasPreviewLabel) {
    return { mode: "skipped", reason: "label_absent" };
  }

  if (isFork && !allowForkBuilds) {
    return {
      mode: "skipped",
      reason: "fork_isolation_unavailable",
      removeLabel: true,
    };
  }

  if (isFork && action !== "labeled") {
    return {
      mode: "skipped",
      reason: "fork_reapproval_required",
      removeLabel: true,
    };
  }

  if (isFork && !TRUSTED_LABEL_PERMISSIONS.has(actorPermission)) {
    return {
      mode: "skipped",
      reason: "fork_reapproval_required",
      removeLabel: true,
    };
  }

  if (["opened", "reopened", "synchronize", "labeled"].includes(action)) {
    return { mode: "attempt", reason: "requested" };
  }

  return { mode: "noop", reason: "unsupported_action" };
}

function compareContainsMain(compareStatus) {
  return compareStatus === "ahead" || compareStatus === "identical";
}

function selectMergedPull(pulls, mainCommit) {
  const matches = pulls.filter(
    (pull) =>
      pull.merged_at !== null &&
      pull.base?.ref === "main" &&
      pull.merge_commit_sha === mainCommit,
  );

  if (matches.length !== 1) {
    return {
      pull: null,
      reason:
        matches.length === 0
          ? "no merged pull request is associated with the main commit"
          : "multiple merged pull requests are associated with the main commit",
    };
  }

  return { pull: matches[0], reason: "" };
}

function parseSuccessfulPolicyStatus({
  statuses,
  pullNumber,
  serverUrl,
  repository,
}) {
  const latest = [...statuses]
    .filter((status) => status.context === POLICY_STATUS_CONTEXT)
    .sort((left, right) => right.id - left.id)[0];

  if (!latest) {
    return { status: null, reason: "the final PR head has no preview policy status" };
  }
  if (latest.state !== "success") {
    return {
      status: null,
      reason: `the latest preview policy status is ${latest.state}`,
    };
  }

  const match = /^Published test-([1-9][0-9]*) \(run ([1-9][0-9]*), attempt ([1-9][0-9]*)\)\.$/.exec(
    latest.description || "",
  );
  if (!match || Number(match[1]) !== pullNumber) {
    return { status: null, reason: "the preview policy status receipt is invalid" };
  }

  const runId = match[2];
  const runAttempt = match[3];
  const expectedUrl = `${serverUrl}/${repository}/actions/runs/${runId}`;
  if (latest.target_url !== expectedUrl) {
    return { status: null, reason: "the preview policy status run URL is invalid" };
  }

  return {
    status: {
      runId,
      runAttempt,
      targetUrl: expectedUrl,
    },
    reason: "",
  };
}

function decideRcPreview({ pull, hasPreviewLabel, policyStatus, workflowRun }) {
  if (!pull) {
    return { eligible: false, reason: "no unique merged pull request" };
  }
  if (!hasPreviewLabel) {
    return { eligible: false, reason: `pull request #${pull.number} is not labeled` };
  }
  if (!policyStatus) {
    return { eligible: false, reason: "the final PR head has no valid preview receipt" };
  }
  if (
    !workflowRun ||
    workflowRun.id !== Number(policyStatus.runId) ||
    workflowRun.run_attempt !== Number(policyStatus.runAttempt) ||
    workflowRun.event !== "pull_request_target" ||
    workflowRun.conclusion !== "success" ||
    workflowRun.path !== ".github/workflows/pr-test-image.yml" ||
    workflowRun.display_title !== `PR test image: PR #${pull.number}`
  ) {
    return { eligible: false, reason: "the preview workflow run receipt is invalid" };
  }
  return { eligible: true, reason: "" };
}

function resolvePullRequestEvent(payload, actorPermission) {
  const pull = payload.pull_request;
  if (!pull || !Number.isSafeInteger(pull.number)) {
    throw new Error("The event payload has no valid pull request.");
  }

  const triggerHead = pull.head?.sha;
  if (typeof triggerHead !== "string" || !/^[0-9a-f]{40}$/.test(triggerHead)) {
    throw new Error("The event payload has no valid PR head SHA.");
  }

  return {
    action: payload.action,
    eventLabel: payload.label?.name || "",
    triggerHead,
    pullNumber: pull.number,
    isFork: pull.head.repo?.full_name !== pull.base.repo.full_name,
    actorPermission,
  };
}

async function resolvePreviewAttempt({
  github,
  context,
  actorPermission,
  allowForkBuilds = false,
}) {
  const event = resolvePullRequestEvent(context.payload, actorPermission);
  const { data: pull } = await github.rest.pulls.get({
    owner: context.repo.owner,
    repo: context.repo.repo,
    pull_number: event.pullNumber,
  });

  if (pull.state !== "open" || pull.base.ref !== "main") {
    return { mode: "noop", reason: "pull_request_not_open_for_main" };
  }

  const hasPreviewLabel = hasLabel(pull.labels, PREVIEW_LABEL);
  const decision = decidePreviewEvent({
    ...event,
    allowForkBuilds,
    hasPreviewLabel,
    triggerHeadMatches: pull.head.sha === event.triggerHead,
  });

  return {
    ...decision,
    pull,
    pullNumber: pull.number,
    headSha: pull.head.sha,
    headRepository: pull.head.repo?.full_name || "",
    isFork: event.isFork,
    hasPreviewLabel,
  };
}

async function resolveRcPreview({ github, context, mainCommit }) {
  const pulls = await github.paginate(
    github.rest.repos.listPullRequestsAssociatedWithCommit,
    {
      owner: context.repo.owner,
      repo: context.repo.repo,
      commit_sha: mainCommit,
      per_page: 100,
    },
  );
  const selected = selectMergedPull(pulls, mainCommit);
  if (!selected.pull) {
    return { eligible: false, reason: selected.reason };
  }

  const { data: pull } = await github.rest.pulls.get({
    owner: context.repo.owner,
    repo: context.repo.repo,
    pull_number: selected.pull.number,
  });
  const hasPreviewLabel = hasLabel(pull.labels, PREVIEW_LABEL);
  const statuses = await github.paginate(
    github.rest.repos.listCommitStatusesForRef,
    {
      owner: context.repo.owner,
      repo: context.repo.repo,
      ref: pull.head.sha,
      per_page: 100,
    },
  );
  const receipt = parseSuccessfulPolicyStatus({
    statuses,
    pullNumber: pull.number,
    serverUrl: context.serverUrl,
    repository: `${context.repo.owner}/${context.repo.repo}`,
  });
  if (!receipt.status) {
    return { eligible: false, reason: receipt.reason };
  }

  let workflowRun = null;
  try {
    ({ data: workflowRun } = await github.rest.actions.getWorkflowRun({
      owner: context.repo.owner,
      repo: context.repo.repo,
      run_id: Number(receipt.status.runId),
    }));
  } catch (error) {
    if (error.status !== 404) {
      throw error;
    }
  }

  const workflowValid = decideRcPreview({
    pull,
    hasPreviewLabel,
    policyStatus: receipt.status,
    workflowRun,
  });
  if (!workflowValid.eligible) {
    return workflowValid;
  }

  return {
    eligible: true,
    reason: "",
    pullNumber: pull.number,
    headSha: pull.head.sha,
    runId: receipt.status.runId,
    runAttempt: receipt.status.runAttempt,
  };
}

module.exports = {
  PRODUCTION_E2E_ACTOR,
  POLICY_STATUS_CONTEXT,
  PREVIEW_LABEL,
  compareContainsMain,
  decideAutomaticProductionE2E,
  decideManualProductionE2E,
  decidePreviewEvent,
  decideRcPreview,
  parseSuccessfulPolicyStatus,
  resolvePreviewAttempt,
  resolvePullRequestEvent,
  resolveRcPreview,
  selectMergedPull,
  validateProductionImageReference,
};
