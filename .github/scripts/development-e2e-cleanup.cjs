"use strict";

const fs = require("node:fs");
const { GitHubApi, RegistryClient, normalizeVersion } = require("./pr-image-cleanup.cjs");
const {
  RUN_TAG_PATTERN, SHA_PATTERN, imageName, assertDevelopmentImage, assertTrustedDevelopmentRun,
} = require("./development-e2e-policy.cjs");

function ownedVersion(version, registry, repository) {
  if (version.tags.length !== 1) return { reason: "expected exactly one owned run tag" };
  const match = RUN_TAG_PATTERN.exec(version.tags[0]);
  if (!match) return { reason: "unexpected development image tag" };
  const ref = `${registry.image}@${version.digest}`;
  const manifest = registry.manifest(ref);
  if (manifest.manifests?.length) return { reason: "unexpected development image manifest index" };
  const labels = registry.labels(ref, "linux/amd64");
  const sourceRevision = labels["org.opencontainers.image.revision"];
  if (!SHA_PATTERN.test(sourceRevision || "")) return { reason: "missing development source revision" };
  try {
    assertDevelopmentImage({ labels, repository, sourceRevision, runId: match[1], runAttempt: match[2] });
  } catch {
    return { reason: "development image ownership labels do not match its tag" };
  }
  return { runId: Number(match[1]), runAttempt: Number(match[2]), sourceRevision, tag: version.tags[0] };
}

async function completedOwner(api, repository, owner) {
  const current = await api.request(`/repos/${repository}/actions/runs/${owner.runId}`);
  assertTrustedDevelopmentRun(current, repository);
  if (current.status !== "completed") return { reason: "development run is still active" };
  if (current.run_attempt < owner.runAttempt) return { reason: "image names an unknown run attempt" };
  const attempt = current.run_attempt === owner.runAttempt ? current
    : await api.request(`/repos/${repository}/actions/runs/${owner.runId}/attempts/${owner.runAttempt}`);
  assertTrustedDevelopmentRun(attempt, repository);
  if (attempt.id !== owner.runId || attempt.run_attempt !== owner.runAttempt || attempt.status !== "completed") {
    return { reason: "development run attempt is not complete" };
  }
  return {};
}

async function cleanupDevelopmentImages({ api, registry, repository, run = null, dryRun = false }) {
  if (run) assertTrustedDevelopmentRun(run, repository);
  const packageName = `${repository.split("/")[1].toLowerCase()}-e2e`;
  imageName(repository);
  const summary = { dry_run: dryRun, deleted: [], retained: [], already_deleted: [] };
  let versions;
  try {
    versions = (await api.versions(packageName)).map(normalizeVersion);
  } catch (error) {
    if (error.status !== 404) throw error;
    summary.retained.push({ reason: "development image package is not present" });
    return summary;
  }
  for (const version of versions) {
    const tagMatch = RUN_TAG_PATTERN.exec(version.tags.find((tag) => RUN_TAG_PATTERN.test(tag)) || "");
    if (run && (!tagMatch || Number(tagMatch[1]) !== run.id || Number(tagMatch[2]) !== run.run_attempt)) continue;
    const owner = ownedVersion(version, registry, repository);
    if (owner.reason) {
      summary.retained.push({ version_id: version.id, reason: owner.reason });
      continue;
    }
    let state = await completedOwner(api, repository, owner);
    if (state.reason) {
      summary.retained.push({ version_id: version.id, reason: state.reason });
      continue;
    }
    let refreshed;
    try {
      refreshed = normalizeVersion(await api.request(`/orgs/${repository.split("/")[0]}/packages/container/${encodeURIComponent(packageName)}/versions/${version.id}`));
    } catch (error) {
      if (error.status !== 404) throw error;
      summary.already_deleted.push(version.id);
      continue;
    }
    const currentOwner = ownedVersion(refreshed, registry, repository);
    if (refreshed.id !== version.id || refreshed.digest !== version.digest || currentOwner.reason ||
        currentOwner.tag !== owner.tag || registry.head(`${registry.image}:${owner.tag}`) !== version.digest) {
      summary.retained.push({ version_id: version.id, reason: "development image identity changed before deletion" });
      continue;
    }
    state = await completedOwner(api, repository, owner);
    if (state.reason) {
      summary.retained.push({ version_id: version.id, reason: state.reason });
      continue;
    }
    const action = { version_id: version.id, digest: version.digest, run_id: owner.runId, run_attempt: owner.runAttempt };
    if (dryRun) {
      summary.retained.push({ ...action, reason: "dry run" });
      continue;
    }
    try {
      await api.deleteVersion(packageName, version.id);
      summary.deleted.push(action);
    } catch (error) {
      if (error.status !== 404) throw error;
      summary.already_deleted.push(version.id);
    }
  }
  return summary;
}

async function main() {
  const repository = process.env.GITHUB_REPOSITORY;
  const api = new GitHubApi({ apiUrl: process.env.GITHUB_API_URL || "https://api.github.com", token: process.env.GITHUB_TOKEN, repository });
  const event = JSON.parse(fs.readFileSync(process.env.GITHUB_EVENT_PATH, "utf8"));
  const eventName = process.env.GITHUB_EVENT_NAME;
  if (!["schedule", "workflow_run"].includes(eventName)) throw new Error("unsupported development cleanup event");
  const run = eventName === "workflow_run" ? event.workflow_run : null;
  if (eventName === "workflow_run" && !run) throw new Error("development cleanup event is missing its run");
  const summary = await cleanupDevelopmentImages({
    api, repository, run, registry: new RegistryClient({ image: imageName(repository) }),
  });
  const rendered = JSON.stringify(summary, null, 2);
  console.log(rendered);
  if (process.env.GITHUB_STEP_SUMMARY) {
    fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY, `### Development image cleanup\n\n\`\`\`json\n${rendered}\n\`\`\`\n`);
  }
}

if (require.main === module) {
  main().catch((error) => {
    console.error(`Development image cleanup failed: ${error.message}`);
    process.exitCode = 1;
  });
}

module.exports = { ownedVersion, completedOwner, cleanupDevelopmentImages };
