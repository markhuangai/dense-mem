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

function fixtureManifest() {
  return {
    schema_version: 2,
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
  };
}

function copyManifestFixture() {
  const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), "dense-mem-architecture-"));
  fs.mkdirSync(path.join(fixtureRoot, "architecture/modules"), { recursive: true });
  const rootManifest = JSON.parse(fs.readFileSync(path.join(root, "architecture/ownership.v2.json"), "utf8"));
  fs.writeFileSync(path.join(fixtureRoot, "architecture/ownership.v2.json"), JSON.stringify(rootManifest, null, 2));
  for (const reference of rootManifest.fragments) {
    const source = path.join(root, reference);
    const destination = path.join(fixtureRoot, reference);
    fs.copyFileSync(source, destination);
    const fragment = JSON.parse(fs.readFileSync(source, "utf8"));
    for (const ownership of fragment.source_ownership ?? []) {
      const sourcePath = path.join(root, ownership.path);
      const destinationPath = path.join(fixtureRoot, ownership.path);
      fs.mkdirSync(path.dirname(destinationPath), { recursive: true });
      fs.copyFileSync(sourcePath, destinationPath);
    }
    fs.writeFileSync(destination, JSON.stringify(fragment, null, 2));
  }
  return { fixtureRoot, rootManifest };
}

test("loads the complete independently owned architecture inventory", () => {
  assert.equal(productionManifest.load_diagnostics.length, 0);
  assert.equal(productionManifest.schema_version, 2);
  assert.equal(productionManifest.fragments.length, 63);
  assert.equal(productionManifest.source_ownership.length, 126);
  assert.equal(productionManifest.workers.length, 46);
  assert.equal(Object.hasOwn(productionManifest, "exceptions"), false);
  assert.deepEqual(validateManifest(productionManifest), []);
});

test("rejects missing, unlisted, and duplicate capability fragments", () => {
  const absent = copyManifestFixture();
  try {
    delete absent.rootManifest.fragments;
    fs.writeFileSync(
      path.join(absent.fixtureRoot, "architecture/ownership.v2.json"),
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
      path.join(empty.fixtureRoot, "architecture/ownership.v2.json"),
      JSON.stringify(empty.rootManifest, null, 2),
    );
    const loaded = loadManifest(empty.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.startsWith("invalid-fragment:")));
  } finally {
    fs.rmSync(empty.fixtureRoot, { recursive: true, force: true });
  }

  const unlisted = copyManifestFixture();
  try {
    fs.copyFileSync(
      path.join(unlisted.fixtureRoot, "architecture/modules/classification-policy.json"),
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
      path.join(duplicate.fixtureRoot, "architecture/ownership.v2.json"),
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
      path.join(duplicateCapability.fixtureRoot, "architecture/modules/classification-policy.json"),
      path.join(duplicateCapability.fixtureRoot, "architecture/modules/classification-policy-copy.json"),
    );
    const duplicateFragment = JSON.parse(fs.readFileSync(
      path.join(duplicateCapability.fixtureRoot, "architecture/modules/classification-policy-copy.json"),
      "utf8",
    ));
    duplicateCapability.rootManifest.fragments.push("architecture/modules/classification-policy-copy.json");
    fs.writeFileSync(
      path.join(duplicateCapability.fixtureRoot, "architecture/ownership.v2.json"),
      JSON.stringify(duplicateCapability.rootManifest, null, 2),
    );
    assert.equal(duplicateFragment.capability, "classification-policy");
    const loaded = loadManifest(duplicateCapability.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.startsWith("duplicate-fragment:") && item.includes("capability classification-policy")));
  } finally {
    fs.rmSync(duplicateCapability.fixtureRoot, { recursive: true, force: true });
  }

  const misplaced = copyManifestFixture();
  try {
    const sourcePath = path.join(misplaced.fixtureRoot, "architecture/modules/sse-transport.json");
    const source = JSON.parse(fs.readFileSync(sourcePath, "utf8"));
    const targetPath = path.join(misplaced.fixtureRoot, "architecture/modules/server-composition.json");
    const target = JSON.parse(fs.readFileSync(targetPath, "utf8"));
    target.workers.push(source.workers[0]);
    fs.writeFileSync(targetPath, JSON.stringify(target, null, 2));
    const loaded = loadManifest(misplaced.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("worker") && item.includes("must be owned by")));
  } finally {
    fs.rmSync(misplaced.fixtureRoot, { recursive: true, force: true });
  }
});

test("rejects retired migration metadata", () => {
  const fixtureCopy = copyManifestFixture();
  try {
    const fragmentPath = path.join(fixtureCopy.fixtureRoot, "architecture/modules/server-composition.json");
    const fragment = JSON.parse(fs.readFileSync(fragmentPath, "utf8"));
    fragment.completed_issues = [381];
    fragment.source_ownership[0].issue = 381;
    fragment.source_ownership[0].owner_issue = 381;
    fragment.workers[0].lifecycle_issue = 381;
    fs.writeFileSync(fragmentPath, JSON.stringify(fragment, null, 2));
    const loaded = loadManifest(fixtureCopy.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("completed_issues is retired")));
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("retired issue metadata")));
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("unsupported field owner_issue")));
    assert.ok(validateManifest(loaded).some((item) => item.includes("uses retired lifecycle_issue")));
  } finally {
    fs.rmSync(fixtureCopy.fixtureRoot, { recursive: true, force: true });
  }
});

test("accepts omitted optional fragment sections and rejects malformed ones", () => {
  const fixtureCopy = copyManifestFixture();
  try {
    const fragmentPath = path.join(fixtureCopy.fixtureRoot, "architecture/modules/classification-policy.json");
    const fragment = JSON.parse(fs.readFileSync(fragmentPath, "utf8"));
    delete fragment.go;
    fs.writeFileSync(fragmentPath, JSON.stringify(fragment, null, 2));
    assert.deepEqual(loadManifest(fixtureCopy.fixtureRoot).load_diagnostics, []);
  } finally {
    fs.rmSync(fixtureCopy.fixtureRoot, { recursive: true, force: true });
  }

  for (const [field, value] of [
    ["go", null],
    ["browser", {units: null}],
    ["workers", {}],
    ["source_ownership", {}],
  ]) {
    const malformed = copyManifestFixture();
    try {
      const fragmentPath = path.join(malformed.fixtureRoot, "architecture/modules/classification-policy.json");
      const fragment = JSON.parse(fs.readFileSync(fragmentPath, "utf8"));
      fragment[field] = value;
      fs.writeFileSync(fragmentPath, JSON.stringify(fragment, null, 2));
      const loaded = loadManifest(malformed.fixtureRoot);
      assert.ok(loaded.load_diagnostics.some((item) => item.includes(`${field}`)));
    } finally {
      fs.rmSync(malformed.fixtureRoot, { recursive: true, force: true });
    }
  }
});

test("rejects schema version 1 and mixed-version manifests", () => {
  const rootV1 = copyManifestFixture();
  try {
    rootV1.rootManifest.schema_version = 1;
    fs.writeFileSync(
      path.join(rootV1.fixtureRoot, "architecture/ownership.v2.json"),
      JSON.stringify(rootV1.rootManifest, null, 2),
    );
    const loaded = loadManifest(rootV1.fixtureRoot);
    assert.ok(validateManifest(loaded).some((item) => item.includes("schema_version must be 2")));
  } finally {
    fs.rmSync(rootV1.fixtureRoot, { recursive: true, force: true });
  }

  const mixed = copyManifestFixture();
  try {
    const fragmentPath = path.join(mixed.fixtureRoot, "architecture/modules/classification-policy.json");
    const fragment = JSON.parse(fs.readFileSync(fragmentPath, "utf8"));
    fragment.schema_version = 1;
    fs.writeFileSync(fragmentPath, JSON.stringify(fragment, null, 2));
    const loaded = loadManifest(mixed.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("classification-policy.json schema_version must be 2")));
  } finally {
    fs.rmSync(mixed.fixtureRoot, { recursive: true, force: true });
  }
});

test("rejects central manifest fields inside capability fragments", () => {
  const fixtureCopy = copyManifestFixture();
  try {
    const fragmentPath = path.join(fixtureCopy.fixtureRoot, "architecture/modules/control-portal.json");
    const fragment = JSON.parse(fs.readFileSync(fragmentPath, "utf8"));
    fragment.module = "ignored-module";
    fragment.allowed_targets = {};
    fragment.fragments = [];
    fragment.go ??= {units: []};
    fragment.go.profiles = ["production"];
    fragment.browser ??= {units: []};
    fragment.browser.entries = ["web/src/main.tsx"];
    fragment.browser.exclusions = [];
    fs.writeFileSync(fragmentPath, JSON.stringify(fragment, null, 2));

    const loaded = loadManifest(fixtureCopy.fixtureRoot);
    for (const field of [
      "module",
      "allowed_targets",
      "fragments",
      "go.profiles",
      "browser.entries",
      "browser.exclusions",
    ]) {
      assert.ok(loaded.load_diagnostics.some((item) => item.includes(`central field ${field}`)));
    }
  } finally {
    fs.rmSync(fixtureCopy.fixtureRoot, { recursive: true, force: true });
  }
});

test("validates exact capability source ownership", () => {
  const fixtureCopy = copyManifestFixture();
  try {
    const fragmentPath = path.join(fixtureCopy.fixtureRoot, "architecture/modules/server-composition.json");
    const fragment = JSON.parse(fs.readFileSync(fragmentPath, "utf8"));
    fragment.source_ownership.push({path: "cmd/internal/serverapp/server.go"});
    fragment.source_ownership.push({path: "cmd/internal/serverapp/missing.go"});
    fragment.source_ownership.push({path: "cmd/internal/serverapp/*.go"});
    fs.writeFileSync(fragmentPath, JSON.stringify(fragment, null, 2));
    const loaded = loadManifest(fixtureCopy.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("source cmd/internal/serverapp/server.go is owned by both")));
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("source ownership must name an exact existing source file")));
  } finally {
    fs.rmSync(fixtureCopy.fixtureRoot, { recursive: true, force: true });
  }
});

test("requires every partitioned composition and binding source to be owned", () => {
  const fixtureCopy = copyManifestFixture();
  try {
    const fragmentPath = path.join(fixtureCopy.fixtureRoot, "architecture/modules/remember-application.json");
    const fragment = JSON.parse(fs.readFileSync(fragmentPath, "utf8"));
    fragment.source_ownership = fragment.source_ownership.filter((entry) => !entry.path.endsWith("remember_bindings.go"));
    fs.writeFileSync(fragmentPath, JSON.stringify(fragment, null, 2));
    const loaded = loadManifest(fixtureCopy.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.startsWith("missing-source-ownership:") && item.includes("remember_bindings.go")));
  } finally {
    fs.rmSync(fixtureCopy.fixtureRoot, { recursive: true, force: true });
  }
});

test("rejects a fragment whose capability does not match its filename", () => {
  const fixtureCopy = copyManifestFixture();
  try {
    const fragmentPath = path.join(fixtureCopy.fixtureRoot, "architecture/modules/foo.json");
    const fragment = JSON.parse(fs.readFileSync(
      path.join(fixtureCopy.fixtureRoot, "architecture/modules/classification-policy.json"),
      "utf8",
    ));
    fragment.capability = "bar";
    fs.writeFileSync(fragmentPath, JSON.stringify(fragment, null, 2));
    fixtureCopy.rootManifest.fragments.push("architecture/modules/foo.json");
    fs.writeFileSync(
      path.join(fixtureCopy.fixtureRoot, "architecture/ownership.v2.json"),
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
    const serverPath = path.join(fixtureCopy.fixtureRoot, "architecture/modules/server-composition.json");
    const serverFragment = JSON.parse(fs.readFileSync(serverPath, "utf8"));
    const postgresFragment = JSON.parse(fs.readFileSync(
      path.join(fixtureCopy.fixtureRoot, "architecture/modules/postgres-storage-adapter.json"),
      "utf8",
    ));
    const controlFragment = JSON.parse(fs.readFileSync(
      path.join(fixtureCopy.fixtureRoot, "architecture/modules/control-portal.json"),
      "utf8",
    ));
    serverFragment.go.units.push(postgresFragment.go.units[0]);
    serverFragment.browser = {units: [controlFragment.browser.units[0]]};
    fs.writeFileSync(serverPath, JSON.stringify(serverFragment, null, 2));
    const loaded = loadManifest(fixtureCopy.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.startsWith("duplicate-fragment:") && item.includes(postgresFragment.go.units[0].id)));
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
  const unit = productionManifest.go.units.find((entry) => entry.id.endsWith("/internal/knowledge/postgres"));
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

test("retains permanent ownership records and worker anchors", () => {
  assert.equal(productionManifest.source_ownership.length, 126);
  assert.ok(productionManifest.source_ownership.every((entry) => Object.keys(entry).length === 3));
  assert.ok(productionManifest.workers.every((entry) => !Object.hasOwn(entry, "lifecycle_issue")));
  assert.ok(productionManifest.workers.every((entry) => entry.role === "worker"));
});

test("allows composition-to-adapter edges", () => {
  assert.deepEqual(checkGoEdges(fixtureManifest(), [{
    source: "fixture/composition",
    target: "fixture/postgres",
  }]).diagnostics, []);
});

test("rejects a transport-to-PostgreSQL falsification edge", () => {
  const result = checkGoEdges(fixtureManifest(), [{
    source: "fixture/transport",
    target: "fixture/postgres",
  }]);
  assert.equal(result.diagnostics.length, 1);
  assert.match(result.diagnostics[0], /^forbidden:/);
});

test("rejects an unclassified package", () => {
  const result = checkGoEdges(fixtureManifest(), [{
    source: "fixture/missing",
    target: "fixture/postgres",
  }]);
  assert.equal(result.diagnostics.length, 1);
  assert.match(result.diagnostics[0], /^unclassified:/);
});

test("rejects retired migration fields and duplicated role policy", () => {
  const manifest = structuredClone(productionManifest);
  manifest.completed_issues = [263];
  manifest.exceptions = [];
  manifest.workers[0].lifecycle_issue = 381;
  const diagnostics = validateManifest(manifest);
  assert.ok(diagnostics.some((item) => item.includes("completed_issues is retired")));
  assert.ok(diagnostics.some((item) => item.includes("exceptions is retired")));
  assert.ok(diagnostics.some((item) => item.includes("uses retired lifecycle_issue")));

  const duplicatePolicy = structuredClone(productionManifest);
  duplicatePolicy.allowed_targets = {};
  assert.ok(validateManifest(duplicatePolicy).some((item) => item.includes("allowed_targets") && item.includes("architecture role matrix")));

  const rootOwned = copyManifestFixture();
  try {
    rootOwned.rootManifest.source_ownership = [];
    fs.writeFileSync(
      path.join(rootOwned.fixtureRoot, "architecture/ownership.v2.json"),
      JSON.stringify(rootOwned.rootManifest, null, 2),
    );
    const loaded = loadManifest(rootOwned.fixtureRoot);
    assert.ok(loaded.load_diagnostics.some((item) => item.includes("root manifest must not define source_ownership")));
  } finally {
    fs.rmSync(rootOwned.fixtureRoot, {recursive: true, force: true});
  }
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
