"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");

const HARNESS_ROOTS = [".github", "scripts", "tests", "cmd/e2e", "web/tests-compose"];

function prepareForkWorkspace(harness, source) {
  harness = fs.realpathSync(harness);
  source = fs.realpathSync(source);
  if (harness === source || !fs.existsSync(path.join(harness, ".github/scripts/fork-e2e-policy.cjs"))) {
    throw new Error("a distinct trusted upstream harness is required");
  }
  const files = execFileSync("git", ["-C", harness, "ls-files", "-z"], { encoding: "utf8" }).split("\0").filter(Boolean);
  const sourceFiles = execFileSync("git", ["-C", source, "ls-files", "-z"], { encoding: "utf8" }).split("\0").filter(Boolean);
  for (const name of HARNESS_ROOTS) fs.rmSync(path.join(source, name), { recursive: true, force: true });
  for (const name of sourceFiles.filter((name) => name.endsWith("_test.go"))) {
    const target = path.join(source, name);
    assertSafeParents(source, target);
    fs.rmSync(target, { force: true });
  }
  for (const name of files) {
    if (!isHarnessFile(name)) continue;
    const target = path.join(source, name);
    assertSafeParents(source, target);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.rmSync(target, { force: true });
    const original = path.join(harness, name);
    if (!fs.lstatSync(original).isFile()) throw new Error("the trusted harness contains an unsupported file type");
    fs.copyFileSync(original, target);
    fs.chmodSync(target, fs.statSync(original).mode & 0o777);
  }
}

function isHarnessFile(name) {
  return HARNESS_ROOTS.some((root) => name === root || name.startsWith(`${root}/`)) ||
    name.endsWith("_test.go") || /^web\/playwright.*\.config\./.test(name);
}

function assertSafeParents(root, target) {
  if (!target.startsWith(`${root}${path.sep}`)) throw new Error("harness path escaped the generated workspace");
  for (let current = path.dirname(target); current !== root; current = path.dirname(current)) {
    if (fs.lstatSync(current, { throwIfNoEntry: false })?.isSymbolicLink()) throw new Error("a fork symlink would redirect the trusted harness");
  }
}

if (require.main === module) {
  if (process.argv.length !== 4) throw new Error("usage: fork-e2e-workspace.cjs TRUSTED_HARNESS FORK_SOURCE");
  prepareForkWorkspace(process.argv[2], process.argv[3]);
}

module.exports = { prepareForkWorkspace, isHarnessFile };
