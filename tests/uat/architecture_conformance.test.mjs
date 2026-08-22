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
func work() {}
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
    ]);
  } finally {
    fs.rmSync(fixtureRoot, { recursive: true, force: true });
  }
});
