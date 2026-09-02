#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";

const REDACTION = "[REDACTED]";

function valuesFromEnvFile(path, field = "", minimumLength = 4) {
  const values = [];
  let selected;
  const add = (value) => {
    if (typeof value !== "string") return;
    if (/[\r\n]/.test(value)) throw new Error("multiline Compose env values are unsupported for diagnostics redaction");
    if (field) {
      selected = value;
      return;
    }
    if (value.length >= minimumLength) values.push(value);
  };
  if (!path) return values;
  let source;
  try {
    source = readFileSync(path, "utf8");
  } catch {
    throw new Error("unable to read the diagnostics redaction env file");
  }
  const unsupported = (lineNumber, reason) => {
    throw new Error(`unsupported Compose env syntax at line ${lineNumber}: ${reason}`);
  };
  const parseValue = (raw, lineNumber) => {
    const valueText = raw.trim();
    if (valueText === "") return "";
    if (valueText.startsWith("'")) {
      const closing = valueText.indexOf("'", 1);
      if (closing === -1) unsupported(lineNumber, "unterminated single-quoted value");
      const suffix = valueText.slice(closing + 1).trim();
      if (suffix && !suffix.startsWith("#")) unsupported(lineNumber, "unexpected text after quoted value");
      return valueText.slice(1, closing);
    }
    if (valueText.startsWith('"')) {
      let value = "";
      let closing = -1;
      for (let index = 1; index < valueText.length; index += 1) {
        const character = valueText[index];
        if (character === '"') {
          closing = index;
          break;
        }
        if (character === "\\") {
          const escaped = valueText[++index];
          const escapes = { n: "\n", r: "\r", t: "\t", "\\": "\\", '"': '"' };
          if (escaped === undefined || !Object.hasOwn(escapes, escaped)) {
            unsupported(lineNumber, "unsupported double-quote escape");
          }
          value += escapes[escaped];
        } else {
          value += character;
        }
      }
      if (closing === -1) unsupported(lineNumber, "unterminated double-quoted value");
      const suffix = valueText.slice(closing + 1).trim();
      if (suffix && !suffix.startsWith("#")) unsupported(lineNumber, "unexpected text after quoted value");
      if (value.includes("$")) unsupported(lineNumber, "variable interpolation is unsupported");
      return value;
    }
    const comment = valueText.search(/[\t ]#/);
    const value = (comment === -1 ? valueText : valueText.slice(0, comment)).trimEnd();
    if (value.includes("$")) unsupported(lineNumber, "variable interpolation is unsupported");
    return value;
  };
  for (const [index, sourceLine] of source.split(/\r?\n/).entries()) {
    const lineNumber = index + 1;
    const line = lineNumber === 1 ? sourceLine.replace(/^\uFEFF/, "") : sourceLine;
    if (/^\s*$/.test(line) || /^\s*#/.test(line)) continue;
    const match = line.match(/^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:=|:)\s*(.*)$/);
    if (!match) unsupported(lineNumber, "expected KEY=VALUE or KEY: VALUE assignment");
    const value = parseValue(match[2], lineNumber);
    if (!field || match[1] === field) add(value);
  }
  return field ? selected : values;
}

function valueFromEnvFile(path, field) {
  return valuesFromEnvFile(path, field);
}

function createRedactor(values, write, minimumLength = 4) {
  const patterns = [...new Set(values)]
    .filter((value) => typeof value === "string" && value.length >= minimumLength && !/[\r\n]/.test(value))
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
  const minimumLength = process.env.DENSE_MEM_CI_REDACT_ALLOW_SHORT === "1" ? 1 : 4;
  const values = [
    ...valuesFromEnvFile(process.env.DENSE_MEM_CI_REDACT_ENV_FILE || "", "", minimumLength),
    ...(process.env.DENSE_MEM_CI_REDACT_EXTRA_VALUES || "").split("\n"),
  ].filter((value) => typeof value === "string" && value.length >= minimumLength && !/[\r\n]/.test(value));
  const redactor = createRedactor(values, (chunk) => process.stdout.write(chunk), minimumLength);
  process.stdin.setEncoding("utf8");
  process.stdin.on("data", (chunk) => redactor.write(chunk));
  process.stdin.on("end", () => redactor.end());
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) run();

export { createRedactor, redactChunks, valueFromEnvFile, valuesFromEnvFile };
