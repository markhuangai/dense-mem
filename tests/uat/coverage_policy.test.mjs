import assert from "node:assert/strict";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { execFileSync, spawnSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { devNull, tmpdir } from "node:os";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = new URL("../../", import.meta.url);
const repositoryRoot = fileURLToPath(root);
const packageScript = join(repositoryRoot, "scripts", "go-packages.sh");
const coverageScript = join(repositoryRoot, "scripts", "coverage-report.sh");
const coverageGate = join(repositoryRoot, "scripts", "coverage-gate.mjs");
const isolatedGitEnvironment = Object.fromEntries(
  Object.entries(process.env).filter(([name]) => !name.startsWith("GIT_")),
);
isolatedGitEnvironment.GIT_CONFIG_GLOBAL = devNull;
isolatedGitEnvironment.GIT_CONFIG_NOSYSTEM = "1";

async function read(relativePath) {
  return readFile(new URL(relativePath, root), "utf8");
}

test("coverage reports use complete Go discovery and deduplicated profiles", async () => {
  const script = await read("scripts/coverage-report.sh");
  const ci = await read("scripts/ci-check.sh");
  const gate = await read("scripts/coverage-gate.mjs");
  const textlintIgnore = await read(".textlintignore");
  const workflow = await read(".github/workflows/ci-shared.yml");

  assert.match(script, /--transitional/);
  assert.match(script, /--complete/);
  assert.match(script, /-coverpkg=/);
  assert.match(script, /cmd\/e2e/);
  assert.match(script, /merge_profiles/);
  assert.doesNotMatch(ci, /conflict\/postgres/);
  assert.doesNotMatch(ci, /storage\/(neo4j|postgres|redis)/);
  assert.match(textlintIgnore, /coverage\/\*\*/);
  assert.match(textlintIgnore, /coverage\.out/);
  assert.match(script, /grep -v '\/cmd\/server\$'/);
  assert.match(script, /--tags evaluation/);
  assert.match(workflow, /scripts\/coverage-report\.sh --complete/);
  assert.match(script, /total \* 10/);
  assert.match(ci, /coverage-gate\.mjs/);
  assert.match(gate, /covered \* 10/);
});

test("browser and proxy coverage keep only bootstrap wrappers outside the inventory", async () => {
  const webPackage = JSON.parse(await read("web/package.json"));
  const webConfig = await read("web/vite.config.ts");
  const proxyPackage = JSON.parse(await read("packages/mcp-proxy/package.json"));
  const proxyIntegration = await read("packages/mcp-proxy/tests/proxy.integration.test.js");
  const workflow = await read(".github/workflows/ci-shared.yml");

  assert.equal(webPackage.scripts["test:coverage"], "vitest run --coverage");
  assert.match(webConfig, /provider: "v8"/);
  assert.match(webConfig, /all: true/);
  assert.match(webConfig, /src\/App\.test-helpers\.ts/);
  assert.match(webConfig, /src\/main\.tsx/);
  assert.match(webConfig, /src\/user\/main\.tsx/);
  assert.equal(proxyPackage.devDependencies.c8, "10.1.3");
  assert.match(proxyPackage.scripts["test:coverage"], /c8/);
  assert.match(proxyIntegration, /NODE_V8_COVERAGE/);
  assert.match(workflow, /npm run test:coverage --prefix web/);
  assert.match(workflow, /npm run test:coverage --prefix packages\/mcp-proxy/);
});

test("Go coverage discovery keeps external tests, testless packages, and working-tree sources", (t) => {
  const fixture = mkdtempSync(join(tmpdir(), "dense-mem-coverage-discovery-"));
  t.after(() => rmSync(fixture, { recursive: true, force: true }));

  writeFixture(fixture, "go.mod", "module example.com/discovery\n\ngo 1.26\n");
  writeFixture(fixture, ".gitignore", "ignored/**\n");
  writeFixture(fixture, "cmd/server/main.go", "package main\nfunc main() {}\n");
  writeFixture(fixture, "cmd/eval-runner/main.go", "package main\nfunc main() {}\n");
  writeFixture(fixture, "cmd/internal/demo/postgres/cleanup.go", "package postgres\nfunc Cleanup() {}\n");
  writeFixture(fixture, "cmd/e2e/go.mod", "module example.com/discovery/cmd/e2e\n\ngo 1.26\n");
  writeFixture(fixture, "cmd/e2e/main.go", "package main\nfunc main() {}\n");
  writeFixture(fixture, "internal/storage/postgres/graphread/traversal.go", "package graphread\nfunc Traverse() {}\n");
  writeFixture(fixture, "internal/storage/postgres/lockadmission/admission.go", "package lockadmission\nfunc Admit() {}\n");
  writeFixture(fixture, "internal/example/postgres/adapter.go", "package postgres\nfunc Adapt() {}\n");
  writeFixture(fixture, "internal/with-tests/with.go", "package withtests\nfunc Value() int { return 1 }\n");
  writeFixture(fixture, "internal/with-tests/with_external_test.go", "package withtests_test\n");
  writeFixture(fixture, "internal/no-tests/no.go", "package notests\nfunc Value() int { return 1 }\n");
  writeFixture(fixture, "internal/integration-only/only_integration_test.go", "//go:build integration\n\npackage integrationonly\nimport \"testing\"\nfunc TestOnlyIntegration(t *testing.T) {}\n");
  writeFixture(fixture, "tests/uat/fixture.go", "package fixture\n");
  writeFixture(fixture, "ignored/ignored.go", "package ignored\n");

  const gitOptions = { cwd: fixture, env: isolatedGitEnvironment };
  execFileSync("git", ["init", "-q"], gitOptions);
  execFileSync("git", ["config", "user.email", "coverage@example.test"], gitOptions);
  execFileSync("git", ["config", "user.name", "Coverage Fixture"], gitOptions);
  execFileSync("git", ["add", "."], gitOptions);
  execFileSync("git", ["commit", "-qm", "fixture"], gitOptions);
  writeFixture(fixture, "internal/working-tree/working.go", "package workingtree\nfunc Value() int { return 1 }\n");

  const complete = run("bash", [packageScript, "--coverage", "--root", fixture]);
  assert.equal(complete.status, 0, complete.stderr);
  assert.match(complete.stdout, /example\.com\/discovery\/internal\/with-tests/);
  assert.match(complete.stdout, /example\.com\/discovery\/internal\/no-tests/);
  assert.match(complete.stdout, /example\.com\/discovery\/internal\/working-tree/);
  assert.match(complete.stdout, /example\.com\/discovery\/cmd\/eval-runner/);
  assert.doesNotMatch(complete.stdout, /example\.com\/discovery\/cmd\/internal\/demo\/postgres/);
  assert.doesNotMatch(complete.stdout, /example\.com\/discovery\/internal\/storage\/postgres\/(graphread|lockadmission)/);
  assert.doesNotMatch(complete.stdout, /example\.com\/discovery\/internal\/example\/postgres/);
  assert.doesNotMatch(complete.stdout, /example\.com\/discovery\/cmd\/e2e/);
  assert.doesNotMatch(complete.stdout, /example\.com\/discovery\/internal\/integration-only/);
  assert.doesNotMatch(complete.stdout, /example\.com\/discovery\/tests\/uat/);
  assert.doesNotMatch(complete.stdout, /example\.com\/discovery\/ignored/);

  const production = run("bash", [packageScript, "--production", "--root", fixture]);
  assert.equal(production.status, 0, production.stderr);
  assert.doesNotMatch(production.stdout, /example\.com\/discovery\/cmd\/eval-runner/);
  assert.match(production.stdout, /example\.com\/discovery\/internal\/no-tests/);
  assert.doesNotMatch(production.stdout, /example\.com\/discovery\/internal\/integration-only/);

  const integration = run("bash", [packageScript, "--tags", "integration", "--root", fixture]);
  assert.equal(integration.status, 0, integration.stderr);
  assert.match(integration.stdout, /example\.com\/discovery\/internal\/integration-only/);

  const notIntegration = run("bash", [packageScript, "--tags", "notintegration", "--root", fixture]);
  assert.equal(notIntegration.status, 0, notIntegration.stderr);
  assert.doesNotMatch(notIntegration.stdout, /example\.com\/discovery\/internal\/integration-only/);

  writeFixture(
    fixture,
    "internal/integration-only/broken.go",
    "//go:build integration\n\npackage integrationonly\n\nimport (\n",
  );
  const brokenIntegration = run("bash", [packageScript, "--tags", "integration", "--root", fixture]);
  assert.notEqual(brokenIntegration.status, 0);
});

test("complete Go coverage deduplicates profiles and rejects exact thresholds", (t) => {
  const fixture = mkdtempSync(join(tmpdir(), "dense-mem-coverage-runner-"));
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const fakeGo = join(fixture, "go");
  writeFileSync(fakeGo, fakeGoScript());
  chmodSync(fakeGo, 0o755);

  const completeDir = join(fixture, "complete");
  const complete = runCoverage(fakeGo, completeDir, "--complete");
  assert.equal(complete.status, 1, complete.stderr);
  const merged = readFixture(join(completeDir, "go-complete.out"));
  assert.equal((merged.match(/^example\//gmu) || []).length, 5);
  assert.match(merged, /example\/evaluation\.go/);
  assert.match(readFixture(join(completeDir, "go-complete.txt")), /complete total: 3\/5 60\.0%/);

  const missing = runCoverage(fakeGo, join(fixture, "missing"), "--complete", { FAKE_SKIP_EVALUATION: "1" });
  assert.notEqual(missing.status, 0);

  const exact = runCoverage(fakeGo, join(fixture, "exact"), "--transitional", { FAKE_COVERAGE_TOTAL: "90.0" });
  assert.equal(exact.status, 1, exact.stderr);
  const below = runCoverage(fakeGo, join(fixture, "below"), "--transitional", { FAKE_COVERAGE_TOTAL: "89.9" });
  assert.equal(below.status, 1);
});

test("browser and proxy coverage gates fail closed for missing, empty, and exact reports", (t) => {
  const fixture = mkdtempSync(join(tmpdir(), "dense-mem-browser-coverage-gate-"));
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const writeReport = (name, value) => {
    const path = join(fixture, name);
    if (value !== undefined) writeFileSync(path, JSON.stringify(value));
    return path;
  };
  const exact = writeReport("exact.json", { total: { statements: { total: 10, covered: 9 } } });
  const empty = writeReport("empty.json", { total: { statements: { total: 0, covered: 0 } } });
  const passing = writeReport("passing.json", { total: { statements: { total: 11, covered: 10 } } });
  for (const label of ["browser", "MCP proxy"]) {
    assert.equal(run("node", [coverageGate, label, exact]).status, 1);
    assert.equal(run("node", [coverageGate, label, empty]).status, 1);
    assert.equal(run("node", [coverageGate, label, join(fixture, "missing.json")]).status, 1);
    assert.equal(run("node", [coverageGate, label, passing]).status, 0);
  }
});

function run(command, args, options = {}) {
  return spawnSync(command, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: { ...isolatedGitEnvironment, ...(options.env || {}) },
  });
}

function runCoverage(fakeGo, outputDir, mode, extraEnv = {}) {
  return run("bash", [coverageScript, mode], {
    env: {
      PATH: `${join(fakeGo, "..")}:${process.env.PATH}`,
      COVERAGE_OUTPUT_DIR: outputDir,
      ...extraEnv,
    },
  });
}

function writeFixture(rootPath, relativePath, contents) {
  const filePath = join(rootPath, relativePath);
  mkdirSync(join(filePath, ".."), { recursive: true });
  writeFileSync(filePath, contents);
}

function readFixture(filePath) {
  return readFileSync(filePath, "utf8");
}

function fakeGoScript() {
  return [
    "#!/usr/bin/env bash",
    "set -euo pipefail",
    "",
    "if [[ \"${1:-}\" == \"list\" ]]; then",
    "  printf '%s\\n' github.com/markhuangai/dense-mem/internal/conflict/postgres",
    "  exit 0",
    "fi",
    "",
    "if [[ \"${1:-}\" == \"tool\" && \"${2:-}\" == \"cover\" ]]; then",
    "  percent=\"${FAKE_COVERAGE_TOTAL:-60.0}\"",
    "  printf 'fake coverage\\ntotal: (statements) %s%%\\n' \"${percent}\"",
    "  exit 0",
    "fi",
    "",
    "profile=\"\"",
    "for argument in \"$@\"; do",
    "  case \"${argument}\" in",
    "    -coverprofile=*) profile=\"${argument#-coverprofile=}\" ;;",
    "  esac",
    "done",
    "if [[ -z \"${profile}\" ]]; then exit 0; fi",
    "if [[ \"${FAKE_SKIP_EVALUATION:-}\" == \"1\" && \"${profile}\" == *go-evaluation-complete.raw ]]; then exit 0; fi",
    "mkdir -p \"$(dirname \"${profile}\")\"",
    "{",
    "  printf 'mode: atomic\\n'",
    "  case \"${profile}\" in",
    "    *go-root-complete.raw|*coverage.out)",
    "      printf 'example/root.go:1.1,1.2 1 1\\n'",
    "      printf 'example/root.go:1.1,1.2 1 0\\n'",
    "      printf 'example/root.go:2.1,2.2 1 0\\n'",
    "      ;;",
    "    *go-evaluation-complete.raw)",
    "      printf 'example/root.go:1.1,1.2 1 0\\n'",
    "      printf 'example/evaluation.go:1.1,1.2 1 1\\n'",
    "      printf 'example/evaluation.go:2.1,2.2 1 0\\n'",
    "      ;;",
    "    *go-e2e-complete.raw)",
    "      printf 'example/e2e.go:1.1,1.2 1 1\\n'",
    "      ;;",
    "  esac",
    "} > \"${profile}\"",
  ].join("\n") + "\n";
}
