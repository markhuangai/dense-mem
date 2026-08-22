#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";
const roles = new Set([
  "domain",
  "port",
  "application_api",
  "adapter",
  "transport",
  "worker",
  "composition",
  "offline_evaluation",
]);

export const allowedTargets = Object.freeze({
  domain: ["domain"],
  port: ["domain", "port"],
  application_api: ["application_api", "domain", "port"],
  adapter: ["domain", "port"],
  transport: ["application_api", "domain", "transport"],
  worker: ["application_api", "domain", "port"],
  composition: [
    "domain",
    "port",
    "application_api",
    "adapter",
    "transport",
    "worker",
    "composition",
    "offline_evaluation",
  ],
  offline_evaluation: [
    "domain",
    "port",
    "application_api",
    "adapter",
    "transport",
    "worker",
    "composition",
    "offline_evaluation",
  ],
});

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const browserCodeExtensions = new Set([".ts", ".tsx", ".mts", ".cts"]);
const browserStyleExtensions = new Set([".css", ".scss", ".sass", ".less"]);
const excludedBrowserDefaults = [
  "web/src/test/setup.ts",
  "web/src/user/App.test-helpers.ts",
];

function diagnostic(code, message) {
  return `${code}: ${message}`;
}

function sorted(values) {
  return [...values].sort((left, right) => left.localeCompare(right));
}

function hasWildcard(value) {
  return /[*?]/u.test(value);
}

function normaliseRoot(value) {
  return path.resolve(value || process.cwd());
}

function normaliseRelative(root, value) {
  return path.relative(root, path.resolve(root, value)).split(path.sep).join("/");
}

function readJson(filePath) {
  return JSON.parse(fs.readFileSync(filePath, "utf8"));
}

function readModulePath(root) {
  const goMod = fs.readFileSync(path.join(root, "go.mod"), "utf8");
  const match = goMod.match(/^module\s+([^\s]+)\s*$/mu);
  if (!match) {
    throw new Error("go.mod has no module declaration");
  }
  return match[1];
}

function unitMap(manifest, kind) {
  const units = manifest?.[kind]?.units;
  return new Map((Array.isArray(units) ? units : []).map((unit) => [unit.id, unit]));
}

export function validateManifest(manifest) {
  const diagnostics = [];
  if (!manifest || typeof manifest !== "object") {
    return [diagnostic("invalid-manifest", "manifest must be an object")];
  }
  if (manifest.schema_version !== 1) {
    diagnostics.push(diagnostic("invalid-manifest", "schema_version must be 1"));
  }
  if (!Number.isInteger(manifest.enforced_through_issue)) {
    diagnostics.push(diagnostic("invalid-manifest", "enforced_through_issue must be an integer"));
  }
  if (!manifest.module || typeof manifest.module !== "string" || hasWildcard(manifest.module)) {
    diagnostics.push(diagnostic("invalid-manifest", "module must be a concrete module path"));
  }
  if (!manifest.allowed_targets || typeof manifest.allowed_targets !== "object") {
    diagnostics.push(diagnostic("invalid-manifest", "allowed_targets is required"));
  } else {
    for (const role of roles) {
      const expected = allowedTargets[role];
      const actual = manifest.allowed_targets[role];
      if (!Array.isArray(actual) || JSON.stringify(actual) !== JSON.stringify(expected)) {
        diagnostics.push(diagnostic("invalid-manifest", `allowed_targets.${role} must match the architecture role matrix`));
      }
    }
  }

  for (const kind of ["go", "browser"]) {
    const units = manifest?.[kind]?.units;
    if (!Array.isArray(units)) {
      diagnostics.push(diagnostic("invalid-manifest", `${kind}.units must be an array`));
      continue;
    }
    const seen = new Set();
    for (const unit of units) {
      if (!unit || typeof unit !== "object" || typeof unit.id !== "string") {
        diagnostics.push(diagnostic("invalid-manifest", `${kind}.units contains an invalid entry`));
        continue;
      }
      if (seen.has(unit.id)) {
        diagnostics.push(diagnostic("duplicate", `${kind} unit ${unit.id} is classified more than once`));
      }
      seen.add(unit.id);
      if (hasWildcard(unit.id)) {
        diagnostics.push(diagnostic("wildcard", `${kind} unit ${unit.id} must be exact`));
      }
      if (!roles.has(unit.role)) {
        diagnostics.push(diagnostic("unknown-role", `${kind} unit ${unit.id} uses ${unit.role ?? "no role"}`));
      }
      if (typeof unit.capability !== "string" || unit.capability.length === 0) {
        diagnostics.push(diagnostic("invalid-manifest", `${kind} unit ${unit.id} needs a capability`));
      }
    }
  }

  const goUnits = unitMap(manifest, "go");
  const exceptions = manifest.exceptions;
  const exceptionKeys = new Set();
  if (!Array.isArray(exceptions)) {
    diagnostics.push(diagnostic("invalid-manifest", "exceptions must be an array"));
  } else {
    for (const exception of exceptions) {
      if (!exception || typeof exception !== "object") {
        diagnostics.push(diagnostic("invalid-manifest", "exceptions contains an invalid entry"));
        continue;
      }
      const source = exception.source;
      const target = exception.target;
      const key = `${source}\u0000${target}`;
      if (typeof source !== "string" || typeof target !== "string" || hasWildcard(source) || hasWildcard(target)) {
        diagnostics.push(diagnostic("wildcard", `exception ${source ?? "?"} -> ${target ?? "?"} must use exact packages`));
      }
      if (exceptionKeys.has(key)) {
        diagnostics.push(diagnostic("duplicate", `exception ${source} -> ${target} is repeated`));
      }
      exceptionKeys.add(key);
      if (!goUnits.has(source) || !goUnits.has(target)) {
        diagnostics.push(diagnostic("unknown", `exception ${source} -> ${target} names an unclassified package`));
      }
      if (!Number.isInteger(exception.removal_issue) || exception.removal_issue <= manifest.enforced_through_issue || exception.removal_issue > 280) {
        diagnostics.push(diagnostic("expired", `exception ${source} -> ${target} needs a later removal issue`));
      }
      if (typeof exception.reason !== "string" || exception.reason.length === 0) {
        diagnostics.push(diagnostic("invalid-manifest", `exception ${source} -> ${target} needs a reason`));
      }
    }
  }

  const entries = manifest?.browser?.entries;
  if (!Array.isArray(entries) || entries.length === 0) {
    diagnostics.push(diagnostic("invalid-manifest", "browser.entries must contain at least one entry"));
  } else {
    for (const entry of entries) {
      if (typeof entry !== "string" || hasWildcard(entry)) {
        diagnostics.push(diagnostic("invalid-manifest", `browser entry ${entry ?? "?"} must be exact`));
      }
    }
  }
  const exclusions = manifest?.browser?.exclusions;
  if (!Array.isArray(exclusions)) {
    diagnostics.push(diagnostic("invalid-manifest", "browser.exclusions must be an array"));
  } else {
    for (const exclusion of exclusions) {
      if (typeof exclusion !== "string" || hasWildcard(exclusion)) {
        diagnostics.push(diagnostic("wildcard", `browser exclusion ${exclusion ?? "?"} must be exact`));
      }
    }
  }

  const workers = manifest.workers;
  const workerKeys = new Set();
  if (!Array.isArray(workers)) {
    diagnostics.push(diagnostic("invalid-manifest", "workers must be an array"));
  } else {
    for (const worker of workers) {
      const key = `${worker?.path}\u0000${worker?.anchor}`;
      if (!worker || typeof worker.path !== "string" || typeof worker.anchor !== "string" || worker.anchor.length === 0) {
        diagnostics.push(diagnostic("invalid-manifest", "workers contains an invalid anchor"));
        continue;
      }
      if (hasWildcard(worker.path) || hasWildcard(worker.anchor)) {
        diagnostics.push(diagnostic("wildcard", `worker ${worker.path} must use an exact anchor`));
      }
      workerKeys.add(key);
      if (worker.role !== "worker") {
        diagnostics.push(diagnostic("unknown-role", `worker ${worker.path} must use role worker`));
      }
      if (!Number.isInteger(worker.owner_issue) || worker.owner_issue <= manifest.enforced_through_issue || worker.owner_issue > 280) {
        diagnostics.push(diagnostic("expired", `worker ${worker.path} needs its later lifecycle issue`));
      }
    }
  }
  return diagnostics;
}

export function evaluateGoEdge(manifest, source, target) {
  const goUnits = unitMap(manifest, "go");
  const sourceUnit = goUnits.get(source);
  const targetUnit = goUnits.get(target);
  if (!sourceUnit) {
    return { ok: false, diagnostic: diagnostic("unclassified", `Go package ${source} is not in the manifest`) };
  }
  if (!targetUnit) {
    return { ok: false, diagnostic: diagnostic("unclassified", `Go import target ${target} is not in the manifest`) };
  }
  if (source === target || allowedTargets[sourceUnit.role]?.includes(targetUnit.role)) {
    return { ok: true };
  }
  const exception = (manifest.exceptions ?? []).find((candidate) => candidate.source === source && candidate.target === target);
  if (exception) {
    return { ok: true, exception };
  }
  return {
    ok: false,
    diagnostic: diagnostic("forbidden", `${source} (${sourceUnit.role}) imports ${target} (${targetUnit.role})`),
  };
}

export function checkGoEdges(manifest, edges) {
  const diagnostics = [];
  const usedExceptions = new Set();
  for (const edge of edges) {
    const result = evaluateGoEdge(manifest, edge.source, edge.target);
    if (!result.ok) {
      diagnostics.push(result.diagnostic);
    }
    if (result.exception) {
      usedExceptions.add(`${result.exception.source}\u0000${result.exception.target}`);
    }
  }
  return { diagnostics, usedExceptions };
}

function run(command, args, cwd) {
  try {
    return execFileSync(command, args, { cwd, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  } catch (error) {
    const stderr = error?.stderr?.toString().trim();
    throw new Error(`${command} ${args.join(" ")} failed${stderr ? `: ${stderr}` : ""}`);
  }
}

function packageList(root, tags, production) {
  const script = path.join(root, "scripts", "go-packages.sh");
  const args = [script];
  if (production) args.push("--production");
  if (tags) args.push("--tags", tags);
  args.push("--root", root);
  return sorted(run("bash", args, root).split(/\r?\n/u).map((line) => line.trim()).filter(Boolean));
}

export function discoverGo(root, modulePath) {
  const profiles = [
    { name: "production", tags: "", production: true },
    { name: "evaluation", tags: "evaluation", production: false },
  ];
  const packages = new Set();
  const edges = [];
  for (const profile of profiles) {
    const discovered = packageList(root, profile.tags, profile.production).filter((pkg) => !pkg.includes("/tests/"));
    for (const pkg of discovered) packages.add(pkg);
    if (discovered.length === 0) throw new Error(`${profile.name} Go discovery found no packages`);
    const template = "{{.ImportPath}}|{{join .Imports \",\"}}";
    const args = ["list"];
    if (profile.tags) args.push("-tags", profile.tags);
    args.push("-f", template, ...discovered);
    for (const line of run("go", args, root).split(/\r?\n/u).map((item) => item.trim()).filter(Boolean)) {
      const separator = line.indexOf("|");
      if (separator < 0) throw new Error(`unexpected go list output: ${line}`);
      const source = line.slice(0, separator);
      const imports = line.slice(separator + 1).split(",").filter((item) => item.startsWith(`${modulePath}/`));
      for (const target of imports) edges.push({ source, target, profile: profile.name });
    }
  }
  return { packages: sorted(packages), edges };
}

function browserExcluded(root, manifest, filePath) {
  const relative = normaliseRelative(root, filePath);
  return [...excludedBrowserDefaults, ...(manifest.browser?.exclusions ?? [])].includes(relative);
}

function resolveBrowserImport(root, filePath, specifier) {
  if (!specifier.startsWith(".")) return { external: true };
  const base = path.resolve(path.dirname(filePath), specifier);
  const extension = path.extname(base);
  if (browserStyleExtensions.has(extension)) return { style: true };
  const candidates = [];
  if (browserCodeExtensions.has(extension)) candidates.push(base);
  else candidates.push(...[...browserCodeExtensions].map((item) => `${base}${item}`));
  candidates.push(...[...browserCodeExtensions].map((item) => path.join(base, `index${item}`)));
  for (const candidate of candidates) {
    if (fs.existsSync(candidate) && fs.statSync(candidate).isFile()) return { file: candidate };
  }
  return { error: `unresolved browser import ${specifier} from ${normaliseRelative(root, filePath)}` };
}

function stringLiteralText(ts, node) {
  if (!node) return null;
  if (node.kind === ts.SyntaxKind.StringLiteral || node.kind === ts.SyntaxKind.NoSubstitutionTemplateLiteral) return node.text;
  return null;
}

function browserImportSpecifiers(ts, sourceFile) {
  const specifiers = [];
  function visit(node) {
    if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) {
      const text = stringLiteralText(ts, node.moduleSpecifier);
      if (text !== null) specifiers.push(text);
    }
    if (ts.isCallExpression(node)) {
      const argument = node.arguments.length === 1 ? stringLiteralText(ts, node.arguments[0]) : null;
      if (node.expression.kind === ts.SyntaxKind.ImportKeyword && argument !== null) {
        specifiers.push(argument);
      }
      if (ts.isIdentifier(node.expression) && node.expression.text === "require" && argument !== null) {
        specifiers.push(argument);
      }
    }
    node.forEachChild(visit);
  }
  visit(sourceFile);
  return specifiers;
}

export async function discoverBrowser(root, manifest) {
  let api;
  try {
    const apiModule = await import(pathToFileURL(path.join(root, ".lint", "node_modules", "typescript", "dist", "api", "sync", "api.js")).href);
    const astModule = await import(pathToFileURL(path.join(root, ".lint", "node_modules", "typescript", "dist", "ast", "index.js")).href);
    api = new apiModule.API({ cwd: root });
    const configPath = path.join(root, "web", "tsconfig.json");
    const snapshot = api.updateSnapshot({ openProjects: [configPath] });
    const project = snapshot.getProject(configPath);
    if (!project) throw new Error(`TypeScript could not load ${normaliseRelative(root, configPath)}`);
    const program = project.program;
    const ts = astModule;
    const entries = manifest.browser.entries.map((entry) => path.resolve(root, entry));
    const discovered = new Set();
    const edges = [];
    const diagnostics = [];
    const pending = [...entries];
    while (pending.length > 0) {
      const filePath = pending.pop();
      if (discovered.has(filePath) || browserExcluded(root, manifest, filePath)) continue;
      if (!fs.existsSync(filePath)) {
        diagnostics.push(diagnostic("missing-entry", `browser entry ${normaliseRelative(root, filePath)} does not exist`));
        continue;
      }
      const sourceFile = program.getSourceFile(filePath);
      if (!sourceFile) {
        diagnostics.push(diagnostic("unparsed", `TypeScript did not include ${normaliseRelative(root, filePath)}`));
        continue;
      }
      discovered.add(filePath);
      for (const specifier of browserImportSpecifiers(ts, sourceFile)) {
        const resolution = resolveBrowserImport(root, filePath, specifier);
        if (resolution.error) {
          diagnostics.push(diagnostic("unresolved", resolution.error));
        } else if (resolution.file && !browserExcluded(root, manifest, resolution.file)) {
          pending.push(resolution.file);
          edges.push({
            source: normaliseRelative(root, filePath),
            target: normaliseRelative(root, resolution.file),
          });
        }
      }
    }
    return {
      files: sorted([...discovered].map((filePath) => normaliseRelative(root, filePath))),
      edges,
      diagnostics,
    };
  } finally {
    api?.close();
  }
}

function walk(directory) {
  const files = [];
  if (!fs.existsSync(directory)) return files;
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const filePath = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      if (["node_modules", ".git", "dist", "user-dist"].includes(entry.name)) continue;
      files.push(...walk(filePath));
    } else {
      files.push(filePath);
    }
  }
  return files;
}

function isWorkerSource(relative) {
  return /^(cmd\/(?:demo-server|internal\/(?:demo|migrationapp|serverapp))|internal\/(?:observability|repository|service))\//u.test(relative);
}

export function discoverWorkers(root) {
  const workers = [];
  for (const filePath of walk(path.join(root, "cmd")).concat(walk(path.join(root, "internal")))) {
    if (!filePath.endsWith(".go") || filePath.endsWith("_test.go")) continue;
    const relative = normaliseRelative(root, filePath);
    if (!isWorkerSource(relative)) continue;
    const lines = fs.readFileSync(filePath, "utf8").split(/\r?\n/u);
    lines.forEach((line, index) => {
      if (/\bgo\s+(?:func\b|[A-Za-z_][\w.]*(?:\s*\([^;]*\))?)/u.test(line) || /\.(?:Start|Run)\s*\(/u.test(line)) {
        workers.push({ path: relative, line: index + 1, anchor: line.trim() });
      }
    });
  }
  return workers;
}

function checkDiscoveredUnits(manifest, discoveredGo, discoveredBrowser) {
  const diagnostics = [];
  const goUnits = unitMap(manifest, "go");
  const actualGo = new Set(discoveredGo.packages);
  for (const packagePath of sorted(actualGo)) {
    if (!goUnits.has(packagePath)) diagnostics.push(diagnostic("unclassified", `Go package ${packagePath} is not in the manifest`));
  }
  for (const packagePath of goUnits.keys()) {
    if (!actualGo.has(packagePath)) diagnostics.push(diagnostic("stale", `Go package ${packagePath} is not discovered by either profile`));
  }

  const browserUnits = unitMap(manifest, "browser");
  const actualBrowser = new Set(discoveredBrowser.files);
  for (const filePath of sorted(actualBrowser)) {
    if (!browserUnits.has(filePath)) diagnostics.push(diagnostic("unclassified", `browser module ${filePath} is not in the manifest`));
  }
  for (const filePath of browserUnits.keys()) {
    if (!actualBrowser.has(filePath)) diagnostics.push(diagnostic("stale", `browser module ${filePath} is not reachable from a production entry`));
  }
  return diagnostics;
}

function checkBrowserEdges(manifest, discovery) {
  const diagnostics = [...discovery.diagnostics];
  const units = unitMap(manifest, "browser");
  for (const edge of discovery.edges) {
    const source = units.get(edge.source);
    const target = units.get(edge.target);
    if (!source || !target) continue;
    if (!allowedTargets[source.role]?.includes(target.role)) {
      diagnostics.push(diagnostic("forbidden", `${edge.source} (${source.role}) imports ${edge.target} (${target.role})`));
    }
  }
  return diagnostics;
}

function checkWorkers(manifest, discovered) {
  const diagnostics = [];
  const entries = manifest.workers ?? [];
  const used = new Set();
  for (const worker of discovered) {
    const index = entries.findIndex((candidate, candidateIndex) => candidate.path === worker.path && candidate.anchor === worker.anchor && !used.has(candidateIndex));
    if (index < 0) {
      diagnostics.push(diagnostic("unclassified-worker", `${worker.path}:${worker.line} ${worker.anchor}`));
    } else {
      used.add(index);
    }
  }
  entries.forEach((worker, index) => {
    if (!used.has(index)) diagnostics.push(diagnostic("stale-worker", `${worker.path} anchor is not present`));
  });
  return diagnostics;
}

export async function runCheck(root, manifest) {
  const diagnostics = [...validateManifest(manifest)];
  if (diagnostics.length > 0) return sorted(diagnostics);
  const modulePath = manifest.module;
  const go = discoverGo(root, modulePath);
  const browser = await discoverBrowser(root, manifest);
  const workers = discoverWorkers(root);
  diagnostics.push(...checkDiscoveredUnits(manifest, go, browser));
  const edgeResult = checkGoEdges(manifest, go.edges);
  diagnostics.push(...edgeResult.diagnostics);
  for (const exception of manifest.exceptions) {
    const key = `${exception.source}\u0000${exception.target}`;
    if (!edgeResult.usedExceptions.has(key)) diagnostics.push(diagnostic("unused", `exception ${exception.source} -> ${exception.target} is not exercised`));
  }
  diagnostics.push(...checkBrowserEdges(manifest, browser));
  diagnostics.push(...checkWorkers(manifest, workers));
  return sorted(new Set(diagnostics));
}

function parseArgs(argv) {
  const options = { root: repositoryRoot, manifest: null };
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === "--root") options.root = argv[++index];
    else if (arg === "--manifest") options.manifest = argv[++index];
    else if (arg === "--help" || arg === "-h") options.help = true;
    else throw new Error(`unknown option ${arg}`);
  }
  return options;
}

export async function main(argv = process.argv.slice(2)) {
  const options = parseArgs(argv);
  if (options.help) {
    console.log("usage: node scripts/check-architecture.mjs [--root <repository>] [--manifest <path>]");
    return 0;
  }
  const root = normaliseRoot(options.root);
  const manifestPath = options.manifest ? path.resolve(options.manifest) : path.join(root, "architecture", "ownership.v1.json");
  const manifest = readJson(manifestPath);
  const diagnostics = await runCheck(root, manifest);
  if (diagnostics.length > 0) {
    for (const item of diagnostics) console.error(`architecture: ${item}`);
    return 1;
  }
  const go = discoverGo(root, manifest.module);
  const browser = await discoverBrowser(root, manifest);
  console.log(`architecture conformance passed: ${go.packages.length} Go packages, ${browser.files.length} browser modules`);
  return 0;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().then((code) => process.exitCode = code).catch((error) => {
    console.error(`architecture: ${error.message}`);
    process.exitCode = 1;
  });
}
