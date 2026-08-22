import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";

import {
  checkGoEdges,
  discoverBrowser,
  discoverGo,
  discoverWorkers,
  validateManifest,
} from "../../scripts/check-architecture.mjs";

const root = path.resolve(import.meta.dirname, "../..");
const productionManifest = JSON.parse(fs.readFileSync(path.join(root, "architecture/ownership.v1.json"), "utf8"));

function fixture(name) {
  return JSON.parse(fs.readFileSync(path.join(root, "architecture/fixtures", name), "utf8"));
}

function fixtureManifest() {
  return {
    schema_version: 1,
    enforced_through_issue: 260,
    module: "fixture",
    allowed_targets: productionManifest.allowed_targets,
    go: {
      profiles: ["production"],
      units: [
        {id: "fixture/composition", capability: "fixture", role: "composition"},
        {id: "fixture/transport", capability: "fixture", role: "transport"},
        {id: "fixture/postgres", capability: "fixture", role: "adapter"},
      ],
    },
    browser: {
      entries: ["fixture.ts"],
      exclusions: [],
      units: [],
    },
    exceptions: [],
    workers: [],
  };
}

test("allows composition-to-adapter edges", () => {
  const edge = fixture("allowed-edge.json");
  const result = checkGoEdges(fixtureManifest(), [edge]);
  assert.deepEqual(result.diagnostics, []);
});

test("rejects a transport-to-PostgreSQL falsification edge", () => {
  const edge = fixture("forbidden-transport-postgres.json");
  const result = checkGoEdges(fixtureManifest(), [edge]);
  assert.equal(result.diagnostics.length, 1);
  assert.match(result.diagnostics[0], /forbidden/);
});

test("rejects an unclassified package", () => {
  const edge = fixture("unclassified-package.json");
  const result = checkGoEdges(fixtureManifest(), [edge]);
  assert.equal(result.diagnostics.length, 1);
  assert.match(result.diagnostics[0], /unclassified/);
});

test("rejects an exception that expires at the enforced issue", () => {
  const edge = fixture("expired-exception.json");
  const manifest = fixtureManifest();
  manifest.exceptions = [{
    source: edge.source,
    target: edge.target,
    removal_issue: edge.removal_issue,
    reason: "fixture exception",
  }];
  assert.ok(validateManifest(manifest).some((item) => item.startsWith("expired:")));
});

test("discovers both Go profiles, browser entry graph, and worker anchors", async () => {
  const go = discoverGo(root, productionManifest.module);
  assert.ok(go.packages.includes(`${productionManifest.module}/cmd/server`));
  assert.ok(go.packages.includes(`${productionManifest.module}/cmd/eval-runner`));
  assert.ok(go.edges.some((edge) => edge.profile === "evaluation"));

  const browser = await discoverBrowser(root, productionManifest);
  assert.ok(browser.files.includes("web/src/main.tsx"));
  assert.ok(browser.files.includes("web/src/user/main.tsx"));
  assert.equal(browser.diagnostics.length, 0);

  const workers = discoverWorkers(root);
  assert.ok(workers.length > 0);
  assert.ok(workers.every((worker) => productionManifest.workers.some((entry) => entry.path === worker.path && entry.anchor === worker.anchor)));
});
