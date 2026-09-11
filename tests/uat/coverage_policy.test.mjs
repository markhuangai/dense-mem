import assert from "node:assert/strict";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { execFileSync, spawnSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = new URL("../../", import.meta.url);
const repositoryRoot = fileURLToPath(root);
const packageScript = join(repositoryRoot, "scripts", "go-packages.sh");
const coverageScript = join(repositoryRoot, "scripts", "coverage-report.sh");

async function read(relativePath) {
  return readFile(new URL(relativePath, root), "utf8");
}

test("coverage reports use complete Go discovery and deduplicated profiles", async () => {
  const script = await read("scripts/coverage-report.sh");
  const ci = await read("scripts/ci-check.sh");
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
});

test("browser and proxy coverage keep only bootstrap wrappers outside the inventory", async () => {
  const webPackage = JSON.parse(await read("web/package.json"));
  const webConfig = await read("web/vite.config.ts");
  const proxyPackage = JSON.parse(await read("packages/mcp-proxy/package.json"));
  const proxyIntegration = await read("packages/mcp-proxy/tests/proxy.integration.test.js");
  const workflow = await read(".github/workflows/ci-shared.yml");

  assert.equal(webPackage.scripts["test:coverage"], "vitest run --coverage");
  assert.match(webConfig, /provider: "v8"/);
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
  writeFixture(fixture, "cmd/e2e/go.mod", "module example.com/discovery/cmd/e2e\n\ngo 1.26\n");
  writeFixture(fixture, "cmd/e2e/main.go", "package main\nfunc main() {}\n");
  writeFixture(fixture, "internal/with-tests/with.go", "package withtests\nfunc Value() int { return 1 }\n");
  writeFixture(fixture, "internal/with-tests/with_external_test.go", "package withtests_test\n");
  writeFixture(fixture, "internal/no-tests/no.go", "package notests\nfunc Value() int { return 1 }\n");
  writeFixture(fixture, "tests/uat/fixture.go", "package fixture\n");
  writeFixture(fixture, "ignored/ignored.go", "package ignored\n");

  execFileSync("git", ["init", "-q"], { cwd: fixture });
  execFileSync("git", ["config", "user.email", "coverage@example.test"], { cwd: fixture });
  execFileSync("git", ["config", "user.name", "Coverage Fixture"], { cwd: fixture });
  execFileSync("git", ["add", "."], { cwd: fixture });
  execFileSync("git", ["commit", "-qm", "fixture"], { cwd: fixture });
  writeFixture(fixture, "internal/working-tree/working.go", "package workingtree\nfunc Value() int { return 1 }\n");

  const complete = run("bash", [packageScript, "--coverage", "--root", fixture]);
  assert.equal(complete.status, 0, complete.stderr);
  assert.match(complete.stdout, /example\.com\/discovery\/internal\/with-tests/);
  assert.match(complete.stdout, /example\.com\/discovery\/internal\/no-tests/);
  assert.match(complete.stdout, /example\.com\/discovery\/internal\/working-tree/);
  assert.match(complete.stdout, /example\.com\/discovery\/cmd\/eval-runner/);
  assert.doesNotMatch(complete.stdout, /example\.com\/discovery\/cmd\/e2e/);
  assert.doesNotMatch(complete.stdout, /example\.com\/discovery\/tests\/uat/);
  assert.doesNotMatch(complete.stdout, /example\.com\/discovery\/ignored/);

  const production = run("bash", [packageScript, "--production", "--root", fixture]);
  assert.equal(production.status, 0, production.stderr);
  assert.doesNotMatch(production.stdout, /example\.com\/discovery\/cmd\/eval-runner/);
  assert.match(production.stdout, /example\.com\/discovery\/internal\/no-tests/);
});

test("complete Go coverage deduplicates profiles and transitional thresholds are exact", (t) => {
  const fixture = mkdtempSync(join(tmpdir(), "dense-mem-coverage-runner-"));
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const fakeGo = join(fixture, "go");
  writeFileSync(fakeGo, fakeGoScript());
  chmodSync(fakeGo, 0o755);

  const completeDir = join(fixture, "complete");
  const complete = runCoverage(fakeGo, completeDir, "--complete");
  assert.equal(complete.status, 0, complete.stderr);
  const merged = readFixture(join(completeDir, "go-complete.out"));
  assert.equal((merged.match(/^example\//gmu) || []).length, 5);
  assert.match(merged, /example\/evaluation\.go/);
  assert.match(readFixture(join(completeDir, "go-complete.txt")), /complete total: 3\/5 60\.0%/);

  const missing = runCoverage(fakeGo, join(fixture, "missing"), "--complete", { FAKE_SKIP_EVALUATION: "1" });
  assert.notEqual(missing.status, 0);

  const exact = runCoverage(fakeGo, join(fixture, "exact"), "--transitional", { FAKE_COVERAGE_TOTAL: "90.0" });
  assert.equal(exact.status, 0, exact.stderr);
  const below = runCoverage(fakeGo, join(fixture, "below"), "--transitional", { FAKE_COVERAGE_TOTAL: "89.9" });
  assert.equal(below.status, 1);
});

function run(command, args, options = {}) {
  return spawnSync(command, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: { ...process.env, ...(options.env || {}) },
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
