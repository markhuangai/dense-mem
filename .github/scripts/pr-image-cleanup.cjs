"use strict";

const fs = require("node:fs");
const { execFileSync } = require("node:child_process");

const TEST_TAG_PATTERN = /^test-([1-9][0-9]*)$/;
const PRERELEASE_TAG_PATTERN = /^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$/;
const PREVIEW_LABELS = Object.freeze([
  "io.dense-mem.preview.pr",
  "io.dense-mem.preview.head",
  "io.dense-mem.preview.main",
  "io.dense-mem.preview.run-id",
  "io.dense-mem.preview.run-attempt",
]);
const RELEASE_WORKFLOW = "Release prerelease";
const DEFAULT_BATCH_LIMIT = 200;
const TARGET_CONCURRENCY = 8;
const DEFAULT_PREVIEW_QUIESCE_MAX_POLLS = 90;
const DEFAULT_PREVIEW_QUIESCE_POLL_MILLISECONDS = 60_000;
const PREVIEW_ACTIVE_STATUSES = Object.freeze(["queued", "in_progress", "waiting", "requested", "pending"]);
const PREVIEW_PUBLICATION_JOB_NAMES = Object.freeze(["Build untrusted preview", "Publish trusted preview"]);

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

function releaseTargetSha(run) {
  const title = run?.display_title || run?.displayTitle;
  const prefix = `${RELEASE_WORKFLOW}: `;
  if (run?.name !== RELEASE_WORKFLOW || typeof title !== "string" || !title.startsWith(prefix)) return null;
  const sha = title.slice(prefix.length).trim();
  return /^[0-9a-f]{40}$/.test(sha) ? sha : null;
}

function previewRunForPull(run, pullNumber) {
  const title = run?.display_title || run?.displayTitle;
  return title === `PR test image: PR #${pullNumber}`;
}

function isActivePreviewStatus(status) {
  return PREVIEW_ACTIVE_STATUSES.includes(status);
}

async function previewPublicationIsActive(api, run) {
  if (!isActivePreviewStatus(run.status)) return false;
  const jobs = await api.jobs(run.id);
  if (jobs.some((job) => PREVIEW_PUBLICATION_JOB_NAMES.includes(job.name) && isActivePreviewStatus(job.status))) {
    return true;
  }
  const publish = jobs.find((job) => job.name === "Publish trusted preview");
  if (publish?.status === "completed") return false;
  const build = jobs.find((job) => job.name === "Build untrusted preview");
  if (build?.status === "completed" && build.conclusion !== "success") return false;
  return true;
}

function releaseOutcome({ run, jobs, mergeCommitSha }) {
  if (!run || releaseTargetSha(run) !== mergeCommitSha || run.conclusion !== "success") {
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

function cleanupEligibility({ pull, release, releasedImage = false, allowLegacy = false }) {
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
    if (allowLegacy) {
      return { eligible: true, reason: "explicit legacy cleanup override" };
    }
    return { eligible: false, reason: "waiting for the prerelease workflow" };
  }
  const outcome = releaseOutcome({
    run: release.run,
    jobs: release.jobs,
    mergeCommitSha: pull.merge_commit_sha,
  });
  if (!outcome.eligible && releasedImage) {
    return { eligible: true, reason: "verified prerelease image metadata" };
  }
  return outcome.eligible
    ? { eligible: true, reason: outcome.reason }
    : { eligible: false, reason: outcome.reason };
}

function hasReleasedImage(versions, mergeCommitSha) {
  return versions.some((version) =>
    version.imageRevision === mergeCommitSha &&
    (version.tags || []).some((tag) => PRERELEASE_TAG_PATTERN.test(tag)),
  );
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

function postOrder(versions, rootDigest) {
  const byDigest = new Map(versions.map((version) => [version.digest, version]));
  const visited = new Set();
  const ordered = [];
  const visit = (digest) => {
    if (visited.has(digest)) return;
    visited.add(digest);
    for (const child of byDigest.get(digest)?.children || []) visit(child);
    ordered.push(digest);
  };
  visit(rootDigest);
  return ordered;
}

function buildDetachedDeletionPlan({ versions, detachedDigest, selectedTags = [] }) {
  const normalized = versions.map(normalizeVersion);
  const byDigest = new Map(normalized.map((version) => [version.digest, version]));
  const detached = byDigest.get(detachedDigest);
  if (!detached) {
    return {
      actions: [],
      blocked: [{ digest: detachedDigest, reason: "detached manifest is absent from package versions" }],
      protectedDigests: [],
      cost: 0,
    };
  }

  const detachedSubtree = reachableFrom(normalized, [detachedDigest]);
  const externalRoots = normalized.filter((version) => !detachedSubtree.has(version.digest));
  const retainedRoots = normalized.filter((version) => version.digest !== detachedDigest && detachedSubtree.has(version.digest) &&
    version.tags.some((tag) => !isTestTag(tag) || !selectedTags.includes(tag)));
  const protectedDigests = reachableFrom(normalized, [
    ...externalRoots.map((version) => version.digest),
    ...retainedRoots.map((version) => version.digest),
  ]);
  if (protectedDigests.has(detachedDigest)) {
    return {
      actions: [],
      blocked: [{ digest: detachedDigest, reason: "detached manifest is referenced by a retained manifest graph" }],
      protectedDigests: [...protectedDigests],
      cost: 0,
    };
  }
  const blocked = [];
  const actions = [];
  for (const digest of postOrder(normalized, detachedDigest).reverse()) {
    const version = byDigest.get(digest);
    if (!version) {
      blocked.push({ digest, reason: "generated manifest is absent from package versions" });
      continue;
    }
    if (protectedDigests.has(digest)) continue;
    if (digest === detachedDigest) {
      const unexpectedTags = version.tags.filter((tag) => !selectedTags.includes(tag));
      if (unexpectedTags.length > 0) {
        blocked.push({ digest, reason: "detached manifest has a retained tag" });
        continue;
      }
    } else if (version.tags.length > 0) {
      blocked.push({ digest, reason: "generated manifest has a retained tag" });
      continue;
    }
    actions.push({
      type: "delete",
      versionId: version.id,
      digest: version.digest,
      tags: digest === detachedDigest ? [...selectedTags] : [],
    });
  }
  return { actions, blocked, protectedDigests: [...protectedDigests], cost: actions.length };
}

function validateCleanupState({ pull, release, releasedImage = false, allowLegacy = false }) {
  if (!pull || String(pull.state).toLowerCase() !== "closed") {
    throw new Error("pull request changed state before cleanup");
  }
  const eligibility = cleanupEligibility({ pull, release, releasedImage, allowLegacy });
  if (!eligibility.eligible) {
    throw new Error(`cleanup eligibility changed before deletion: ${eligibility.reason}`);
  }
  return eligibility;
}

function deletionCost(versions, actions) {
  return actions.reduce((total, action) => {
    if (action.type !== "detach") return total + 1;
    return total + action.tags.length * reachableFrom(versions, [action.digest]).size;
  }, 0);
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
  const cost = deletionCost(normalized, uniqueActions);
  if (cost > maxActions) {
    return {
      actions: [],
      blocked: [{ reason: `deletion set contains ${cost} versions; batch limit is ${maxActions}` }],
      protectedDigests: [...protectedDigests],
      cost,
    };
  }
  return { actions: uniqueActions, blocked, protectedDigests: [...protectedDigests], cost };
}

function aggregateBatch({ plans, maxActions = DEFAULT_BATCH_LIMIT }) {
  const total = plans.reduce((sum, plan) => {
    if (!plan || (plan.blocked || []).length > 0) return sum;
    return sum + (plan.cost ?? plan.actions?.length ?? 0);
  }, 0);
  return { total, allowed: total <= maxActions };
}

function planSignature(plan) {
  return JSON.stringify({
    actions: [...(plan.actions || [])].map(({ type, versionId, digest, tags }) => ({ type, versionId, digest, tags })).sort((left, right) => JSON.stringify(left).localeCompare(JSON.stringify(right))),
    blocked: [...(plan.blocked || [])].sort((left, right) => JSON.stringify(left).localeCompare(JSON.stringify(right))),
  });
}

function assertPlanUnchanged(expected, actual) {
  if (planSignature(expected) !== planSignature(actual)) {
    throw new Error("cleanup plan changed before deletion");
  }
}

async function mapWithConcurrency(values, concurrency, mapper) {
  const results = new Array(values.length);
  let next = 0;
  const worker = async () => {
    while (next < values.length) {
      const index = next;
      next += 1;
      results[index] = await mapper(values[index], index);
    }
  };
  await Promise.all(Array.from({ length: Math.min(concurrency, values.length) }, worker));
  return results;
}

class GitHubApi {
  constructor({ apiUrl, token, repository }) {
    this.apiUrl = apiUrl.replace(/\/$/, "");
    this.token = token;
    this.repository = repository;
    this.releaseRunsPromise = null;
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

  releaseRuns() {
    if (!this.releaseRunsPromise) {
      this.releaseRunsPromise = this.paged(`/repos/${this.repository}/actions/workflows/release-rc.yml/runs?branch=main`);
    }
    return this.releaseRunsPromise;
  }

  async previewRuns() {
    const pages = await Promise.all(PREVIEW_ACTIVE_STATUSES.map((status) =>
      this.paged(`/repos/${this.repository}/actions/workflows/pr-test-image.yml/runs?event=pull_request_target&status=${status}`),
    ));
    const runs = new Map();
    for (const page of pages) {
      for (const run of page) runs.set(run.id, run);
    }
    return [...runs.values()];
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
    const previewPr = testPrFromTag(tag);
    if (previewPr === null) throw new Error(`cannot detach a non-preview tag: ${tag}`);
    const labels = [
      "--label", `io.dense-mem.preview.pr=${previewPr}`,
      "--label", `io.dense-mem.preview.head=cleanup-${suffix}`,
      "--label", `io.dense-mem.preview.main=cleanup-${suffix}`,
      "--label", `io.dense-mem.preview.run-id=${suffix}`,
      "--label", "io.dense-mem.preview.run-attempt=1",
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
  const matching = runs.filter((candidate) => releaseTargetSha(candidate) === pull.merge_commit_sha);
  if (matching.length === 0) return null;
  let newestUnresolved = null;
  for (const run of matching) {
    const jobs = await api.jobs(run.id);
    const receipt = { run, jobs };
    if (!newestUnresolved) newestUnresolved = receipt;
    if (releaseOutcome({ run, jobs, mergeCommitSha: pull.merge_commit_sha }).eligible) return receipt;
  }
  return newestUnresolved;
}

async function pullForNumber(api, number) {
  try {
    return await api.pull(number);
  } catch (error) {
    if (error.status === 404) return null;
    throw error;
  }
}

async function waitForPreviewQuiescence(api, pullNumbers, {
  maxPolls = DEFAULT_PREVIEW_QUIESCE_MAX_POLLS,
  pollMilliseconds = DEFAULT_PREVIEW_QUIESCE_POLL_MILLISECONDS,
  sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)),
} = {}) {
  const numbers = [...new Set(pullNumbers.filter((number) => Number.isSafeInteger(number) && number > 0))];
  if (numbers.length === 0) return;
  for (let attempt = 0; attempt < maxPolls; attempt += 1) {
    const runs = await api.previewRuns();
    const candidates = runs.filter((run) => numbers.some((number) => previewRunForPull(run, number)) && isActivePreviewStatus(run.status));
    const active = [];
    for (const run of candidates) {
      if (await previewPublicationIsActive(api, run)) active.push(run);
    }
    if (active.length === 0) return;
    if (attempt + 1 >= maxPolls) {
      throw new Error(`preview publication is still active for pull requests: ${numbers.join(", ")}`);
    }
    await sleep(pollMilliseconds);
  }
}

function eventPayload() {
  const eventPath = process.env.GITHUB_EVENT_PATH;
  if (!eventPath) throw new Error("GITHUB_EVENT_PATH is required");
  return JSON.parse(fs.readFileSync(eventPath, "utf8"));
}

async function resolveTargets(api, event, versions, environment = process.env) {
  const eventName = environment.GITHUB_EVENT_NAME;
  const requested = environment.CLEANUP_PR_NUMBER;
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
  if (eventName === "workflow_run" && environment.CLEANUP_MANUAL_SWEEP !== "true") {
    const targetSha = releaseTargetSha(event.workflow_run);
    if (!targetSha) return [];
    const pulls = await api.associatedPulls(targetSha);
    const merged = pulls.filter((pull) => pull.merged_at && pull.merge_commit_sha === targetSha);
    if (merged.length !== 1) return [];
    const pull = await pullForNumber(api, merged[0].number);
    return [{ number: merged[0].number, pull, release: { run: event.workflow_run, jobs: await api.jobs(event.workflow_run.id) } }];
  }
  if (environment.CLEANUP_MANUAL_SWEEP === "true") {
    const numbers = new Set();
    for (const version of versions) {
      for (const tag of version.tags || []) {
        const number = testPrFromTag(tag);
        if (number !== null) numbers.add(number);
      }
      if (version.previewPr !== null && version.previewPr !== undefined) numbers.add(version.previewPr);
    }
    return mapWithConcurrency([...numbers].sort((a, b) => a - b), TARGET_CONCURRENCY, async (number) => {
      const pull = await pullForNumber(api, number);
      const release = await releaseForPull(api, pull);
      return { number, pull, release };
    });
  }
  const numbers = new Set();
  for (const version of versions) {
    for (const tag of version.tags || []) {
      const number = testPrFromTag(tag);
      if (number !== null) numbers.add(number);
    }
    if (version.previewPr !== null && version.previewPr !== undefined) numbers.add(version.previewPr);
  }
  return mapWithConcurrency([...numbers].sort((a, b) => a - b), TARGET_CONCURRENCY, async (number) => {
    const pull = await pullForNumber(api, number);
    const release = await releaseForPull(api, pull);
    return { number, pull, release };
  });
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
  let versions = (await api.versions(packageName)).map(normalizeVersion);
  const dryRun = process.env.CLEANUP_DRY_RUN === "true";
  const allowLegacy = process.env.CLEANUP_ALLOW_LEGACY === "true" && Boolean(process.env.CLEANUP_PR_NUMBER);
  const maxActions = Number(process.env.CLEANUP_BATCH_LIMIT || DEFAULT_BATCH_LIMIT);
  const image = `ghcr.io/${repository.toLowerCase()}`;
  const registry = new RegistryClient({ image });
  const event = eventPayload();
  registry.scanVersions(versions);
  const targets = await resolveTargets(api, event, versions);
  await waitForPreviewQuiescence(api, targets
    .filter(({ pull }) => String(pull?.state).toLowerCase() === "closed")
    .map(({ number }) => number));
  versions = (await api.versions(packageName)).map(normalizeVersion);
  registry.scanVersions(versions);

  const prepareTarget = (target) => {
    if (!target.pull) return { target, eligibility: { eligible: false, reason: "pull request not found" }, plan: null };
    const eligiblePrs = new Set([target.number]);
    const releasedImage = Boolean(target.pull.merged_at && hasReleasedImage(versions, target.pull.merge_commit_sha));
    const eligibility = cleanupEligibility({ ...target, releasedImage, allowLegacy });
    if (!eligibility.eligible) return { target, eligibility, plan: null };
    const plan = buildDeletionPlan({ versions, eligiblePrs, targetPr: target.number, maxActions });
    return { target, eligibility, plan };
  };
  const preparedTargets = targets.map(prepareTarget);
  const manualSweep = process.env.CLEANUP_MANUAL_SWEEP === "true" ||
    (process.env.GITHUB_EVENT_NAME === "workflow_dispatch" && !process.env.CLEANUP_PR_NUMBER);
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
  let executedBatchCost = 0;
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
      if (!manualSweep) assertPlanUnchanged(plan, refreshedPlan);
      if (refreshedPlan.blocked.length > 0) {
        result.status = "blocked";
        result.reason = refreshedPlan.blocked[0].reason;
        result.actions = [];
        result.blocked = refreshedPlan.blocked;
        summaries.push(result);
        continue;
      }
      if (manualSweep && executedBatchCost + refreshedPlan.cost > maxActions) {
        result.status = "blocked";
        result.reason = `manual batch contains more than ${maxActions} versions after revalidation`;
        result.actions = [];
        result.blocked = [{ reason: result.reason }];
        summaries.push(result);
        continue;
      }
      result.actions = refreshedPlan.actions;
      result.blocked = refreshedPlan.blocked;
      for (const action of refreshedPlan.actions) {
        const current = refreshedVersions.find((version) => version.id === action.versionId);
        if (!current || current.digest !== action.digest) {
          throw new Error(`cleanup target ${action.digest} changed before deletion`);
        }
        if (action.type === "delete" && action.tags.some((tag) => !current.tags.includes(tag))) {
          throw new Error(`cleanup tag ${action.tags.join(", ")} changed before deletion`);
        }
      }
      const refreshedReleasedImage = Boolean(refreshedPull.merged_at && hasReleasedImage(refreshedVersions, refreshedPull.merge_commit_sha));
      validateCleanupState({
        pull: refreshedPull,
        release: refreshedRelease,
        releasedImage: refreshedReleasedImage,
        allowLegacy,
      });
      const completed = [];
      for (const action of refreshedPlan.actions.filter(({ type }) => type === "detach")) {
        for (const tag of action.tags) {
          const detachedDigest = registry.detachTag(action.digest, tag, `${target.number}-${Date.now()}`);
          const refreshed = (await api.versions(packageName)).map(normalizeVersion);
          registry.scanVersions(refreshed);
          const detached = refreshed.find((version) => version.digest === detachedDigest);
          if (!detached) throw new Error(`detached version ${detachedDigest} was not visible in GitHub Packages`);
          const detachedPlan = buildDetachedDeletionPlan({
            versions: refreshed,
            detachedDigest,
            selectedTags: [tag],
          });
          if (detachedPlan.blocked.length > 0) {
            throw new Error(`detached manifest graph is unsafe to delete: ${JSON.stringify(detachedPlan.blocked)}`);
          }
          if (detachedPlan.cost > maxActions) {
            throw new Error(`detached manifest graph contains ${detachedPlan.cost} versions; batch limit is ${maxActions}`);
          }
          for (const generated of detachedPlan.actions) {
            try {
              await api.deleteVersion(packageName, generated.versionId);
              completed.push({ ...generated, type: "detach", tag, generated: true });
            } catch (error) {
              if (error.status === 404) completed.push({ ...generated, type: "detach", tag, generated: true, alreadyDeleted: true });
              else throw error;
            }
          }
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
      if (manualSweep) executedBatchCost += refreshedPlan.cost;
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
  buildDetachedDeletionPlan,
  aggregateBatch,
  assertPlanUnchanged,
  cleanupEligibility,
  GitHubApi,
  hasReleasedImage,
  isTestTag,
  labelsPreviewPr,
  mapWithConcurrency,
  normalizeVersion,
  previewRunForPull,
  releaseForPull,
  releaseOutcome,
  releaseTargetSha,
  resolveTargets,
  selectedTestTags,
  testPrFromTag,
  validateCleanupState,
  waitForPreviewQuiescence,
  RegistryClient,
};
