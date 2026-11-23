import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { tmpdir } from "node:os";
import test from "node:test";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { hashFile, postgresImage, publishSeed, sealSeed, seedBuildVersion, seedReference, seedRepository, verifySeedFiles } from "../../../.github/scripts/evaluation-seeds.mjs";
import { docker } from "./workflow.mjs";

const exec = promisify(execFile);
const fixturePath = fileURLToPath(import.meta.url);
const repoRoot = fileURLToPath(new URL("../../../", import.meta.url));
const workerPath = path.relative(repoRoot, fixturePath);
const registryHash = "c".repeat(64);
const nativeFetch = globalThis.fetch;
const ghcr = "ghcr.io/";

function registryClient(address) {
  const endpoint = "http://" + address + "/v2/" + seedRepository.slice(ghcr.length);
  const inspect = async (tag) => {
    const response = await nativeFetch(endpoint + "/manifests/" + tag, {
      method: "HEAD",
      headers: { Accept: [
        "application/vnd.oci.image.index.v1+json",
        "application/vnd.docker.distribution.manifest.list.v2+json",
        "application/vnd.oci.image.manifest.v1+json",
        "application/vnd.docker.distribution.manifest.v2+json",
      ].join(", ") },
      signal: AbortSignal.timeout(5000),
    });
    if (response.status === 404) return null;
    assert.ok(response.ok, String(response.status));
    return response.headers.get("docker-content-digest");
  };
  return {
    inspect,
    github: {
      async request(route) {
        assert.match(route, /\/dense-mem-eval-postgres\/versions$/);
        const response = await nativeFetch(endpoint + "/tags/list", { signal: AbortSignal.timeout(5000) });
        assert.ok(response.ok);
        const { tags } = await response.json();
        const versions = new Map();
        for (const tag of tags) {
          const digest = await inspect(tag);
          if (!versions.has(digest)) versions.set(digest, []);
          versions.get(digest).push(tag);
        }
        return { data: Array.from(versions, ([name, tags]) => ({ name, metadata: { container: { tags } } })) };
      },
    },
  };
}

async function until(predicate) {
  for (let attempt = 0; attempt < 200; attempt++) {
    if (await predicate()) return;
    await delay(100);
  }
  assert.fail("seed registry or publication barrier was not reached");
}

async function worker(address, publisherDirectory, dataset, selected, discriminator, bootableImage, predecessor, barrier = "") {
  const { github } = registryClient(address);
  globalThis.fetch = (input, options) => {
    const url = new URL(input);
    assert.equal(url.hostname, "ghcr.io");
    if (url.pathname === "/token") return Promise.resolve(Response.json({ token: "disposable-registry" }));
    return nativeFetch("http://" + address + url.pathname, options);
  };
  const localDocker = (args, options) => docker(args.map(arg => arg.startsWith(seedRepository) ? address + "/" + arg.slice(ghcr.length) : arg), options);
  const directory = process.cwd();
  const runID = "local-" + discriminator.repeat(32) + "-" + dataset;
  await verifySeedFiles(directory, dataset, registryHash, dataset === "bootstrap" ? 0 : 1);
  let gates = 0;
  try {
    const image = await publishSeed(localDocker, directory, dataset, selected, {}, async () => {
      if (++gates === 2 && barrier) {
        await fs.writeFile(barrier, "ready");
        await until(() => fs.access(barrier + ".release").then(() => true, () => false));
      }
    }, { runID, previousLatest: predecessor || null, bootableImage }, { github, publisherDirectory });
    process.stdout.write(JSON.stringify({ image }) + "\n");
  } catch (error) {
    process.stdout.write(JSON.stringify({ error: error.message, diagnostic: error.diagnostic }) + "\n");
  }
}

if (process.argv[2] === "--publisher") {
  await worker(...process.argv.slice(3));
} else {
  test("seed publication serializes independent builders against a real OCI registry", { timeout: 150000 }, async (t) => {
    const directory = await fs.mkdtemp(path.join(tmpdir(), "dense-mem-publisher-"));
    const worktrees = [];
    t.after(async () => {
      for (const worktree of worktrees) {
        await fs.rm(path.join(worktree, ".seed-exports"), { recursive: true, force: true });
        await exec("git", ["-C", repoRoot, "worktree", "remove", worktree]);
      }
      await fs.rm(directory, { recursive: true });
    });
    for (const name of ["first-builder", "second-builder"]) {
      const worktree = path.join(directory, name);
      await exec("git", ["-C", repoRoot, "worktree", "add", "--quiet", "--detach", worktree, "HEAD"]);
      worktrees.push(worktree);
    }
    const container = docker(["run", "--rm", "-d", "-p", "127.0.0.1::5000", "registry:2"]);
    t.after(() => docker(["stop", container]));
    const address = docker(["port", container, "5000/tcp"]);
    await until(() => nativeFetch("http://" + address + "/v2/", { signal: AbortSignal.timeout(1000) }).then(response => response.ok, () => false));
    const { inspect } = registryClient(address);
    const locks = path.join(directory, "publisher-state");

    async function build(worktree, dataset, identity, date) {
      const working = path.join(worktree, ".seed-exports", identity);
      await fs.mkdir(working, { recursive: true });
      const dump = path.join(working, "database.dump");
      const mapping = path.join(working, "mapping.json");
      await fs.writeFile(dump, identity);
      await fs.writeFile(mapping, "{}");
      await fs.writeFile(path.join(working, "NOTICE.md"), "Synthetic registry test\n");
      const repeatedID = "12345678-0000-0000-0000-000000000000";
      await fs.writeFile(path.join(working, "seed.json"), JSON.stringify({
        version: 1, dataset, registry_sha256: registryHash, corpus_rows: dataset === "bootstrap" ? 0 : 1,
        base_release: "v2.8.1", source_revision: "a".repeat(40), postgres_image: postgresImage,
        embedding_model: "text-embedding-3-small", embedding_dimensions: 1536, schema_version: 1,
        scope: { team_id: repeatedID, credential_id: repeatedID, owner_id: repeatedID, space_id: repeatedID, generation: 1 },
        mapping_sha256: await hashFile(mapping), canonical_sha256: "d".repeat(64), search_sha256: "e".repeat(64),
      }));
      await sealSeed(working);
      const selected = seedBuildVersion("v2.8.1", new Date(date));
      const imageTag = address + "/" + seedReference(dataset, selected).slice(ghcr.length);
      const builtTag = address + "/test-seed:" + identity;
      let bootableImage = "";
      return {
        selected, imageTag, builtTag,
        publish: async (predecessor = "", barrier = "") => {
          if (dataset !== "bootstrap" && !bootableImage) {
            await fs.writeFile(path.join(working, "Dockerfile"),
              'FROM scratch\nLABEL io.dense-mem.eval.seed-format="postgres-v2"\nCOPY database.dump /seed/database.dump\n');
            docker(["build", "--quiet", "--network=none", "--platform=linux/amd64", "--tag", builtTag, working]);
            bootableImage = docker(["image", "inspect", "--format", "{{.Id}}", builtTag]);
          }
          const { stdout } = await exec(process.execPath, [
            path.join(worktree, workerPath), "--publisher", address, locks, dataset, selected, identity, bootableImage, predecessor, barrier,
          ], {
            cwd: working, timeout: 60000, env: process.env,
          });
          return JSON.parse(stdout);
        },
      };
    }

    const older = await build(worktrees[0], "hotpotqa", "a", "2026-11-16T17:00:00Z");
    const newer = await build(worktrees[1], "hotpotqa", "b", "2026-11-16T18:00:00Z");
    const firstBootstrap = await build(worktrees[0], "bootstrap", "c", "2026-11-16T17:00:00Z");
    const secondBootstrap = await build(worktrees[1], "bootstrap", "d", "2026-11-16T18:00:00Z");
    const localTags = [older, newer, firstBootstrap, secondBootstrap].flatMap(build => [build.imageTag, build.builtTag]);
    localTags.push(address + "/" + seedReference("hotpotqa").slice(ghcr.length), address + "/" + seedReference("bootstrap").slice(ghcr.length));
    t.after(() => {
      for (const tag of localTags) {
        try { docker(["image", "inspect", tag]); } catch { continue; }
        docker(["image", "rm", tag]);
      }
    });

    const barrier = path.join(directory, "dataset-barrier");
    const promoting = newer.publish("", barrier);
    let winner;
    try {
      await until(() => fs.access(barrier).then(() => true, () => false));
      const conflict = await older.publish();
      assert.match(conflict.error, /Another build is publishing/);
      assert.equal(conflict.diagnostic.alias, seedReference("hotpotqa"));
      assert.equal(await inspect("hotpotqa-" + older.selected), null);
    } finally {
      await fs.writeFile(barrier + ".release", "resume");
      winner = await promoting;
    }
    assert.ok(winner.image, JSON.stringify(winner));
    const newestDigest = await inspect("hotpotqa-" + newer.selected);
    assert.match(newestDigest, /^sha256:/);
    assert.equal(winner.image, seedRepository + "@" + newestDigest);
    const stale = await older.publish(winner.image);
    assert.match(stale.error, /newer or equal generation/);
    assert.equal(stale.diagnostic.observed_digest, winner.image);
    const olderDigest = await inspect("hotpotqa-" + older.selected);
    assert.match(olderDigest, /^sha256:/);
    assert.notEqual(olderDigest, newestDigest);
    assert.equal(await inspect("hotpotqa-latest"), newestDigest);
    assert.deepEqual(await newer.publish(), winner);
    assert.equal(await inspect("hotpotqa-" + older.selected), olderDigest);

    const bootstrapBarrier = path.join(directory, "bootstrap-barrier");
    const bootstrap = firstBootstrap.publish("", bootstrapBarrier);
    let first;
    try {
      await until(() => fs.access(bootstrapBarrier).then(() => true, () => false));
      assert.match((await secondBootstrap.publish()).error, /Another build is publishing/);
    } finally {
      await fs.writeFile(bootstrapBarrier + ".release", "resume");
      first = await bootstrap;
    }
    const bootstrapDigest = await inspect("bootstrap-" + firstBootstrap.selected);
    assert.equal(first.image, seedRepository + "@" + bootstrapDigest);
    assert.match((await secondBootstrap.publish()).error, /alias changed/);
    assert.match(await inspect("bootstrap-" + secondBootstrap.selected), /^sha256:/);
    assert.equal(await inspect("bootstrap-latest"), bootstrapDigest);
    assert.deepEqual(await firstBootstrap.publish(), first);
  });
}
