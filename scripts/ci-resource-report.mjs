#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { spawn, spawnSync, execFileSync } from "node:child_process";
import { pathToFileURL } from "node:url";

export function readResourceSample() {
  const cpu = readFileSync("/proc/stat", "utf8").split("\n", 1)[0].trim().split(/\s+/).slice(1).map(Number);
  const memory = Object.fromEntries([...readFileSync("/proc/meminfo", "utf8").matchAll(/^(MemTotal|MemAvailable):\s+(\d+) kB$/gm)].map(([, key, value]) => [key, Number(value) * 1024]));
  if (cpu.length < 8 || cpu.some((value) => !Number.isFinite(value)) || !Number.isFinite(memory.MemAvailable) || !Number.isFinite(memory.MemTotal)) throw new Error("runner resource counters unavailable");
  return { total: cpu.slice(0, 8).reduce((sum, value) => sum + value, 0), idle: cpu[3] + cpu[4], used_bytes: memory.MemTotal - memory.MemAvailable, total_bytes: memory.MemTotal };
}

export async function runResourceReport(label, command, sourceDirectory = process.cwd()) {
  if (!/^[a-zA-Z0-9_/-]{1,80}$/.test(label) || command.length === 0) throw new Error("resource reporter requires a label and command");
  const source = execFileSync("git", ["rev-parse", "HEAD"], { cwd: sourceDirectory, encoding: "utf8" }).trim();
  const dirty = spawnSync("git", ["diff", "--quiet", "HEAD"], { cwd: sourceDirectory });
  if (![0, 1].includes(dirty.status)) throw new Error("resource reporter source status unavailable");
  const report = { type: "ci_resource_summary", source_sha: source, source_dirty: dirty.status === 1, run_id: process.env.GITHUB_RUN_ID || process.env.DENSE_MEM_CI_RUN_ID || "", run_attempt: process.env.GITHUB_RUN_ATTEMPT || process.env.DENSE_MEM_CI_RUN_ATTEMPT || "", label, scope: "runner /proc; sampled system usage includes service processes", interval_ms: 1000, samples: 0, cpu_intervals: 0, sampled_cpu_percent_max: 0, sampled_used_bytes_max: 0, collection_ms: 0, unavailable_samples: 0 };
  let previous;
  const sample = () => {
    const started = performance.now();
    try {
      const current = readResourceSample();
      if (previous && current.total > previous.total) {
        report.sampled_cpu_percent_max = Math.max(report.sampled_cpu_percent_max, 100 * (1 - (current.idle - previous.idle) / (current.total - previous.total)));
        report.cpu_intervals += 1;
      }
      report.sampled_used_bytes_max = Math.max(report.sampled_used_bytes_max, current.used_bytes);
      report.memory_total_bytes = current.total_bytes;
      report.samples += 1;
      previous = current;
    } catch (error) {
      report.unavailable_samples += 1;
      report.measurement_error_code = error.code || "invalid_counters";
    } finally {
      report.collection_ms += performance.now() - started;
    }
  };
  sample();
  const started = performance.now();
  const child = spawn(command[0], command.slice(1), { stdio: "inherit" });
  const forward = (signal) => child.kill(signal);
  const onInterrupt = () => forward("SIGINT");
  const onTerminate = () => forward("SIGTERM");
  process.on("SIGINT", onInterrupt);
  process.on("SIGTERM", onTerminate);
  const timer = setInterval(sample, report.interval_ms);
  try {
    const result = await new Promise((resolve) => {
      child.once("error", (error) => { report.child_start_error_code = error.code; resolve({ code: 127, signal: null }); });
      child.once("exit", (code, signal) => resolve({ code, signal }));
    });
    sample();
    Object.assign(report, { elapsed_ms: performance.now() - started, exit_code: result.code, signal: result.signal, measurements_complete: report.cpu_intervals > 0 && report.unavailable_samples === 0 });
    process.stdout.write(JSON.stringify(report) + "\n");
    return result.code ?? (result.signal === "SIGINT" ? 130 : 143);
  } finally {
    clearInterval(timer);
    process.off("SIGINT", onInterrupt);
    process.off("SIGTERM", onTerminate);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [label, ...args] = process.argv.slice(2);
  const sourceDirectory = args[0] === "--source-dir" ? args.splice(0, 2)[1] : process.cwd();
  if (args.shift() !== "--") throw new Error("usage: ci-resource-report.mjs LABEL [--source-dir DIR] -- COMMAND [ARGUMENTS]");
  process.exitCode = await runResourceReport(label, args, sourceDirectory);
}
