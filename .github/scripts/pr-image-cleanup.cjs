"use strict";

const fs = require("node:fs");
const { execFileSync } = require("node:child_process");

const TEST_TAG_PATTERN = /^test-([1-9][0-9]*)$/;
const PREVIEW_LABELS = Object.freeze([
  "io.dense-mem.preview.pr",
  "io.dense-mem.preview.head",
  "io.dense-mem.preview.main",
  "io.dense-mem.preview.run-id",
  "io.dense-mem.preview.run-attempt",
]);
const RELEASE_WORKFLOW = "Release prerelease";
const DEFAULT_BATCH_LIMIT = 200;

function testPrFromTag(tag) {
  const match = TEST_TAG_PATTERN.exec(tag || "");
  return match ? Number(match[1]) : null;
}

function isTestTag(tag) {
  return testPrFromTag(tag) !== null;
}

function selectedTestTags(tags, eligiblePrs, targetPr) {
  const selected = [];
  for (const tag of tags || []) {
    const pr = testPrFromTag(tag);
    if (pr !== null && (targetPr === null ? eligiblePrs.has(pr) : pr === targetPr)) {
      selected.push(tag);
    }
  }
  return selected;
}

function labelsPreviewPr(labels) {
  const value = labels?.["io.dense-mem.preview.pr"];
  const pr = testPrFromTag(`test-${value}`);
  if (pr === null) return null;
  for (const label of PREVIEW_LABELS) {
    if (typeof labels[label] !== "string" || labels[label].trim() === "") return null;
  }
  return pr;
}

function normalizeVersion(version) {
  const digest = version.digest || version.name;
  if (typeof version.id !== "number" || !/^sha256:[0-9a-f]{64}$/.test(digest || "")) {
    throw new Error("package version has an invalid id or digest");
  }
  return {
    id: version.id,
    digest,
    tags: [...new Set(version.tags || version.metadata?.container?.tags || [])].sort(),
    previewPr: version.previewPr ?? labelsPreviewPr(version.labels),
    imageRevision: version.imageRevision ?? null,
    children: [...new Set(version.children || [])],
  };
}

function releaseOutcome({ run, jobs, mergeCommitSha }) {
  if (!run || run.name !== RELEASE_WORKFLOW || run.head_sha !== mergeCommitSha || run.conclusion !== "success") {
    return { eligible: false, reason: "the prerelease workflow did not complete successfully for the merge" };
  }
  const classifier = (jobs || []).find((job) => job.name === "Classify release changes");
  if (!classifier || classifier.conclusion !== "success") {
    return { eligible: false, reason: "the release classifier did not complete successfully" };
  }
  const publicationNames = new Set([
    "Publish prerelease image / Build and push prerelease image",
    "Promote preview image",
  ]);
  const publicationJobs = (jobs || []).filter((job) => publicationNames.has(job.name));
  if (publicationJobs.some((job) => ["failure", "cancelled", "timed_out", "action_required"].includes(job.conclusion))) {
    return { eligible: false, reason: "a prerelease publication job failed" };
  }
  const noRelease = (jobs || []).find((job) => job.name === "No prerelease required");
  if (publicationJobs.some((job) => job.conclusion === "success") && noRelease?.conclusion === "success") {
    return { eligible: false, reason: "release workflow reported both publication and no-release decisions" };
  }
  if (publicationJobs.some((job) => job.conclusion === "success")) {
    return { eligible: true, reason: "prerelease publication completed" };
  }
  if (noRelease?.conclusion === "success") {
    return { eligible: true, reason: "the release classifier explicitly selected no release" };
  }
  return { eligible: false, reason: "the release workflow has no complete publication decision" };
}

function cleanupEligibility({ pull, release, releasedImage = false }) {
  if (!pull || String(pull.state).toLowerCase() !== "closed") {
    return { eligible: false, reason: "the pull request is not closed" };
  }
  if (!pull.merged_at) {
    return { eligible: true, reason: "closed without merge" };
  }
  if (!/^[0-9a-f]{40}$/.test(pull.merge_commit_sha || "")) {
    return { eligible: false, reason: "the merged pull request has no valid merge commit" };
  }
  if (!release) {
    if (releasedImage) {
      return { eligible: true, reason: "verified prerelease image metadata" };
    }
    return { eligible: false, reason: "waiting for the prerelease workflow" };
  }
  const outcome = releaseOutcome({
    run: release.run,
    jobs: release.jobs,
    mergeCommitSha: pull.merge_commit_sha,
  });
  return outcome.eligible
    ? { eligible: true, reason: outcome.reason }
    : { eligible: false, reason: outcome.reason };
}

function reachableFrom(versions, roots) {
  const byDigest = new Map(versions.map((version) => [version.digest, version]));
  const reachable = new Set();
  const queue = [...roots];
  while (queue.length > 0) {
    const digest = queue.shift();
    if (reachable.has(digest)) continue;
    reachable.add(digest);
    const version = byDigest.get(digest);
    for (const child of version?.children || []) {
      if (!reachable.has(child)) queue.push(child);
    }
  }
  return reachable;
}

function buildDeletionPlan({ versions, eligiblePrs = new Set(), targetPr = null, maxActions = DEFAULT_BATCH_LIMIT }) {
  const normalized = versions.map(normalizeVersion);
  const byDigest = new Map(normalized.map((version) => [version.digest, version]));
  const selectedByDigest = new Map();
  const selectedOrphanDigests = new Set();
  const protectedRoots = [];

  for (const version of normalized) {
    const selected = selectedTestTags(version.tags, eligiblePrs, targetPr);
    if (selected.length > 0) selectedByDigest.set(version.digest, selected);
    const ownedOrphan = version.tags.length === 0 && version.previewPr !== null && version.previewPr !== undefined &&
      (targetPr === null ? eligiblePrs.has(version.previewPr) : version.previewPr === targetPr);
    if (ownedOrphan) selectedOrphanDigests.add(version.digest);
    const retained = version.tags.some((tag) => !isTestTag(tag)) ||
      version.tags.some((tag) => isTestTag(tag) && !selected.includes(tag));
    if (retained) protectedRoots.push(version.digest);
  }

  const protectedDigests = reachableFrom(normalized, protectedRoots);
  const actions = [];
  const actionDigests = new Set();
  const blocked = [];

  for (const version of normalized) {
    const selected = selectedByDigest.get(version.digest) || [];
    if (selected.length === 0 && !selectedOrphanDigests.has(version.digest)) continue;
    if (protectedDigests.has(version.digest)) {
      if (selected.length > 0) actions.push({ type: "detach", versionId: version.id, digest: version.digest, tags: selected });
      continue;
    }
    if (selected.length === 0) {
      actions.push({ type: "delete", versionId: version.id, digest: version.digest, tags: [] });
      actionDigests.add(version.digest);
      continue;
    }
    const allTagsSelected = version.tags.every((tag) => selected.includes(tag));
    if (!allTagsSelected) {
      actions.push({ type: "detach", versionId: version.id, digest: version.digest, tags: selected });
      continue;
    }
    actions.push({ type: "delete", versionId: version.id, digest: version.digest, tags: selected });
    actionDigests.add(version.digest);
  }

  function addOrphanedChildren(digest) {
    const version = byDigest.get(digest);
    for (const childDigest of version?.children || []) {
      if (protectedDigests.has(childDigest) || actionDigests.has(childDigest)) continue;
      const child = byDigest.get(childDigest);
      if (!child) {
        blocked.push({ digest: childDigest, reason: "manifest child is absent from package versions" });
        continue;
      }
      if (child.tags.length > 0) {
        blocked.push({ digest: childDigest, reason: "manifest child has a retained tag" });
        continue;
      }
      const ownedByTarget = targetPr === null ? eligiblePrs.has(child.previewPr) : child.previewPr === targetPr;
      if (!ownedByTarget) {
        blocked.push({ digest: childDigest, reason: "untagged manifest child is missing or owned by another pull request" });
        continue;
      }
      actions.push({ type: "delete", versionId: child.id, digest: child.digest, tags: [] });
      actionDigests.add(child.digest);
      addOrphanedChildren(childDigest);
    }
  }

  for (const action of actions.filter(({ type }) => type === "delete")) {
    addOrphanedChildren(action.digest);
  }

  const uniqueActions = [];
  const actionKeys = new Set();
  for (const action of actions) {
    const key = `${action.type}:${action.versionId}:${action.tags?.join(",") || ""}`;
    if (!actionKeys.has(key)) {
      actionKeys.add(key);
      uniqueActions.push(action);
    }
  }
  if (uniqueActions.length > maxActions) {
    return {
      actions: [],
      blocked: [{ reason: `deletion set contains ${uniqueActions.length} versions; batch limit is ${maxActions}` }],
      protectedDigests: [...protectedDigests],
    };
  }
  return { actions: uniqueActions, blocked, protectedDigests: [...protectedDigests] };
}

function aggregateBatch({ plans, maxActions = DEFAULT_BATCH_LIMIT }) {
  const total = plans.reduce((sum, plan) => sum + (plan?.actions?.length || 0), 0);
  return { total, allowed: total <= maxActions };
}

function planSignature(plan) {
  return JSON.stringify({
    actions: [...(plan.actions || [])].map(({ type, versionId, digest, tags }) => ({ type, versionId, digest, tags })).sort((left, right) => JSON.stringify(left).localeCompare(JSON.stringify(right))),
    blocked: [...(plan.blocked || [])].sort((left, right) => JSON.stringify(left).localeCompare(JSON.stringify(right))),
  });
}

class GitHubApi {
  constructor({ apiUrl, token, repository }) {
    this.apiUrl = apiUrl.replace(/\/$/, "");
    this.token = token;
    this.repository = repository;
  }

  async request(path, options = {}) {
    const response = await fetch(`${this.apiUrl}${path}`, {
      ...options,
      headers: {
        Accept: "application/vnd.github+json",
        Authorization: `Bearer ${this.token}`,
        "X-GitHub-Api-Version": "2022-11-28",
        ...(options.headers || {}),
      },
    });
    if (!response.ok) {
      const body = await response.text();
      const error = new Error(`GitHub API ${response.status} for ${path}: ${body.slice(0, 300)}`);
      error.status = response.status;
      throw error;
    }
    if (response.status === 204) return null;
    return response.json();
  }

  async paged(path) {
    const values = [];
    for (let page = 1; page <= 100; page += 1) {
      const separator = path.includes("?") ? "&" : "?";
      const response = await this.request(`${path}${separator}per_page=100&page=${page}`);
      const pageValues = Array.isArray(response) ? response : response.workflow_runs || response.versions || response.jobs || response.items || [];
      values.push(...pageValues);
      if (pageValues.length < 100) return values;
    }
    throw new Error(`pagination exceeded the 100-page limit for ${path}`);
  }

  pull(number) {
    return this.request(`/repos/${this.repository}/pulls/${number}`);
  }

  async versions(packageName) {
    return this.paged(`/orgs/${this.repository.split("/")[0]}/packages/container/${encodeURIComponent(packageName)}/versions`);
  }

  async releaseRuns() {
    return this.paged(`/repos/${this.repository}/actions/workflows/release-rc.yml/runs?branch=main`);
  }

  async jobs(runId) {
    return this.paged(`/repos/${this.repository}/actions/runs/${runId}/jobs`);
  }

  async deleteVersion(packageName, versionId) {
    return this.request(`/orgs/${this.repository.split("/")[0]}/packages/container/${encodeURIComponent(packageName)}/versions/${versionId}`, { method: "DELETE" });
  }

  async associatedPulls(commit) {
    return this.paged(`/repos/${this.repository}/commits/${commit}/pulls`);
  }
}

class RegistryClient {
  constructor({ image, binary = process.env.REGCTL_BIN || "regctl" }) {
    this.image = image;
    this.binary = binary;
    this.manifestCache = new Map();
    this.inspectCache = new Map();
  }

  run(args) {
    return execFileSync(this.binary, args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
  }

  manifest(ref) {
    if (!this.manifestCache.has(ref)) {
      const body = this.run(["manifest", "get", ref, "--format", "raw-body"]);
      this.manifestCache.set(ref, JSON.parse(body));
    }
    return this.manifestCache.get(ref);
  }

  labels(ref, platform) {
    const key = `${ref}|${platform || ""}`;
    if (!this.inspectCache.has(key)) {
      const args = ["image", "inspect", ref];
      if (platform) args.push("--platform", platform);
      args.push("--format", "{{json .}}");
      this.inspectCache.set(key, JSON.parse(this.run(args))?.config?.Labels || {});
    }
    return this.inspectCache.get(key);
  }

  head(ref) {
    return this.run(["manifest", "head", ref]);
  }

  detachTag(digest, tag, suffix) {
    const target = `${this.image}:${tag}`;
    const source = `${this.image}@${digest}`;
    const labels = [
      ...PREVIEW_LABELS.map((label) => ["--label", `${label}=`]).flat(),
      "--label", `org.opencontainers.image.version=cleanup-${suffix}`,
    ];
    this.run(["image", "mod", source, "--create", target, ...labels]);
    const newDigest = this.head(target);
    if (!/^sha256:[0-9a-f]{64}$/.test(newDigest) || newDigest === digest) {
      throw new Error(`detaching ${target} did not create a new manifest`);
    }
    return newDigest;
  }

  scanVersions(versions, roots = versions) {
    const byDigest = new Map(versions.map((version) => [version.digest, version]));
    const visited = new Set();
    const scan = (digest, platform) => {
      if (visited.has(digest)) return;
      const version = byDigest.get(digest);
      if (!version) return;
      visited.add(digest);
      const body = this.manifest(`${this.image}@${digest}`);
      version.children = [...new Set((body.manifests || []).map((child) => child.digest).filter(Boolean))];
      if (version.children.length > 0) {
        for (const child of body.manifests || []) {
          const childPlatform = child.platform && child.platform.os && child.platform.architecture
            ? `${child.platform.os}/${child.platform.architecture}`
            : platform;
          scan(child.digest, childPlatform);
        }
        const childVersions = version.children.map((child) => byDigest.get(child)).filter(Boolean);
        const previewOwners = [...new Set(childVersions.map((child) => child.previewPr).filter((value) => value !== null && value !== undefined))];
        const revisions = [...new Set(childVersions.map((child) => child.imageRevision).filter(Boolean))];
        if (previewOwners.length === 1) version.previewPr = version.previewPr ?? previewOwners[0];
        if (revisions.length === 1) version.imageRevision = version.imageRevision ?? revisions[0];
        return;
      }
      const labels = this.labels(`${this.image}@${digest}`, platform);
      version.previewPr = version.previewPr ?? labelsPreviewPr(labels);
      version.imageRevision = version.imageRevision ?? labels["org.opencontainers.image.revision"] ?? null;
    };
    for (const version of roots) scan(version.digest);
    return versions;
  }
}

async function releaseForPull(api, pull) {
  if (!pull?.merged_at) return null;
  const runs = await api.releaseRuns();
  const run = runs.find((candidate) => candidate.name === RELEASE_WORKFLOW && candidate.head_sha === pull.merge_commit_sha);
  if (!run) return null;
  return { run, jobs: await api.jobs(run.id) };
}

async function pullForNumber(api, number) {
  try {
    return await api.pull(number);
  } catch (error) {
    if (error.status === 404) return null;
    throw error;
  }
}

function eventPayload() {
  const eventPath = process.env.GITHUB_EVENT_PATH;
  if (!eventPath) throw new Error("GITHUB_EVENT_PATH is required");
  return JSON.parse(fs.readFileSync(eventPath, "utf8"));
}

async function resolveTargets(api, event, versions) {
  const eventName = process.env.GITHUB_EVENT_NAME;
  const requested = process.env.CLEANUP_PR_NUMBER;
  if (requested) {
    const number = Number(requested);
    if (!Number.isSafeInteger(number) || number < 1) throw new Error("CLEANUP_PR_NUMBER must be a positive integer");
    const pull = await pullForNumber(api, number);
    const release = await releaseForPull(api, pull);
    return [{ number, pull, release }];
  }
  if (eventName === "pull_request_target") {
    const number = event.pull_request?.number;
    const pull = await pullForNumber(api, number);
    const release = await releaseForPull(api, pull);
    return [{ number, pull, release }];
  }
  if (eventName === "workflow_run") {
    const headSha = event.workflow_run?.head_sha;
    const pulls = await api.associatedPulls(headSha);
    const merged = pulls.filter((pull) => pull.merged_at && pull.merge_commit_sha === headSha);
    if (merged.length !== 1) return [];
    const pull = await pullForNumber(api, merged[0].number);
    return [{ number: merged[0].number, pull, release: { run: event.workflow_run, jobs: await api.jobs(event.workflow_run.id) } }];
  }
  const numbers = new Set();
  for (const version of versions) {
    for (const tag of version.tags || []) {
      const number = testPrFromTag(tag);
      if (number !== null) numbers.add(number);
    }
    if (version.previewPr !== null && version.previewPr !== undefined) numbers.add(version.previewPr);
  }
  return Promise.all([...numbers].sort((a, b) => a - b).map(async (number) => {
    const pull = await pullForNumber(api, number);
    const release = await releaseForPull(api, pull);
    return { number, pull, release };
  }));
}

function outputSummary(summary) {
  const rendered = JSON.stringify(summary, null, 2);
  console.log(rendered);
  if (process.env.GITHUB_STEP_SUMMARY) {
    fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY, `### PR image cleanup\n\n\`\`\`json\n${rendered}\n\`\`\`\n`);
  }
}

async function main() {
  const repository = process.env.GITHUB_REPOSITORY;
  const token = process.env.GITHUB_TOKEN;
  const packageName = repository?.split("/")[1]?.toLowerCase();
  if (!repository || !token || !packageName) throw new Error("GITHUB_REPOSITORY, GITHUB_TOKEN, and a repository package are required");
  const api = new GitHubApi({ apiUrl: process.env.GITHUB_API_URL || "https://api.github.com", token, repository });
  const versions = (await api.versions(packageName)).map(normalizeVersion);
  const dryRun = process.env.CLEANUP_DRY_RUN === "true";
  const maxActions = Number(process.env.CLEANUP_BATCH_LIMIT || DEFAULT_BATCH_LIMIT);
  const image = `ghcr.io/${repository.toLowerCase()}`;
  const registry = new RegistryClient({ image });
  const event = eventPayload();
  registry.scanVersions(versions);
  const targets = await resolveTargets(api, event, versions);

  const prepareTarget = (target) => {
    if (!target.pull) return { target, eligibility: { eligible: false, reason: "pull request not found" }, plan: null };
    const eligiblePrs = new Set([target.number]);
    const releasedImage = Boolean(target.pull.merged_at && versions.some((version) =>
      version.imageRevision === target.pull.merge_commit_sha &&
      version.tags.some((tag) => /^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$/.test(tag)),
    ));
    const eligibility = cleanupEligibility({ ...target, releasedImage });
    if (!eligibility.eligible) return { target, eligibility, plan: null };
    const plan = buildDeletionPlan({ versions, eligiblePrs, targetPr: target.number, maxActions });
    return { target, eligibility, plan };
  };
  const preparedTargets = targets.map(prepareTarget);
  const manualSweep = process.env.GITHUB_EVENT_NAME === "workflow_dispatch" && !process.env.CLEANUP_PR_NUMBER;
  if (manualSweep) {
    const batch = aggregateBatch({ plans: preparedTargets.map(({ plan }) => plan), maxActions });
    if (!batch.allowed) {
      outputSummary({
        dryRun,
        summaries: preparedTargets.map(({ target, eligibility, plan }) => ({
          pr: target.number,
          status: plan ? "blocked" : eligibility.eligible ? "blocked" : "retained",
          reason: plan ? `manual batch contains ${batch.total} versions; batch limit is ${maxActions}` : eligibility.reason,
          actions: plan?.actions || [],
          blocked: plan?.blocked || [],
        })),
      });
      return;
    }
  }

  const summaries = [];
  for (const prepared of preparedTargets) {
    const { target, eligibility, plan } = prepared;
    if (!target.pull) {
      summaries.push({ pr: target.number, status: "skipped", reason: "pull request not found" });
      continue;
    }
    if (!eligibility.eligible) {
      summaries.push({ pr: target.number, status: "retained", reason: eligibility.reason });
      continue;
    }
    const result = { pr: target.number, status: plan.blocked.length ? "blocked" : dryRun ? "dry_run" : "cleaned", reason: eligibility.reason, actions: plan.actions, blocked: plan.blocked };
    if (!dryRun && plan.blocked.length === 0) {
      const refreshedPull = await pullForNumber(api, target.number);
      const refreshedRelease = await releaseForPull(api, refreshedPull);
      if (!refreshedPull || String(refreshedPull.state).toLowerCase() !== "closed") {
        throw new Error(`pull request #${target.number} changed state before cleanup`);
      }
      const refreshedVersions = (await api.versions(packageName)).map(normalizeVersion);
      registry.scanVersions(refreshedVersions);
      const refreshedEligiblePrs = new Set([target.number]);
      const refreshedPlan = buildDeletionPlan({
        versions: refreshedVersions,
        eligiblePrs: refreshedEligiblePrs,
        targetPr: target.number,
        maxActions,
      });
      if (planSignature(refreshedPlan) !== planSignature(plan)) {
        throw new Error(`cleanup plan changed before deletion for pull request #${target.number}`);
      }
      for (const action of refreshedPlan.actions) {
        const current = refreshedVersions.find((version) => version.id === action.versionId);
        if (!current || current.digest !== action.digest) {
          throw new Error(`cleanup target ${action.digest} changed before deletion`);
        }
        if (action.type === "delete" && action.tags.some((tag) => !current.tags.includes(tag))) {
          throw new Error(`cleanup tag ${action.tags.join(", ")} changed before deletion`);
        }
      }
      const refreshedReleasedImage = Boolean(refreshedPull.merged_at && refreshedVersions.some((version) =>
        version.tags.some((tag) => /^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$/.test(tag)) &&
        version.imageRevision === refreshedPull.merge_commit_sha,
      ));
      const refreshedEligibility = cleanupEligibility({
        pull: refreshedPull,
        release: refreshedRelease,
        releasedImage: refreshedReleasedImage,
      });
      if (!refreshedEligibility.eligible) {
        throw new Error(`cleanup eligibility changed before deletion: ${refreshedEligibility.reason}`);
      }
      const completed = [];
      for (const action of refreshedPlan.actions.filter(({ type }) => type === "detach")) {
        for (const tag of action.tags) {
          const detachedDigest = registry.detachTag(action.digest, tag, `${target.number}-${Date.now()}`);
          const refreshed = (await api.versions(packageName)).map(normalizeVersion);
          const detached = refreshed.find((version) => version.digest === detachedDigest);
          if (!detached) throw new Error(`detached version ${detachedDigest} was not visible in GitHub Packages`);
          await api.deleteVersion(packageName, detached.id);
          completed.push({ type: "detach", tag, deletedVersionId: detached.id, digest: detachedDigest });
        }
      }
      for (const action of refreshedPlan.actions.filter(({ type }) => type === "delete")) {
        try {
          await api.deleteVersion(packageName, action.versionId);
          completed.push(action);
        } catch (error) {
          if (error.status === 404) completed.push({ ...action, alreadyDeleted: true });
          else throw error;
        }
      }
      result.completed = completed;
    }
    summaries.push(result);
  }
  outputSummary({ dryRun, summaries });
}

if (require.main === module) {
  main().catch((error) => {
    console.error(`PR image cleanup failed: ${error.message}`);
    process.exitCode = 1;
  });
}

module.exports = {
  DEFAULT_BATCH_LIMIT,
  PREVIEW_LABELS,
  TEST_TAG_PATTERN,
  buildDeletionPlan,
  aggregateBatch,
  cleanupEligibility,
  isTestTag,
  labelsPreviewPr,
  normalizeVersion,
  releaseOutcome,
  selectedTestTags,
  testPrFromTag,
};
