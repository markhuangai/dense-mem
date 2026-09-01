#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";

const REDACTION = "[REDACTED]";

function valuesFromEnvFile(path) {
  const values = [];
  const add = (value) => {
    if (typeof value !== "string" || value.length < 4 || /[\r\n]/.test(value)) return;
    values.push(value);
  };
  try {
    for (const line of readFileSync(path, "utf8").split(/\r?\n/)) {
      const match = line.match(/^\s*(?:export\s+)?[A-Za-z_][A-Za-z0-9_]*\s*=\s*(.*)$/);
      if (!match) continue;
      let value = match[1].trim();
      if ((value.startsWith("\"") && value.endsWith("\"")) || (value.startsWith("'") && value.endsWith("'"))) {
        value = value.slice(1, -1);
      }
      add(value);
    }
  } catch {}
  return values;
}

function createRedactor(values, write) {
  const patterns = [...new Set(values)]
    .filter((value) => typeof value === "string" && value.length >= 4 && !/[\r\n]/.test(value))
    .sort((left, right) => right.length - left.length);
  const maxPatternLength = patterns.reduce((max, value) => Math.max(max, value.length), 0);
  let pending = "";

  const findMatch = () => {
    let matchIndex = -1;
    let matchLength = 0;
    for (const pattern of patterns) {
      const index = pending.indexOf(pattern);
      if (index === -1) continue;
      if (matchIndex === -1 || index < matchIndex || (index === matchIndex && pattern.length > matchLength)) {
        matchIndex = index;
        matchLength = pattern.length;
      }
    }
    return { index: matchIndex, length: matchLength };
  };

  const hasOverlappingPartial = (match) => {
    if (match.index === -1) return false;
    for (const pattern of patterns) {
      if (pattern.length <= match.length) continue;
      for (let index = 0; index < pending.length; index += 1) {
        const available = pending.slice(index);
        if (available.length >= pattern.length || !pattern.startsWith(available)) continue;
        if (index < match.index + match.length && index + pattern.length > match.index) return true;
      }
    }
    return false;
  };

  const processPending = (final = false) => {
    for (;;) {
      const match = findMatch();
      const deferred = match.index !== -1 && !final && hasOverlappingPartial(match);
      if (match.index !== -1 && !deferred) {
        write(`${pending.slice(0, match.index)}${REDACTION}`);
        pending = pending.slice(match.index + match.length);
        continue;
      }
      if (pending.length < maxPatternLength) return;
      write(pending[0]);
      pending = pending.slice(1);
    }
  };

  return {
    write(chunk) {
      if (patterns.length === 0) {
        write(chunk);
        return;
      }
      pending += chunk;
      processPending();
    },
    end() {
      if (patterns.length === 0) return;
      processPending(true);
      write(pending);
      pending = "";
    },
  };
}

function redactChunks(chunks, values) {
  let output = "";
  const redactor = createRedactor(values, (chunk) => {
    output += chunk;
  });
  for (const chunk of chunks) redactor.write(chunk);
  redactor.end();
  return output;
}

function run() {
  const values = [
    ...valuesFromEnvFile(process.env.DENSE_MEM_CI_REDACT_ENV_FILE || ""),
    ...(process.env.DENSE_MEM_CI_REDACT_EXTRA_VALUES || "").split("\n"),
  ].filter((value) => typeof value === "string" && value.length >= 4 && !/[\r\n]/.test(value));
  const redactor = createRedactor(values, (chunk) => process.stdout.write(chunk));
  process.stdin.setEncoding("utf8");
  process.stdin.on("data", (chunk) => redactor.write(chunk));
  process.stdin.on("end", () => redactor.end());
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) run();

export { createRedactor, redactChunks, valuesFromEnvFile };
