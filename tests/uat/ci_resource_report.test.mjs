import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { readResourceSample } from "../../scripts/ci-resource-report.mjs";

const reporter = fileURLToPath(new URL("../../scripts/ci-resource-report.mjs", import.meta.url));

test("resource reporter measures real runner counters and preserves command output", async () => {
  const actual = readResourceSample();
  assert(actual.total > actual.idle && actual.used_bytes > 0 && actual.used_bytes < actual.total_bytes);
  const { stdout } = await promisify(execFile)(process.execPath, [reporter, "native-test", "--", process.execPath, "-e", 'console.log("native-command-output");setTimeout(()=>{},1100)']);
  const lines = stdout.trim().split("\n");
  assert.equal(lines[0], "native-command-output");
  const summary = JSON.parse(lines.at(-1));
  assert.equal(summary.type, "ci_resource_summary");
  assert.equal(summary.exit_code, 0);
  assert.equal(summary.measurements_complete, true);
  assert(summary.samples >= 3 && summary.elapsed_ms >= 1000 && summary.collection_ms > 0);
  assert(summary.sampled_used_bytes_max > 0 && summary.sampled_cpu_percent_max >= 0);
  assert.match(summary.source_sha, /^[0-9a-f]{40}$/);
});

test("resource reporter preserves a failing child status", async () => {
  await assert.rejects(promisify(execFile)(process.execPath, [reporter, "failed-test", "--", process.execPath, "-e", "process.exit(7)"]), (error) => {
    assert.equal(error.code, 7);
    assert.equal(JSON.parse(error.stdout.trim()).exit_code, 7);
    return true;
  });
});

test("resource reporter forwards cancellation and closes its sampling timer", async () => {
  const child = spawn(process.execPath, [reporter, "cancelled-test", "--", process.execPath, "-e", 'console.log("ready");setInterval(()=>{},1000)'], { stdio: ["ignore", "pipe", "pipe"] });
  let output = "";
  let sent = false;
  child.stdout.on("data", (data) => {
    output += data;
    if (output.includes("ready") && !sent) { sent = true; child.kill("SIGTERM"); }
  });
  const result = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => { child.kill("SIGKILL"); reject(new Error("resource reporter failed to stop")); }, 5000);
    child.once("exit", (code) => { clearTimeout(timer); resolve(code); });
  });
  assert.equal(result, 143);
  assert.equal(JSON.parse(output.trim().split("\n").at(-1)).signal, "SIGTERM");
});
