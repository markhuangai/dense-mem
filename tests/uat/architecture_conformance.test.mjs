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
  isModuleImport,
  parseArgs,
  resolveBrowserImport,
  scanGoTokens,
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
    completed_issues: [260],
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

function assertEdgeExpectation(edge, result) {
  if (edge.expected === "allowed") {
    assert.deepEqual(result.diagnostics, []);
    return;
  }
  assert.equal(result.diagnostics.length, 1);
  assert.match(result.diagnostics[0], new RegExp(`^${edge.expected}:`));
}

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
      removal_issue: 262,
      reason: "fixture exception",
    }];
    return manifest;
  };
  const ordered = validateManifest(makeManifest([260, 262]));
  const reversed = validateManifest(makeManifest([262, 260]));
  assert.deepEqual(reversed, ordered);
  assert.ok(ordered.some((item) => item.startsWith("expired:")));
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
  expiring.completed_issues = [260, 261, 262, 263, 277];
  assert.ok(validateManifest(expiring).some((item) => item.includes("worker") && item.includes("completed issue 277")));

  const permanent = structuredClone(productionManifest);
  permanent.completed_issues = [260, 261, 262, 263, 277];
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
