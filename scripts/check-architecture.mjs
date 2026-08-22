#!/usr/bin/env node

import fs from "node:fs";
import { createRequire } from "node:module";
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

const supportedGoProfiles = Object.freeze({
  production: Object.freeze({ name: "production", tags: "", production: true }),
  evaluation: Object.freeze({ name: "evaluation", tags: "evaluation", production: false }),
});
const supportedGoProfileNames = Object.freeze(Object.keys(supportedGoProfiles));

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
const browserAssetExtensions = new Set([
  ".avif",
  ".bmp",
  ".gif",
  ".ico",
  ".jpeg",
  ".jpg",
  ".json",
  ".png",
  ".svg",
  ".ttf",
  ".wasm",
  ".webp",
  ".woff",
  ".woff2",
]);
const browserJavaScriptExtensions = new Set([".cjs", ".js", ".jsx", ".mjs"]);
const require = createRequire(import.meta.url);

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

export function validateManifest(manifest, actualModulePath = null) {
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
  } else if (actualModulePath !== null && manifest.module !== actualModulePath) {
    diagnostics.push(diagnostic("invalid-manifest", `module must match go.mod module declaration ${actualModulePath}`));
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
  const profiles = manifest?.go?.profiles;
  if (!Array.isArray(profiles) || JSON.stringify(profiles) !== JSON.stringify(supportedGoProfileNames)) {
    diagnostics.push(diagnostic("invalid-manifest", `go.profiles must exactly match [${supportedGoProfileNames.join(", ")}]`));
  }
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
      if (!Number.isInteger(exception.removal_issue) || exception.removal_issue <= manifest.enforced_through_issue) {
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
      const key = `${worker?.path}\u0000${worker?.function}\u0000${worker?.kind}\u0000${worker?.ordinal}`;
      if (!worker || typeof worker.path !== "string" || typeof worker.function !== "string" || worker.function.length === 0 || typeof worker.kind !== "string" || !Number.isInteger(worker.ordinal) || worker.ordinal < 1) {
        diagnostics.push(diagnostic("invalid-manifest", "workers contains an invalid anchor"));
        continue;
      }
      if (hasWildcard(worker.path) || hasWildcard(worker.function) || hasWildcard(worker.kind)) {
        diagnostics.push(diagnostic("wildcard", `worker ${worker.path} must use exact identity fields`));
      }
      if (!new Set(["goroutine", "run", "start"]).has(worker.kind)) {
        diagnostics.push(diagnostic("unknown-kind", `worker ${worker.path} uses ${worker.kind}`));
      }
      if (workerKeys.has(key)) {
        diagnostics.push(diagnostic("duplicate", `worker ${worker.path} identity is repeated`));
      }
      workerKeys.add(key);
      if (worker.role !== "worker") {
        diagnostics.push(diagnostic("unknown-role", `worker ${worker.path} must use role worker`));
      }
      if (!Number.isInteger(worker.owner_issue) || worker.owner_issue <= manifest.enforced_through_issue) {
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

export function isModuleImport(modulePath, packagePath) {
  return packagePath === modulePath || packagePath.startsWith(`${modulePath}/`);
}

export function discoverGo(root, modulePath, profileNames = supportedGoProfileNames) {
  const profiles = profileNames.map((name) => {
    const profile = supportedGoProfiles[name];
    if (!profile) throw new Error(`unsupported Go discovery profile ${name}`);
    return profile;
  });
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
      const imports = line.slice(separator + 1).split(",").filter((item) => isModuleImport(modulePath, item));
      for (const target of imports) edges.push({ source, target, profile: profile.name });
    }
  }
  return { packages: sorted(packages), edges };
}

function browserExcluded(root, manifest, filePath) {
  const relative = normaliseRelative(root, filePath);
  return (manifest.browser?.exclusions ?? []).includes(relative);
}

export function resolveBrowserImport(root, filePath, specifier) {
  const request = specifier.split(/[?#]/u, 1)[0];
  const rootAbsolute = request.startsWith("/");
  if (!specifier.startsWith(".") && !rootAbsolute) return { external: true };
  const base = rootAbsolute
    ? path.resolve(root, "web", request.slice(1))
    : path.resolve(path.dirname(filePath), request);
  const extension = path.extname(base).toLowerCase();
  if (browserStyleExtensions.has(extension) || browserAssetExtensions.has(extension)) return { asset: true };
  const resolutionBase = browserJavaScriptExtensions.has(extension)
    ? base.slice(0, -extension.length)
    : base;
  const candidates = [];
  if (browserCodeExtensions.has(extension)) candidates.push(base);
  else candidates.push(...[...browserCodeExtensions].map((item) => `${resolutionBase}${item}`));
  candidates.push(...[...browserCodeExtensions].map((item) => path.join(resolutionBase, `index${item}`)));
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

function isViteGlobCall(ts, expression) {
  if (!ts.isPropertyAccessExpression(expression) || expression.name?.text !== "glob") return false;
  const receiver = expression.expression;
  return ts.isMetaProperty(receiver)
    && receiver.keywordToken === ts.SyntaxKind.ImportKeyword
    && receiver.name?.text === "meta";
}

function viteGlobPatterns(ts, node) {
  const single = stringLiteralText(ts, node);
  if (single !== null) return [single];
  if (!ts.isArrayLiteralExpression(node)) return null;
  const patterns = [];
  for (const element of node.elements) {
    const pattern = stringLiteralText(ts, element);
    if (pattern === null) return null;
    patterns.push(pattern);
  }
  return patterns.length > 0 ? patterns : null;
}

function absoluteViteGlobPattern(root, filePath, pattern) {
  const negative = pattern.startsWith("!");
  const value = negative ? pattern.slice(1) : pattern;
  if (!value.startsWith(".") && !value.startsWith("/")) {
    throw new Error(`Vite glob ${pattern} must be relative or root-absolute`);
  }
  const absolute = value.startsWith("/")
    ? path.resolve(root, value.slice(1))
    : path.resolve(path.dirname(filePath), value);
  const relative = path.relative(root, absolute);
  if (relative.startsWith("..") || path.isAbsolute(relative)) {
    throw new Error(`Vite glob ${pattern} escapes the repository root`);
  }
  return negative ? `!${absolute}` : absolute;
}

function expandViteGlob(root, filePath, patterns, globSync) {
  const absolutePatterns = patterns.map((pattern) => absoluteViteGlobPattern(root, filePath, pattern));
  const options = { absolute: true, dot: true, nodir: true, follow: false };
  const positivePatterns = absolutePatterns.filter((pattern) => !pattern.startsWith("!"));
  const negativePatterns = absolutePatterns
    .filter((pattern) => pattern.startsWith("!"))
    .map((pattern) => pattern.slice(1));
  const excluded = new Set(globSync(negativePatterns, options).map((match) => path.resolve(match)));
  return sorted(globSync(positivePatterns, options)
    .map((match) => path.resolve(match))
    .filter((match) => !excluded.has(match))
    .filter((match) => {
      const relative = path.relative(root, match);
      return !relative.startsWith("..") && !path.isAbsolute(relative);
    }));
}

function browserImportSpecifiers(ts, sourceFile, root, filePath, globSync) {
  const specifiers = [];
  const diagnostics = [];
  const relativeSpecifier = (target) => {
    const relative = path.relative(path.dirname(filePath), target).split(path.sep).join("/");
    return relative.startsWith(".") ? relative : `./${relative}`;
  };
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
      if (isViteGlobCall(ts, node.expression)) {
        const patterns = viteGlobPatterns(ts, node.arguments[0]);
        const location = normaliseRelative(root, filePath);
        if (patterns === null) {
          diagnostics.push(diagnostic("unsupported-glob", `Vite glob in ${location} must use literal string patterns`));
        } else {
          try {
            for (const match of expandViteGlob(root, filePath, patterns, globSync)) {
              specifiers.push(relativeSpecifier(match));
            }
          } catch (error) {
            diagnostics.push(diagnostic("invalid-glob", `Vite glob in ${location} is invalid: ${error.message}`));
          }
        }
      }
    }
    node.forEachChild(visit);
  }
  visit(sourceFile);
  return { specifiers, diagnostics };
}

export async function discoverBrowser(root, manifest) {
  let api;
  try {
    const typescriptPaths = { paths: [path.join(root, ".lint")] };
    const packageJSON = readJson(require.resolve("typescript/package.json", typescriptPaths));
    if (packageJSON.version !== "7.0.2") {
      throw new Error(`architecture checker requires TypeScript 7.0.2, found ${packageJSON.version}`);
    }
    const apiModule = await import(pathToFileURL(require.resolve("typescript/unstable/sync", typescriptPaths)).href);
    const astModule = await import(pathToFileURL(require.resolve("typescript/unstable/ast", typescriptPaths)).href);
    api = new apiModule.API({ cwd: root });
    const configPath = path.join(root, "web", "tsconfig.json");
    const entries = manifest.browser.entries.map((entry) => path.resolve(root, entry));
    const snapshot = api.updateSnapshot({
      openFiles: entries.filter((entry) => fs.existsSync(entry)),
      openProjects: [configPath],
    });
    const project = snapshot.getProject(configPath);
    if (!project) throw new Error(`TypeScript could not load ${normaliseRelative(root, configPath)}`);
    const program = project.program;
    const ts = astModule;
    const { globSync } = await import(pathToFileURL(require.resolve("glob", typescriptPaths)).href);
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
      const imports = browserImportSpecifiers(ts, sourceFile, root, filePath, globSync);
      diagnostics.push(...imports.diagnostics);
      for (const specifier of imports.specifiers) {
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

function isGoIdentifier(value) {
  return /^[A-Za-z_][A-Za-z0-9_]*$/u.test(value);
}

export function scanGoTokens(source) {
  const tokens = [];
  let index = 0;
  let line = 1;
  const push = (text, tokenLine = line) => tokens.push({ text, line: tokenLine });
  while (index < source.length) {
    const character = source[index];
    if (/\s/u.test(character)) {
      if (character === "\n") line += 1;
      index += 1;
      continue;
    }
    if (character === "/" && source[index + 1] === "/") {
      index += 2;
      while (index < source.length && source[index] !== "\n") index += 1;
      continue;
    }
    if (character === "/" && source[index + 1] === "*") {
      index += 2;
      while (index < source.length && !(source[index] === "*" && source[index + 1] === "/")) {
        if (source[index] === "\n") line += 1;
        index += 1;
      }
      index = Math.min(source.length, index + 2);
      continue;
    }
    if (character === '"' || character === "'" || character === "`") {
      const quote = character;
      index += 1;
      while (index < source.length) {
        if (source[index] === "\n") line += 1;
        if (quote !== "`" && source[index] === "\\") {
          index += 2;
          continue;
        }
        if (source[index] === quote) {
          index += 1;
          break;
        }
        index += 1;
      }
      continue;
    }
    if (/[A-Za-z_]/u.test(character)) {
      const tokenLine = line;
      let end = index + 1;
      while (end < source.length && /[A-Za-z0-9_]/u.test(source[end])) end += 1;
      push(source.slice(index, end), tokenLine);
      index = end;
      continue;
    }
    push(character);
    index += 1;
  }
  return tokens;
}

function matchingToken(tokens, start, opening, closing) {
  let depth = 0;
  for (let index = start; index < tokens.length; index += 1) {
    if (tokens[index].text === opening) depth += 1;
    if (tokens[index].text === closing) {
      depth -= 1;
      if (depth === 0) return index;
    }
  }
  return -1;
}

function functionBodyIndex(tokens, parameterOpen) {
  const parameterClose = matchingToken(tokens, parameterOpen, "(", ")");
  if (parameterClose < 0) return -1;
  let nesting = 0;
  for (let index = parameterClose + 1; index < tokens.length; index += 1) {
    const text = tokens[index].text;
    if (text === "(" || text === "[") {
      nesting += 1;
      continue;
    }
    if (text === ")" || text === "]") {
      if (nesting === 0) return -1;
      nesting -= 1;
      continue;
    }
    if (text === "{" && nesting === 0) {
      const previous = tokens[index - 1]?.text;
      if (previous === "struct" || previous === "interface") {
        const typeEnd = matchingToken(tokens, index, "{", "}");
        if (typeEnd < 0) return -1;
        index = typeEnd;
        continue;
      }
      return index;
    }
  }
  return -1;
}

function functionSignature(tokens, functionIndex) {
  let cursor = functionIndex + 1;
  let functionName = null;
  if (isGoIdentifier(tokens[cursor]?.text)) {
    functionName = tokens[cursor].text;
    cursor += 1;
    if (tokens[cursor]?.text === "[") {
      const typeParametersEnd = matchingToken(tokens, cursor, "[", "]");
      if (typeParametersEnd < 0) return { functionName, parameterOpen: -1 };
      cursor = typeParametersEnd + 1;
    }
    while (cursor < tokens.length && tokens[cursor].text !== "(") cursor += 1;
    return { functionName, parameterOpen: cursor < tokens.length ? cursor : -1 };
  }
  if (tokens[cursor]?.text !== "(") return { functionName, parameterOpen: -1 };
  const firstClose = matchingToken(tokens, cursor, "(", ")");
  if (firstClose < 0) return { functionName, parameterOpen: -1 };
  const candidateName = tokens[firstClose + 1]?.text;
  if (isGoIdentifier(candidateName)) {
    let parameterOpen = firstClose + 2;
    if (tokens[parameterOpen]?.text === "[") {
      const typeParametersEnd = matchingToken(tokens, parameterOpen, "[", "]");
      if (typeParametersEnd < 0) return { functionName, parameterOpen: -1 };
      parameterOpen = typeParametersEnd + 1;
    }
    if (tokens[parameterOpen]?.text === "(") {
      return { functionName: candidateName, parameterOpen };
    }
  }
  return { functionName, parameterOpen: cursor };
}

function goFunctionBodies(tokens) {
  const bodies = new Map();
  for (let index = 0; index < tokens.length; index += 1) {
    if (tokens[index].text !== "func") continue;
    const signature = functionSignature(tokens, index);
    const body = signature.parameterOpen >= 0 ? functionBodyIndex(tokens, signature.parameterOpen) : -1;
    if (body >= 0 && signature.functionName !== null) bodies.set(body, signature.functionName);
  }
  return bodies;
}

function workerIdentity(worker) {
  return `${worker.path}\u0000${worker.function}\u0000${worker.kind}\u0000${worker.ordinal}`;
}

function addWorker(workers, ordinals, pathName, token, functionName, kind) {
  const ordinalKey = `${pathName}\u0000${functionName}\u0000${kind}`;
  const ordinal = (ordinals.get(ordinalKey) ?? 0) + 1;
  ordinals.set(ordinalKey, ordinal);
  workers.push({ path: pathName, line: token.line, function: functionName, kind, ordinal });
}

function isGoroutineInvocation(tokens, index) {
  const next = index + 1;
  if (tokens[next]?.text === "func" || isGoIdentifier(tokens[next]?.text)) return true;
  if (tokens[next]?.text !== "(") return false;
  const calleeEnd = matchingToken(tokens, next, "(", ")");
  if (calleeEnd <= next) return false;
  let callOpen = calleeEnd + 1;
  while (tokens[callOpen]?.text === "." && isGoIdentifier(tokens[callOpen + 1]?.text)) callOpen += 2;
  return tokens[callOpen]?.text === "(";
}

export function discoverWorkers(root) {
  const workers = [];
  const sourceFiles = walk(path.join(root, "cmd")).concat(walk(path.join(root, "internal")))
    .filter((filePath) => filePath.endsWith(".go") && !filePath.endsWith("_test.go"))
    .map((filePath) => ({ filePath, relative: normaliseRelative(root, filePath) }))
    .filter(({ relative }) => relative.startsWith("cmd/") || relative.startsWith("internal/"))
    .sort((left, right) => left.relative.localeCompare(right.relative));
  for (const { filePath, relative } of sourceFiles) {
    const tokens = scanGoTokens(fs.readFileSync(filePath, "utf8"));
    const functionBodies = goFunctionBodies(tokens);
    const braces = [];
    const ordinals = new Map();
    for (let index = 0; index < tokens.length; index += 1) {
      const token = tokens[index];
      if (token.text === "{") {
        braces.push(functionBodies.get(index) ?? braces.at(-1) ?? "<package>");
        continue;
      }
      if (token.text === "}") {
        braces.pop();
        continue;
      }
      const functionName = braces.at(-1) ?? "<package>";
      if (token.text === "go" && isGoroutineInvocation(tokens, index)) {
        addWorker(workers, ordinals, relative, token, functionName, "goroutine");
      }
      if (token.text === "." && ["Start", "Run"].includes(tokens[index + 1]?.text) && tokens[index + 2]?.text === "(") {
        addWorker(workers, ordinals, relative, token, functionName, tokens[index + 1].text.toLowerCase());
      }
    }
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
  const entryKeys = new Set(entries.map(workerIdentity));
  const used = new Set();
  for (const worker of discovered) {
    const key = workerIdentity(worker);
    if (!entryKeys.has(key)) {
      diagnostics.push(diagnostic("unclassified-worker", `${worker.path}:${worker.line} ${worker.kind} in ${worker.function}`));
    } else {
      used.add(key);
    }
  }
  entries.forEach((worker) => {
    const key = workerIdentity(worker);
    if (!used.has(key)) diagnostics.push(diagnostic("stale-worker", `${worker.path} ${worker.kind} in ${worker.function} is not present`));
  });
  return diagnostics;
}

export async function runCheck(root, manifest) {
  const modulePath = readModulePath(root);
  const diagnostics = [...validateManifest(manifest, modulePath)];
  if (diagnostics.length > 0) return { diagnostics: sorted(diagnostics), counts: null };
  const go = discoverGo(root, modulePath, manifest.go.profiles);
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
  return {
    diagnostics: sorted(new Set(diagnostics)),
    counts: { goPackages: go.packages.length, browserModules: browser.files.length },
  };
}

export function parseArgs(argv) {
  const options = { root: repositoryRoot, manifest: null };
  const requireValue = (flag, value) => {
    if (typeof value !== "string" || value.length === 0 || value.startsWith("--")) throw new Error(`${flag} requires a value`);
    return value;
  };
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === "--root") options.root = requireValue(arg, argv[++index]);
    else if (arg === "--manifest") options.manifest = requireValue(arg, argv[++index]);
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
  const result = await runCheck(root, manifest);
  const { diagnostics } = result;
  if (diagnostics.length > 0) {
    for (const item of diagnostics) console.error(`architecture: ${item}`);
    return 1;
  }
  console.log(`architecture conformance passed: ${result.counts.goPackages} Go packages, ${result.counts.browserModules} browser modules`);
  return 0;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().then((code) => process.exitCode = code).catch((error) => {
    console.error(`architecture: ${error.message}`);
    process.exitCode = 1;
  });
}
