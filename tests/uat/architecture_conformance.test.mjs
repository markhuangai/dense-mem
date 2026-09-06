import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  checkGoEdges,
  discoverBrowser,
  discoverGo,
  discoverWorkers,
  evaluateModuleEdge,
  isModuleImport,
  loadManifest,
  parseArgs,
  resolveBrowserImport,
  scanGoTokens,
  validateManifest,
} from "../../scripts/check-architecture.mjs";

const root = path.resolve(import.meta.dirname, "../..");
const productionManifest = loadManifest(root);

function fixture(name) {
  return JSON.parse(fs.readFileSync(path.join(root, "architecture/fixtures", name), "utf8"));
}

function fixtureManifest() {
  return {
    schema_version: 1,
    completed_issues: [260],
    module: "fixture",
    allowed_targets: productionManifest.allowed_targets,
    go: {
      profiles: ["production"],
      units: [
        {id: "fixture/composition", capability: "fixture", role: "composition", visibility: "public"},
        {id: "fixture/transport", capability: "fixture", role: "transport", visibility: "public"},
        {id: "fixture/postgres", capability: "fixture", role: "adapter", visibility: "private"},
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

function assertEdgeExpectation(edge, result) {
  if (edge.expected === "allowed") {
    assert.deepEqual(result.diagnostics, []);
    return;
  }
  assert.equal(result.diagnostics.length, 1);
  assert.match(result.diagnostics[0], new RegExp(`^${edge.expected}:`));
}

function copyManifestFixture() {
  const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), "dense-mem-architecture-"));
  fs.mkdirSync(path.join(fixtureRoot, "architecture/modules"), { recursive: true });
  const rootManifest = JSON.parse(fs.readFileSync(path.join(root, "architecture/ownership.v1.json"), "utf8"));
  fs.writeFileSync(path.join(fixtureRoot, "architecture/ownership.v1.json"), JSON.stringify(rootManifest, null, 2));
  for (const reference of rootManifest.fragments) {
    fs.copyFileSync(path.join(root, reference), path.join(fixtureRoot, reference));
  }
  return { fixtureRoot, rootManifest };
}

test("loads the complete independently owned architecture inventory", () => {
  assert.equal(productionManifest.load_diagnostics.length, 0);
  assert.equal(productionManifest.fragments.length, 51);
  assert.equal(productionManifest.go.units.length, 57);
  assert.equal(productionManifest.browser.units.length, 40);
  assert.equal(productionManifest.exceptions.length, 25);
  assert.equal(productionManifest.workers.length, 52);
  assert.deepEqual(productionManifest.completed_issues, [260, 347, 261, 262, 263, 348]);
  assert.deepEqual(validateManifest(productionManifest), []);
});

test("rejects missing, unlisted, and duplicate capability fragments", () => {
  const absent = copyManifestFixture();
  try {
    delete absent.rootManifest.fragments;
    fs.writeFileSync(
      path.join(absent.fixtureRoot, "architecture/ownership.v1.json"),
      JSON.stringify(absent.rootManifest, null, 2),
    );
    const loaded = loadManifest(absent.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.startsWith("invalid-fragment:")));
  } finally {
    fs.rmSync(absent.fixtureRoot, { recursive: true, force: true });
  }

  const empty = copyManifestFixture();
  try {
    empty.rootManifest.fragments = [];
    fs.writeFileSync(
      path.join(empty.fixtureRoot, "architecture/ownership.v1.json"),
      JSON.stringify(empty.rootManifest, null, 2),
    );
    const loaded = loadManifest(empty.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.startsWith("invalid-fragment:")));
  } finally {
    fs.rmSync(empty.fixtureRoot, { recursive: true, force: true });
  }

  const rootOwned = copyManifestFixture();
  try {
    rootOwned.rootManifest.completed_issues = [381];
    fs.writeFileSync(
      path.join(rootOwned.fixtureRoot, "architecture/ownership.v1.json"),
      JSON.stringify(rootOwned.rootManifest, null, 2),
    );
    const loaded = loadManifest(rootOwned.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("root manifest must not define completed_issues")));
  } finally {
    fs.rmSync(rootOwned.fixtureRoot, { recursive: true, force: true });
  }

  const missing = copyManifestFixture();
  try {
    missing.rootManifest.fragments.push("architecture/modules/missing.json");
    fs.writeFileSync(
      path.join(missing.fixtureRoot, "architecture/ownership.v1.json"),
      JSON.stringify(missing.rootManifest, null, 2),
    );
    const loaded = loadManifest(missing.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.startsWith("missing-fragment:")));
  } finally {
    fs.rmSync(missing.fixtureRoot, { recursive: true, force: true });
  }

  const unlisted = copyManifestFixture();
  try {
    fs.copyFileSync(
      path.join(unlisted.fixtureRoot, "architecture/modules/architecture.json"),
      path.join(unlisted.fixtureRoot, "architecture/modules/unlisted.json"),
    );
    const loaded = loadManifest(unlisted.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.startsWith("unlisted-fragment:")));
  } finally {
    fs.rmSync(unlisted.fixtureRoot, { recursive: true, force: true });
  }

  const duplicate = copyManifestFixture();
  try {
    duplicate.rootManifest.fragments = [
      duplicate.rootManifest.fragments[0],
      duplicate.rootManifest.fragments[0],
      ...duplicate.rootManifest.fragments.slice(1),
    ];
    fs.writeFileSync(
      path.join(duplicate.fixtureRoot, "architecture/ownership.v1.json"),
      JSON.stringify(duplicate.rootManifest, null, 2),
    );
    const loaded = loadManifest(duplicate.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.startsWith("duplicate-fragment:")));
  } finally {
    fs.rmSync(duplicate.fixtureRoot, { recursive: true, force: true });
  }

  const duplicateCapability = copyManifestFixture();
  try {
    fs.copyFileSync(
      path.join(duplicateCapability.fixtureRoot, "architecture/modules/architecture.json"),
      path.join(duplicateCapability.fixtureRoot, "architecture/modules/architecture-copy.json"),
    );
    const duplicateFragment = JSON.parse(fs.readFileSync(
      path.join(duplicateCapability.fixtureRoot, "architecture/modules/architecture-copy.json"),
      "utf8",
    ));
    duplicateFragment.capability = "architecture";
    fs.writeFileSync(
      path.join(duplicateCapability.fixtureRoot, "architecture/modules/architecture-copy.json"),
      JSON.stringify(duplicateFragment, null, 2),
    );
    duplicateCapability.rootManifest.fragments.push("architecture/modules/architecture-copy.json");
    fs.writeFileSync(
      path.join(duplicateCapability.fixtureRoot, "architecture/ownership.v1.json"),
      JSON.stringify(duplicateCapability.rootManifest, null, 2),
    );
    const loaded = loadManifest(duplicateCapability.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.startsWith("duplicate-fragment:") && item.includes("capability architecture")));
  } finally {
    fs.rmSync(duplicateCapability.fixtureRoot, { recursive: true, force: true });
  }

  const misplaced = copyManifestFixture();
  try {
    const sourceFragmentPath = path.join(misplaced.fixtureRoot, "architecture/modules/http-transport.json");
    const sourceFragment = JSON.parse(fs.readFileSync(sourceFragmentPath, "utf8"));
    const architecturePath = path.join(misplaced.fixtureRoot, "architecture/modules/architecture.json");
    const architectureFragment = JSON.parse(fs.readFileSync(architecturePath, "utf8"));
    architectureFragment.exceptions.push(sourceFragment.exceptions[0]);
    architectureFragment.workers.push(sourceFragment.workers[0]);
    fs.writeFileSync(architecturePath, JSON.stringify(architectureFragment, null, 2));
    const loaded = loadManifest(misplaced.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("exception") && item.includes("must be owned by")));
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("worker") && item.includes("must be owned by")));
  } finally {
    fs.rmSync(misplaced.fixtureRoot, { recursive: true, force: true });
  }
});

test("rejects a fragment whose capability does not match its filename", () => {
  const fixtureCopy = copyManifestFixture();
  try {
    const fragmentPath = path.join(fixtureCopy.fixtureRoot, "architecture/modules/foo.json");
    const fragment = JSON.parse(fs.readFileSync(
      path.join(fixtureCopy.fixtureRoot, "architecture/modules/architecture.json"),
      "utf8",
    ));
    fragment.capability = "bar";
    fs.writeFileSync(fragmentPath, JSON.stringify(fragment, null, 2));
    fixtureCopy.rootManifest.fragments.push("architecture/modules/foo.json");
    fs.writeFileSync(
      path.join(fixtureCopy.fixtureRoot, "architecture/ownership.v1.json"),
      JSON.stringify(fixtureCopy.rootManifest, null, 2),
    );
    const loaded = loadManifest(fixtureCopy.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item === "invalid-fragment: architecture/modules/foo.json capability must match its filename foo"));
  } finally {
    fs.rmSync(fixtureCopy.fixtureRoot, { recursive: true, force: true });
  }
});

test("rejects duplicate Go and browser unit ownership across fragments", () => {
  const fixtureCopy = copyManifestFixture();
  try {
    const architecturePath = path.join(fixtureCopy.fixtureRoot, "architecture/modules/architecture.json");
    const architectureFragment = JSON.parse(fs.readFileSync(architecturePath, "utf8"));
    const postgresFragment = JSON.parse(fs.readFileSync(
      path.join(fixtureCopy.fixtureRoot, "architecture/modules/postgres-storage-adapter.json"),
      "utf8",
    ));
    const controlFragment = JSON.parse(fs.readFileSync(
      path.join(fixtureCopy.fixtureRoot, "architecture/modules/control-portal.json"),
      "utf8",
    ));
    architectureFragment.go.units.push(postgresFragment.go.units[0]);
    architectureFragment.browser.units.push(controlFragment.browser.units[0]);
    fs.writeFileSync(architecturePath, JSON.stringify(architectureFragment, null, 2));
    const loaded = loadManifest(fixtureCopy.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.startsWith("duplicate-fragment:") && item.includes("internal/repository")));
    assert.ok(loaded.load_diagnostics.some((item) => item.startsWith("duplicate-fragment:") && item.includes("web/src/")));
  } finally {
    fs.rmSync(fixtureCopy.fixtureRoot, { recursive: true, force: true });
  }
});

test("rejects fragment units that try to override inherited capability", () => {
  const fixtureCopy = copyManifestFixture();
  try {
    const postgresPath = path.join(fixtureCopy.fixtureRoot, "architecture/modules/postgres-storage-adapter.json");
    const postgresFragment = JSON.parse(fs.readFileSync(postgresPath, "utf8"));
    postgresFragment.go.units[0].capability = "wrong-capability";
    fs.writeFileSync(postgresPath, JSON.stringify(postgresFragment, null, 2));

    const controlPath = path.join(fixtureCopy.fixtureRoot, "architecture/modules/control-portal.json");
    const controlFragment = JSON.parse(fs.readFileSync(controlPath, "utf8"));
    controlFragment.browser.units[0].capability = "wrong-capability";
    fs.writeFileSync(controlPath, JSON.stringify(controlFragment, null, 2));

    const loaded = loadManifest(fixtureCopy.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("Go unit") && item.includes("must inherit capability")));
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("browser unit") && item.includes("must inherit capability")));
  } finally {
    fs.rmSync(fixtureCopy.fixtureRoot, { recursive: true, force: true });
  }
});

test("units inherit capability ownership from their fragment", () => {
  const unit = productionManifest.go.units.find((entry) => entry.id.endsWith("/internal/repository"));
  assert.equal(unit.capability, "postgres-storage-adapter");
  assert.equal(unit.role, "postgres_adapter");
  assert.equal(unit.visibility, "private");
});

test("enforces private visibility and narrow PostgreSQL infrastructure reuse", () => {
  const unit = (id, role, capability, visibility) => ({ id, role, capability, visibility });
  const privateCrossCapability = evaluateModuleEdge(
    unit("source", "application_api", "source-capability", "public"),
    unit("target", "application_api", "target-capability", "private"),
  );
  assert.equal(privateCrossCapability.ok, false);
  assert.match(privateCrossCapability.diagnostic, /^private:/);

  const publicCrossCapability = evaluateModuleEdge(
    unit("source", "transport", "source-capability", "private"),
    unit("target", "transport", "target-capability", "public"),
  );
  assert.equal(publicCrossCapability.ok, true);

  const compositionPrivate = evaluateModuleEdge(
    unit("composition", "composition", "composition-capability", "public"),
    unit("adapter", "adapter", "adapter-capability", "private"),
  );
  assert.equal(compositionPrivate.ok, true);

  const postgresInfrastructure = evaluateModuleEdge(
    unit("postgres-adapter", "postgres_adapter", "storage-adapter", "private"),
    unit("postgres-infrastructure", "postgres_infrastructure", "storage-infrastructure", "private"),
  );
  assert.equal(postgresInfrastructure.ok, true);

  const workerToWorker = evaluateModuleEdge(
    unit("worker-source", "worker", "source-capability", "private"),
    unit("worker-target", "worker", "target-capability", "private"),
  );
  assert.equal(workerToWorker.ok, false);
  assert.match(workerToWorker.diagnostic, /^forbidden:/);

  const sameCapabilityPrivate = evaluateModuleEdge(
    unit("private-source", "application_api", "same-capability", "private"),
    unit("private-target", "application_api", "same-capability", "private"),
  );
  assert.equal(sameCapabilityPrivate.ok, true);

  for (const role of ["application_api", "transport", "adapter"]) {
    const forbidden = evaluateModuleEdge(
      unit(`${role}-source`, role, `${role}-capability`, "public"),
      unit("postgres-infrastructure", "postgres_infrastructure", "storage-infrastructure", "private"),
    );
    assert.equal(forbidden.ok, false);
    assert.match(forbidden.diagnostic, /^forbidden:/);
  }

  const publicInfrastructure = structuredClone(productionManifest);
  publicInfrastructure.go.units.find((entry) => entry.role === "postgres_infrastructure").visibility = "public";
  assert.ok(validateManifest(publicInfrastructure).some((item) => item.includes("postgres_infrastructure") && item.includes("must be private")));
});

test("retains precise replacement owners and lifecycle obligations", () => {
  const exceptionOwners = [...new Set(productionManifest.exceptions.map((entry) => entry.removal_issue))].sort((a, b) => a - b);
  assert.deepEqual(exceptionOwners, [363, 365, 366, 367, 368, 370, 375, 376, 377, 379, 380, 382]);
  assert.equal(productionManifest.exceptions.some((entry) => entry.removal_issue === 276), false);
  assert.equal(productionManifest.exceptions.some((entry) => entry.removal_issue === 280), false);
  assert.ok(productionManifest.workers.every((entry) => entry.lifecycle_issue === 381));
});

test("allows composition-to-adapter edges", () => {
  const edge = fixture("allowed-edge.json");
  assertEdgeExpectation(edge, checkGoEdges(fixtureManifest(), [edge]));
});

test("rejects a transport-to-PostgreSQL falsification edge", () => {
  const edge = fixture("forbidden-transport-postgres.json");
  assertEdgeExpectation(edge, checkGoEdges(fixtureManifest(), [edge]));
});

test("rejects an unclassified package", () => {
  const edge = fixture("unclassified-package.json");
  assertEdgeExpectation(edge, checkGoEdges(fixtureManifest(), [edge]));
});

test("rejects an exception owned by a completed issue", () => {
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

test("completion membership is independent of issue order", () => {
  const makeManifest = (completedIssues) => {
    const manifest = structuredClone(productionManifest);
    manifest.completed_issues = completedIssues;
    manifest.exceptions = [{
      source: "github.com/markhuangai/dense-mem/internal/repository",
      target: "github.com/markhuangai/dense-mem/internal/storage/postgres",
      removal_issue: 261,
      reason: "fixture exception",
    }];
    return manifest;
  };
  const ordered = validateManifest(makeManifest([260, 262]));
  const reversed = validateManifest(makeManifest([262, 260]));
  assert.deepEqual(reversed, ordered);
  assert.equal(ordered.some((item) => item.startsWith("expired:")), false);
});

test("completing an issue expires only its retained obligations", () => {
  const completed = structuredClone(productionManifest);
  completed.completed_issues = [260, 261, 262, 263, 272];
  completed.exceptions = completed.exceptions.filter((entry) => entry.removal_issue !== 272);
  assert.deepEqual(validateManifest(completed), []);

  const retained = structuredClone(completed);
  retained.exceptions.push({
    source: "github.com/markhuangai/dense-mem/internal/service/skillpackservice",
    target: "github.com/markhuangai/dense-mem/internal/repository",
    removal_issue: 272,
    reason: "fixture retained obligation",
  });
  assert.ok(validateManifest(retained).some((item) => item.includes("completed issue 272")));
});

test("rejects malformed completion membership", () => {
  const manifest = structuredClone(productionManifest);
  manifest.completed_issues = [260, 260, 0, "261"];
  const diagnostics = validateManifest(manifest);
  assert.ok(diagnostics.some((item) => item.startsWith("duplicate: completed_issues")));
  assert.ok(diagnostics.filter((item) => item.includes("completed_issues contains an invalid issue number")).length >= 2);
});

test("rejects missing completion membership and malformed lifecycle metadata", () => {
  const missing = structuredClone(productionManifest);
  delete missing.completed_issues;
  assert.ok(validateManifest(missing).some((item) => item.includes("completed_issues must be an array")));

  const malformed = structuredClone(productionManifest);
  malformed.workers[0].lifecycle_issue = 0;
  malformed.workers[1].lifecycle_issue = "277";
  const diagnostics = validateManifest(malformed);
  assert.ok(diagnostics.some((item) => item.includes("lifecycle_issue must be a positive issue number")));
});

test("rejects obsolete completion and worker lifecycle fields", () => {
  const manifest = structuredClone(productionManifest);
  manifest.enforced_through_issue = 263;
  manifest.workers[0].owner_issue = 277;
  const diagnostics = validateManifest(manifest);
  assert.ok(diagnostics.some((item) => item.includes("enforced_through_issue is obsolete")));
  assert.ok(diagnostics.some((item) => item.includes("uses obsolete owner_issue")));
});

test("completed worker lifecycle obligations fail while permanent anchors remain valid", () => {
  const expiring = structuredClone(productionManifest);
  expiring.completed_issues = [260, 261, 262, 263, 381];
  assert.ok(validateManifest(expiring).some((item) => item.includes("worker") && item.includes("completed issue 381")));

  const permanent = structuredClone(productionManifest);
  permanent.completed_issues = [260, 261, 262, 263, 381];
  permanent.workers = [permanent.workers[0]];
  delete permanent.workers[0].lifecycle_issue;
  assert.equal(validateManifest(permanent).some((item) => item.includes("worker") && item.startsWith("expired:")), false);
});

test("rejects Go profiles that the checker cannot discover", () => {
  const manifest = structuredClone(productionManifest);
  manifest.go.profiles = ["production", "integration"];
  assert.ok(validateManifest(manifest).some((item) => item.includes("go.profiles must exactly match")));
});

test("rejects browser exclusions that are not test-only modules", () => {
  const manifest = structuredClone(productionManifest);
  manifest.browser.exclusions = ["web/src/App.tsx"];
  assert.ok(validateManifest(manifest).some((item) => item.includes("browser exclusion web/src/App.tsx must target a test-only module")));
});

test("rejects a manifest module that differs from go.mod", () => {
  const manifest = structuredClone(productionManifest);
  manifest.module = `${productionManifest.module}/internal`;
  assert.ok(validateManifest(manifest, productionManifest.module).some((item) => item.includes("module must match go.mod module declaration")));
});

test("discovers both Go profiles, browser entry graph, and worker anchors", async () => {
  const go = discoverGo(root, productionManifest.module);
  assert.ok(go.packages.includes(`${productionManifest.module}/cmd/server`));
  assert.ok(go.packages.includes(`${productionManifest.module}/cmd/eval-runner`));
  assert.ok(go.edges.some((edge) => edge.profile === "evaluation"));
  const productionOnly = discoverGo(root, productionManifest.module, ["production"]);
  assert.equal(productionOnly.packages.includes(`${productionManifest.module}/cmd/eval-runner`), false);

  const browser = await discoverBrowser(root, productionManifest);
  assert.ok(browser.files.includes("web/src/main.tsx"));
  assert.ok(browser.files.includes("web/src/user/main.tsx"));
  assert.equal(browser.diagnostics.length, 0);

  const workers = discoverWorkers(root);
  assert.ok(workers.length > 0);
  assert.ok(workers.some((worker) => worker.path === "cmd/oauth-compat-harness/main.go"));
  assert.ok(workers.some((worker) => worker.path === "internal/http/server.go"));
  assert.ok(workers.some((worker) => worker.path === "internal/sse/lifecycle.go"));
  assert.ok(workers.every((worker) => productionManifest.workers.some((entry) => (
    entry.path === worker.path
      && entry.function === worker.function
      && entry.kind === worker.kind
      && entry.ordinal === worker.ordinal
  ))));
});

test("traverses statically analyzable Vite glob modules", async () => {
  const fixtureName = `architecture-glob-${process.pid}-${Date.now()}.tsx`;
  const fixturePath = path.join(root, "web/src", fixtureName);
  fs.writeFileSync(fixturePath, `export const modules = import.meta.glob(["./control/*.tsx", "!./control/*.test.tsx"]);\n`);
  try {
    const manifest = structuredClone(productionManifest);
    manifest.browser.entries = [`web/src/${fixtureName}`];
    const browser = await discoverBrowser(root, manifest);
    assert.equal(browser.diagnostics.length, 0);
    assert.ok(browser.files.includes("web/src/control/ConfigPanel.tsx"));
    assert.equal(browser.files.some((filePath) => filePath.endsWith(".test.tsx")), false);
  } finally {
    fs.rmSync(fixturePath, { force: true });
  }
});

test("resolves root-absolute Vite globs from the web root", async () => {
  const fixtureName = `architecture-root-glob-${process.pid}-${Date.now()}.tsx`;
  const fixturePath = path.join(root, "web/src", fixtureName);
  fs.writeFileSync(fixturePath, `export const modules = import.meta.glob("/src/control/*.tsx");\n`);
  try {
    const manifest = structuredClone(productionManifest);
    manifest.browser.entries = [`web/src/${fixtureName}`];
    const browser = await discoverBrowser(root, manifest);
    assert.equal(browser.diagnostics.length, 0);
    assert.ok(browser.files.includes("web/src/control/ConfigPanel.tsx"));
  } finally {
    fs.rmSync(fixturePath, { force: true });
  }
});

test("rejects exclusions reached from production browser entries", async () => {
  const suffix = `${process.pid}-${Date.now()}`;
  const entryName = `architecture-exclusion-entry-${suffix}.tsx`;
  const excludedName = `architecture-production-${suffix}.test.tsx`;
  const entryPath = path.join(root, "web/src", entryName);
  const excludedPath = path.join(root, "web/src/test", excludedName);
  fs.writeFileSync(entryPath, `import "./test/${excludedName}";\n`);
  fs.writeFileSync(excludedPath, "export const hidden = true;\n");
  try {
    const manifest = structuredClone(productionManifest);
    manifest.browser.entries = [`web/src/${entryName}`];
    manifest.browser.exclusions = [`web/src/test/${excludedName}`];
    const browser = await discoverBrowser(root, manifest);
    assert.ok(browser.diagnostics.some((item) => item.includes(`browser exclusion web/src/test/${excludedName} is reachable from a production entry`)));
  } finally {
    fs.rmSync(entryPath, { force: true });
    fs.rmSync(excludedPath, { force: true });
  }
});

test("fails closed on variable dynamic imports", async () => {
  const fixtureName = `architecture-dynamic-import-${process.pid}-${Date.now()}.tsx`;
  const fixturePath = path.join(root, "web/src", fixtureName);
  fs.writeFileSync(fixturePath, `const panel = "ConfigPanel"; export const module = import(\`./control/\${panel}.tsx\`);\n`);
  try {
    const manifest = structuredClone(productionManifest);
    manifest.browser.entries = [`web/src/${fixtureName}`];
    const browser = await discoverBrowser(root, manifest);
    assert.ok(browser.diagnostics.some((item) => item.startsWith("unsupported-import:")));
  } finally {
    fs.rmSync(fixturePath, { force: true });
  }
});

test("traverses statically analyzable Vite Worker modules", async () => {
  const fixtureName = `architecture-worker-import-${process.pid}-${Date.now()}.tsx`;
  const fixturePath = path.join(root, "web/src", fixtureName);
  fs.writeFileSync(fixturePath, `export const worker = new Worker(new URL("./control/ConfigPanel.tsx", import.meta.url), { type: "module" });\n`);
  try {
    const manifest = structuredClone(productionManifest);
    manifest.browser.entries = [`web/src/${fixtureName}`];
    const browser = await discoverBrowser(root, manifest);
    assert.equal(browser.diagnostics.length, 0);
    assert.ok(browser.files.includes("web/src/control/ConfigPanel.tsx"));
  } finally {
    fs.rmSync(fixturePath, { force: true });
  }
});

test("traverses statically analyzable Vite SharedWorker modules", async () => {
  const fixtureName = `architecture-shared-worker-import-${process.pid}-${Date.now()}.tsx`;
  const fixturePath = path.join(root, "web/src", fixtureName);
  fs.writeFileSync(fixturePath, `export const worker = new SharedWorker(new URL("./control/ConfigPanel.tsx", import.meta.url), { type: "module" });\n`);
  try {
    const manifest = structuredClone(productionManifest);
    manifest.browser.entries = [`web/src/${fixtureName}`];
    const browser = await discoverBrowser(root, manifest);
    assert.equal(browser.diagnostics.length, 0);
    assert.ok(browser.files.includes("web/src/control/ConfigPanel.tsx"));
  } finally {
    fs.rmSync(fixturePath, { force: true });
  }
});

test("fails closed on malformed Vite Worker constructors", async () => {
  const fixtureName = `architecture-worker-malformed-${process.pid}-${Date.now()}.tsx`;
  const fixturePath = path.join(root, "web/src", fixtureName);
  fs.writeFileSync(fixturePath, "export const worker = new Worker(new URL);\n");
  try {
    const manifest = structuredClone(productionManifest);
    manifest.browser.entries = [`web/src/${fixtureName}`];
    const browser = await discoverBrowser(root, manifest);
    assert.ok(browser.diagnostics.some((item) => item.startsWith("unsupported-worker:")));
  } finally {
    fs.rmSync(fixturePath, { force: true });
  }
});

test("rejects missing checker option values", () => {
  assert.throws(() => parseArgs(["--root"]), /--root requires a value/);
  assert.throws(() => parseArgs(["--root", ""]), /--root requires a value/);
  assert.throws(() => parseArgs(["--manifest"]), /--manifest requires a value/);
  assert.throws(() => parseArgs(["--root", "--manifest"]), /--root requires a value/);
});

test("resolves browser JavaScript aliases and ignores asset imports", () => {
  const importer = path.join(root, "web/src/main.tsx");
  const alias = resolveBrowserImport(root, importer, "./App.js?import");
  assert.equal(alias.file, path.join(root, "web/src/App.tsx"));
  assert.deepEqual(resolveBrowserImport(root, importer, "./logo.svg?url"), { asset: true });
});

test("resolves Vite root-absolute browser imports inside web", () => {
  const importer = path.join(root, "web/src/main.tsx");
  const resolved = resolveBrowserImport(root, importer, "/src/control/ConfigPanel.tsx");
  assert.equal(resolved.file, path.join(root, "web/src/control/ConfigPanel.tsx"));
});

test("keeps module-root imports inside the discovered graph", () => {
  assert.equal(isModuleImport("example.test/dense-mem", "example.test/dense-mem"), true);
  assert.equal(isModuleImport("example.test/dense-mem", "example.test/dense-mem/internal/service"), true);
  assert.equal(isModuleImport("example.test/dense-mem", "example.test/dense-memory"), false);
});

test("does not treat Go comments or strings as worker signals", () => {
  const tokens = scanGoTokens(`package fixture
// go ignored() and .Run()
var text = "go ignored() .Run()"
func run() { go work(); client.Run() }
`);
  assert.equal(tokens.filter((token) => token.text === "go").length, 1);
  assert.equal(tokens.filter((token) => token.text === "Run").length, 1);
});

test("keeps worker scope across composite return types and anonymous results", () => {
  const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), "architecture-worker-"));
  try {
    const fixtureDirectory = path.join(fixtureRoot, "cmd", "fixture");
    fs.mkdirSync(fixtureDirectory, { recursive: true });
    fs.writeFileSync(path.join(fixtureDirectory, "main.go"), `package fixture
import "context"
var _ = context.Background
func returnsStruct() struct { value int } { go work() }
func literal() { go func() error { return nil }() }
func parenthesized() { go (work)() }
func selector(worker *workerType) { go (*worker).serve() }
func composite() { go []func(){work}[0]() }
func unicode() { go 启动() }
func work() {}
func 启动() {}
type workerType struct{}
func (workerType) serve() {}
`);
    assert.deepEqual(discoverWorkers(fixtureRoot).map((worker) => ({
      function: worker.function,
      kind: worker.kind,
    })), [
      { function: "returnsStruct", kind: "goroutine" },
      { function: "literal", kind: "goroutine" },
      { function: "parenthesized", kind: "goroutine" },
      { function: "selector", kind: "goroutine" },
      { function: "composite", kind: "goroutine" },
      { function: "unicode", kind: "goroutine" },
    ]);
  } finally {
    fs.rmSync(fixtureRoot, { recursive: true, force: true });
  }
});
