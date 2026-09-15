import fs from "node:fs";
import path from "node:path";

const supportedGoProfileNames = Object.freeze(["production", "evaluation"]);
const workerKinds = new Set(["goroutine", "run", "start"]);
const fragmentCentralFields = Object.freeze(["module", "allowed_targets", "fragments"]);
const fragmentCentralNestedFields = Object.freeze({
  go: ["profiles"],
  browser: ["entries", "exclusions"],
});

export const allowedTargets = Object.freeze({
  domain: ["domain"],
  port: ["domain", "port"],
  application_api: ["application_api", "domain", "port"],
  adapter: ["domain", "port"],
  postgres_adapter: ["domain", "port", "postgres_infrastructure"],
  postgres_infrastructure: ["domain", "port", "postgres_infrastructure"],
  transport: ["application_api", "domain", "transport"],
  worker: ["application_api", "domain", "port"],
  composition: [
    "domain",
    "port",
    "application_api",
    "adapter",
    "postgres_adapter",
    "postgres_infrastructure",
    "transport",
    "worker",
    "composition",
    "offline_evaluation",
  ],
  offline_evaluation: [
    "domain",
    "port",
    "application_api",
    "transport",
    "offline_evaluation",
  ],
});

const roles = new Set(Object.keys(allowedTargets));

function diagnostic(code, message) {
  return `${code}: ${message}`;
}

function sorted(values) {
  return [...values].sort((left, right) => left.localeCompare(right));
}

function hasWildcard(value) {
  return typeof value === "string" && /[*?]/u.test(value);
}

const requiredSourceOwnership = Object.freeze([
  ["cmd/internal/serverapp/application_composition.go", "server-composition"],
  ["cmd/internal/serverapp/server.go", "server-composition"],
  ["cmd/server/main.go", "server-composition"],
  ["cmd/demo-server/main.go", "demo-composition"],
  ["cmd/internal/serverapp/access_composition.go", "access-application"],
  ["cmd/internal/serverapp/access_adapters.go", "access-application"],
  ["cmd/internal/serverapp/private_memory.go", "privacy-application"],
  ["cmd/internal/serverapp/audit_composition.go", "audit-application"],
  ["cmd/internal/serverapp/configuration_composition.go", "settings-application"],
  ["cmd/internal/serverapp/security_composition.go", "settings-application"],
  ["cmd/internal/serverapp/operation_log_composition.go", "application-services"],
  ["cmd/internal/serverapp/usage_metrics_composition.go", "application-services"],
  ["cmd/internal/serverapp/telemetry_composition.go", "application-services"],
  ["cmd/internal/serverapp/semanticwrite_composition.go", "semantic-write-application"],
  ["cmd/internal/serverapp/semanticwrite_embedding_adapter.go", "semantic-write-application"],
  ["cmd/internal/serverapp/search_composition.go", "embedding-provider"],
  ["cmd/internal/serverapp/search_background.go", "embedding-provider"],
  ["cmd/internal/serverapp/conflict_queue_composition.go", "conflict-application"],
  ["cmd/internal/serverapp/evidence_conflict_composition.go", "evidence-conflict-application"],
  ["cmd/internal/serverapp/conflict_review_composition.go", "conflict-review-application"],
  ["cmd/internal/serverapp/security_rejection_audit.go", "audit-application"],
  ["cmd/internal/serverapp/remember_composition.go", "remember-application"],
  ["internal/tools/registry/remember_bindings.go", "remember-application"],
  ["cmd/internal/serverapp/dream_composition.go", "dream-application"],
  ["internal/tools/registry/dream_bindings.go", "dream-application"],
  ["cmd/internal/serverapp/trace_composition.go", "context-application"],
  ["internal/tools/registry/trace_bindings.go", "context-application"],
  ["cmd/internal/serverapp/graph_composition.go", "graph-application"],
  ["cmd/internal/serverapp/community_composition.go", "community-application"],
  ["cmd/internal/serverapp/recall_composition.go", "memory-application"],
  ["cmd/internal/serverapp/lifecycle_composition.go", "memory-application"],
  ["cmd/internal/serverapp/recall_feedback_composition.go", "memory-application"],
  ["internal/tools/registry/recall_bindings.go", "memory-application"],
  ["internal/tools/registry/lifecycle_bindings.go", "memory-application"],
  ["cmd/internal/serverapp/memorypack_composition.go", "skill-pack-application"],
  ["internal/tools/registry/memory_pack_bindings.go", "skill-pack-application"],
  ["internal/tools/registry/capability_bindings.go", "tool-registry-application-api"],
  ["internal/tools/registry/tool_bindings.go", "tool-registry-application-api"],
  ["internal/tools/registry/toolset.go", "tool-registry-application-api"],
  ["internal/tools/registry/contract.go", "tool-registry-application-api"],
  ["internal/tools/registry/evaluation_bindings.go", "offline-evaluation"],
  ["internal/http/control_portal_bindings.go", "http-transport"],
  ["internal/http/router_protected.go", "http-transport"],
  ["internal/http/user_portal.go", "http-transport"],
  ["internal/http/control_portal.go", "http-transport"],
  ["internal/http/server.go", "http-transport"],
]);

function isSafeSourcePath(root, value) {
  if (typeof value !== "string" || value.length === 0 || value.startsWith("/") || hasWildcard(value)) {
    return false;
  }
  const segments = value.split("/");
  if (segments.some((segment) => segment === "" || segment === "." || segment === "..")) {
    return false;
  }
  const absolute = path.resolve(root, value);
  const relative = path.relative(root, absolute);
  return relative === value && fs.existsSync(absolute) && fs.statSync(absolute).isFile();
}

function isVisibility(value) {
  return value === "public" || value === "private";
}

function unitMap(manifest, kind) {
  const units = manifest?.[kind]?.units;
  return new Map((Array.isArray(units) ? units : []).map((unit) => [unit.id, unit]));
}

function workerIdentity(worker) {
  return `${worker?.path}\u0000${worker?.function}\u0000${worker?.kind}\u0000${worker?.ordinal}`;
}

function isBrowserTestOnlyPath(value) {
  return value.split("/").includes("test")
    || value.split("/").includes("tests")
    || /\.(?:test|spec)(?:-helpers)?\.[cm]?[jt]sx?$/u.test(value);
}

function validateAllowedTargets(manifest, diagnostics) {
  if (!manifest.allowed_targets || typeof manifest.allowed_targets !== "object") {
    diagnostics.push(diagnostic("invalid-manifest", "allowed_targets is required"));
    return;
  }
  for (const role of roles) {
    const expected = allowedTargets[role];
    const actual = manifest.allowed_targets[role];
    if (!Array.isArray(actual) || JSON.stringify(actual) !== JSON.stringify(expected)) {
      diagnostics.push(diagnostic("invalid-manifest", `allowed_targets.${role} must match the architecture role matrix`));
    }
  }
}

export function validateManifest(manifest, actualModulePath = null) {
  const diagnostics = [];
  if (!manifest || typeof manifest !== "object") {
    return [diagnostic("invalid-manifest", "manifest must be an object")];
  }
  if (manifest.schema_version !== 2) {
    diagnostics.push(diagnostic("invalid-manifest", "schema_version must be 2"));
  }
  for (const field of ["completed_issues", "exceptions", "compatibility_bridges", "enforced_through_issue"]) {
    if (Object.prototype.hasOwnProperty.call(manifest, field)) {
      diagnostics.push(diagnostic("invalid-manifest", `${field} is retired from schema_version 2`));
    }
  }

  if (!manifest.module || typeof manifest.module !== "string" || hasWildcard(manifest.module)) {
    diagnostics.push(diagnostic("invalid-manifest", "module must be a concrete module path"));
  } else if (actualModulePath !== null && manifest.module !== actualModulePath) {
    diagnostics.push(diagnostic("invalid-manifest", `module must match go.mod module declaration ${actualModulePath}`));
  }
  validateAllowedTargets(manifest, diagnostics);

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
      if (typeof unit.capability !== "string" || unit.capability.length === 0 || hasWildcard(unit.capability)) {
        diagnostics.push(diagnostic("invalid-manifest", `${kind} unit ${unit.id} needs a concrete capability`));
      }
      if (!isVisibility(unit.visibility)) {
        diagnostics.push(diagnostic("invalid-manifest", `${kind} unit ${unit.id} needs visibility public or private`));
      } else if (kind === "go" && ["adapter", "postgres_adapter", "postgres_infrastructure"].includes(unit.role) && unit.visibility !== "private") {
        diagnostics.push(diagnostic("invalid-manifest", `${kind} unit ${unit.id} with role ${unit.role} must be private`));
      }
    }
  }

  const goUnits = unitMap(manifest, "go");
  const profiles = manifest?.go?.profiles;
  if (!Array.isArray(profiles) || JSON.stringify(profiles) !== JSON.stringify(supportedGoProfileNames)) {
    diagnostics.push(diagnostic("invalid-manifest", `go.profiles must exactly match [${supportedGoProfileNames.join(", ")}]`));
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
      } else if (!exclusion.startsWith("web/") || !isBrowserTestOnlyPath(exclusion)) {
        diagnostics.push(diagnostic("invalid-manifest", `browser exclusion ${exclusion} must target a test-only module`));
      }
    }
  }

  const workers = manifest.workers === undefined ? [] : manifest.workers;
  const workerKeys = new Set();
  if (!Array.isArray(workers)) {
    diagnostics.push(diagnostic("invalid-manifest", "workers must be an array"));
  } else {
    for (const worker of workers) {
      const key = workerIdentity(worker);
      if (!worker || typeof worker.path !== "string" || typeof worker.function !== "string" || worker.function.length === 0 || typeof worker.kind !== "string" || !Number.isInteger(worker.ordinal) || worker.ordinal < 1) {
        diagnostics.push(diagnostic("invalid-manifest", "workers contains an invalid anchor"));
        continue;
      }
      if (hasWildcard(worker.path) || hasWildcard(worker.function) || hasWildcard(worker.kind)) {
        diagnostics.push(diagnostic("wildcard", `worker ${worker.path} must use exact identity fields`));
      }
      if (!workerKinds.has(worker.kind)) {
        diagnostics.push(diagnostic("unknown-kind", `worker ${worker.path} uses ${worker.kind}`));
      }
      if (workerKeys.has(key)) {
        diagnostics.push(diagnostic("duplicate", `worker ${worker.path} identity is repeated`));
      }
      workerKeys.add(key);
      if (worker.role !== "worker") {
        diagnostics.push(diagnostic("unknown-role", `worker ${worker.path} must use role worker`));
      }
      for (const field of ["lifecycle_issue", "owner_issue"]) {
        if (Object.prototype.hasOwnProperty.call(worker, field)) {
          diagnostics.push(diagnostic("invalid-manifest", `worker ${worker.path} uses retired ${field}`));
        }
      }
    }
  }
  return diagnostics;
}

export function evaluateModuleEdge(sourceUnit, targetUnit) {
  if (!sourceUnit || !targetUnit) {
    return { ok: false, diagnostic: diagnostic("unclassified", "module edge names an unclassified unit") };
  }
  const roleAllowed = allowedTargets[sourceUnit.role]?.includes(targetUnit.role);
  if (!roleAllowed) {
    return {
      ok: false,
      diagnostic: diagnostic("forbidden", `${sourceUnit.id} (${sourceUnit.role}) imports ${targetUnit.id} (${targetUnit.role})`),
    };
  }
  const sameCapability = sourceUnit.capability === targetUnit.capability;
  const compositionMayConstructPrivate = sourceUnit.role === "composition";
  const postgresAdapterMayUseInfrastructure = sourceUnit.role === "postgres_adapter"
    && targetUnit.role === "postgres_infrastructure";
  if (sameCapability || targetUnit.visibility === "public" || compositionMayConstructPrivate
    || postgresAdapterMayUseInfrastructure) {
    return { ok: true };
  }
  return {
    ok: false,
    diagnostic: diagnostic("private", `${sourceUnit.id} (${sourceUnit.capability}) cannot cross into private ${targetUnit.id} (${targetUnit.capability})`),
  };
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
  return evaluateModuleEdge(sourceUnit, targetUnit);
}

export function checkGoEdges(manifest, edges) {
  const diagnostics = [];
  for (const edge of edges) {
    const result = evaluateGoEdge(manifest, edge.source, edge.target);
    if (!result.ok) diagnostics.push(result.diagnostic);
  }
  return { diagnostics };
}

function readJson(filePath) {
  return JSON.parse(fs.readFileSync(filePath, "utf8"));
}

function fragmentPathIsSafe(root, value) {
  if (typeof value !== "string" || hasWildcard(value) || !value.startsWith("architecture/modules/") || !value.endsWith(".json")) {
    return false;
  }
  const segments = value.split("/");
  if (segments.length !== 3 || segments[0] !== "architecture" || segments[1] !== "modules"
    || segments[2].length <= ".json".length || segments.some((segment) => segment === "." || segment === "..")) {
    return false;
  }
  const absolute = path.resolve(root, value);
  const modulesRoot = path.resolve(root, "architecture/modules");
  const relative = path.relative(modulesRoot, absolute);
  return relative.length > 0 && !relative.startsWith("..") && !path.isAbsolute(relative) && !relative.includes(path.sep);
}

function validateFragmentShape(fragment, relativePath, root, diagnostics) {
  if (!fragment || typeof fragment !== "object") {
    diagnostics.push(diagnostic("invalid-fragment", `${relativePath} must contain an object`));
    return;
  }
  for (const key of fragmentCentralFields) {
    if (Object.prototype.hasOwnProperty.call(fragment, key)) {
      diagnostics.push(diagnostic("invalid-fragment", `${relativePath} must not define central field ${key}; move it to the root manifest`));
    }
  }
  for (const [kind, keys] of Object.entries(fragmentCentralNestedFields)) {
    if (!fragment[kind] || typeof fragment[kind] !== "object") continue;
    for (const key of keys) {
      if (Object.prototype.hasOwnProperty.call(fragment[kind], key)) {
        diagnostics.push(diagnostic("invalid-fragment", `${relativePath} must not define central field ${kind}.${key}; move it to the root manifest`));
      }
    }
  }
  if (fragment.schema_version !== 2) {
    diagnostics.push(diagnostic("invalid-fragment", `${relativePath} schema_version must be 2`));
  }
  if (typeof fragment.capability !== "string" || fragment.capability.length === 0 || hasWildcard(fragment.capability)) {
    diagnostics.push(diagnostic("invalid-fragment", `${relativePath} needs a concrete capability`));
  }
  for (const key of ["completed_issues", "exceptions", "compatibility_bridges", "enforced_through_issue"]) {
    if (Object.prototype.hasOwnProperty.call(fragment, key)) {
      diagnostics.push(diagnostic("invalid-fragment", `${relativePath}.${key} is retired from schema_version 2`));
    }
  }
  for (const kind of ["go", "browser"]) {
    if (fragment[kind] === undefined) continue;
    if (!fragment[kind] || typeof fragment[kind] !== "object" || Array.isArray(fragment[kind])
      || !Array.isArray(fragment[kind].units)) {
      diagnostics.push(diagnostic("invalid-fragment", `${relativePath}.${kind}.units must be an array when ${kind} is present`));
    }
  }
  if (fragment.workers !== undefined && !Array.isArray(fragment.workers)) {
    diagnostics.push(diagnostic("invalid-fragment", `${relativePath}.workers must be an array when present`));
  }
  if (fragment.source_ownership !== undefined && !Array.isArray(fragment.source_ownership)) {
    diagnostics.push(diagnostic("invalid-fragment", `${relativePath}.source_ownership must be an array`));
  } else {
    for (const ownership of fragment.source_ownership ?? []) {
      if (!ownership || typeof ownership !== "object" || Array.isArray(ownership)
        || !isSafeSourcePath(root, ownership.path)) {
        diagnostics.push(diagnostic("invalid-fragment", `${relativePath} source ownership must name an exact existing source file`));
        continue;
      }
      for (const key of Object.keys(ownership)) {
        if (key === "path") continue;
        const detail = key === "issue" ? "uses retired issue metadata" : `contains unsupported field ${key}`;
        diagnostics.push(diagnostic("invalid-fragment", `${relativePath} source ownership ${ownership.path} ${detail}`));
      }
    }
  }
}

export function loadManifest(root, manifestPath = path.join(root, "architecture", "ownership.v2.json")) {
  const diagnostics = [];
  let rootManifest;
  try {
    rootManifest = readJson(manifestPath);
  } catch (error) {
    return {
      schema_version: null,
      module: null,
      go: { profiles: [], units: [] },
      browser: { entries: [], exclusions: [], units: [] },
      allowed_targets: allowedTargets,
      workers: [],
      source_ownership: [],
      load_diagnostics: [diagnostic("invalid-manifest", `cannot read root manifest: ${error.message}`)],
    };
  }
  if (!rootManifest || typeof rootManifest !== "object") {
    return {
      schema_version: null,
      module: null,
      go: { profiles: [], units: [] },
      browser: { entries: [], exclusions: [], units: [] },
      allowed_targets: allowedTargets,
      workers: [],
      source_ownership: [],
      load_diagnostics: [diagnostic("invalid-manifest", "root manifest must be an object")],
    };
  }

  const rootFragmentOwnedFields = [];
  for (const key of ["completed_issues", "exceptions", "compatibility_bridges", "workers", "source_ownership", "enforced_through_issue", "allowed_targets"]) {
    if (Object.prototype.hasOwnProperty.call(rootManifest, key)) {
      const owner = key === "allowed_targets" ? "the checker policy" : "schema_version 2";
      rootFragmentOwnedFields.push(diagnostic("invalid-manifest", `root manifest must not define ${key}; it is owned by ${owner}`));
    }
  }
  for (const kind of ["go", "browser"]) {
    if (rootManifest[kind] && typeof rootManifest[kind] === "object"
      && Object.prototype.hasOwnProperty.call(rootManifest[kind], "units")) {
      rootFragmentOwnedFields.push(diagnostic("invalid-fragment", `root manifest must not define ${kind}.units; move units to capability fragments`));
    }
  }
  diagnostics.push(...rootFragmentOwnedFields);
  const fragmentRefs = rootManifest.fragments;
  if (!Array.isArray(fragmentRefs) || fragmentRefs.length === 0) {
    return {
      ...rootManifest,
      load_diagnostics: [
        ...rootFragmentOwnedFields,
        diagnostic("invalid-fragment", "root manifest must list one or more architecture/modules fragments"),
      ],
    };
  }
  const normalizedRefs = [];
  const seenRefs = new Set();
  for (const ref of fragmentRefs) {
    if (!fragmentPathIsSafe(root, ref)) {
      diagnostics.push(diagnostic("invalid-fragment", `fragment ${ref ?? "?"} must be a direct architecture/modules JSON path`));
      continue;
    }
    if (seenRefs.has(ref)) {
      diagnostics.push(diagnostic("duplicate-fragment", `fragment ${ref} is listed more than once`));
      continue;
    }
    seenRefs.add(ref);
    normalizedRefs.push(ref);
  }
  normalizedRefs.sort((left, right) => left.localeCompare(right));
  const modulesRoot = path.join(root, "architecture", "modules");
  let actualRefs = [];
  try {
    actualRefs = fs.readdirSync(modulesRoot, { withFileTypes: true })
      .filter((entry) => entry.isFile() && entry.name.endsWith(".json"))
      .map((entry) => `architecture/modules/${entry.name}`)
      .sort((left, right) => left.localeCompare(right));
  } catch (error) {
    diagnostics.push(diagnostic("invalid-fragment", `cannot read architecture/modules: ${error.message}`));
  }
  for (const ref of actualRefs) {
    if (!seenRefs.has(ref)) diagnostics.push(diagnostic("unlisted-fragment", `fragment ${ref} is not listed by the root manifest`));
  }
  for (const ref of normalizedRefs) {
    if (!actualRefs.includes(ref)) diagnostics.push(diagnostic("missing-fragment", `fragment ${ref} does not exist`));
  }

  const goUnits = [];
  const browserUnits = [];
  const workers = [];
  const sourceOwnership = [];
  const fragmentRecords = [];
  const fragmentCapabilities = new Map();
  for (const ref of normalizedRefs) {
    if (!actualRefs.includes(ref)) continue;
    let fragment;
    try {
      fragment = readJson(path.join(root, ref));
    } catch (error) {
      diagnostics.push(diagnostic("invalid-fragment", `cannot read ${ref}: ${error.message}`));
      continue;
    }
    validateFragmentShape(fragment, ref, root, diagnostics);
    if (!fragment || typeof fragment !== "object") continue;
    const capability = fragment.capability;
    const expectedCapability = path.basename(ref, ".json");
    if (typeof capability === "string" && capability !== expectedCapability) {
      diagnostics.push(diagnostic("invalid-fragment", `${ref} capability must match its filename ${expectedCapability}`));
    }
    if (typeof capability === "string") {
      const previousRef = fragmentCapabilities.get(capability);
      if (previousRef) {
        diagnostics.push(diagnostic("duplicate-fragment", `capability ${capability} is declared by both ${previousRef} and ${ref}`));
      } else {
        fragmentCapabilities.set(capability, ref);
      }
    }
    fragmentRecords.push({
      ref,
      capability,
      goUnits: Array.isArray(fragment.go?.units) ? fragment.go.units : [],
      browserUnits: Array.isArray(fragment.browser?.units) ? fragment.browser.units : [],
      workers: Array.isArray(fragment.workers) ? fragment.workers : [],
      sourceOwnership: Array.isArray(fragment.source_ownership) ? fragment.source_ownership : [],
    });
    for (const unit of Array.isArray(fragment.go?.units) ? fragment.go.units : []) {
      if (Object.prototype.hasOwnProperty.call(unit ?? {}, "capability")) {
        diagnostics.push(diagnostic("invalid-fragment", `${ref} Go unit ${unit?.id ?? "?"} must inherit capability`));
      }
      goUnits.push({ ...unit, capability });
    }
    for (const unit of Array.isArray(fragment.browser?.units) ? fragment.browser.units : []) {
      if (Object.prototype.hasOwnProperty.call(unit ?? {}, "capability")) {
        diagnostics.push(diagnostic("invalid-fragment", `${ref} browser unit ${unit?.id ?? "?"} must inherit capability`));
      }
      browserUnits.push({ ...unit, capability });
    }
    for (const worker of Array.isArray(fragment.workers) ? fragment.workers : []) workers.push(worker);
    for (const ownership of Array.isArray(fragment.source_ownership) ? fragment.source_ownership : []) {
      sourceOwnership.push({ ...ownership, capability, fragment: ref });
    }
  }

  const goOwners = new Map();
  for (const record of fragmentRecords) {
    for (const unit of record.goUnits) {
      if (typeof unit?.id !== "string") continue;
      const previous = goOwners.get(unit.id);
      if (previous) {
        diagnostics.push(diagnostic("duplicate-fragment", `Go unit ${unit.id} is owned by both ${previous.ref} and ${record.ref}`));
      } else {
        goOwners.set(unit.id, record);
      }
    }
  }
  const browserOwners = new Map();
  for (const record of fragmentRecords) {
    for (const unit of record.browserUnits) {
      if (typeof unit?.id !== "string") continue;
      const previous = browserOwners.get(unit.id);
      if (previous) {
        diagnostics.push(diagnostic("duplicate-fragment", `browser unit ${unit.id} is owned by both ${previous.ref} and ${record.ref}`));
      } else {
        browserOwners.set(unit.id, record);
      }
    }
  }
  const sourceOwners = new Map();
  for (const ownership of sourceOwnership) {
    const previous = sourceOwners.get(ownership.path);
    if (previous) {
      diagnostics.push(diagnostic("duplicate-fragment", `source ${ownership.path} is owned by both ${previous.fragment} and ${ownership.fragment}`));
    } else {
      sourceOwners.set(ownership.path, ownership);
    }
  }
  for (const [sourcePath, capability] of requiredSourceOwnership) {
    const ownership = sourceOwners.get(sourcePath);
    if (!ownership) {
      diagnostics.push(diagnostic("missing-source-ownership", `source ${sourcePath} must have an ownership record`));
      continue;
    }
    if (ownership.capability !== capability) {
      diagnostics.push(diagnostic("invalid-source-ownership", `source ${sourcePath} must be owned by ${capability}`));
    }
  }
  const workerPackage = (worker) => {
    if (typeof rootManifest.module !== "string" || typeof worker?.path !== "string") return null;
    const directory = path.posix.dirname(worker.path);
    if (directory === "." || directory.split("/").some((segment) => segment === "." || segment === ".." || segment.length === 0)) return null;
    return `${rootManifest.module}/${directory}`;
  };
  for (const record of fragmentRecords) {
    for (const worker of record.workers) {
      const packagePath = workerPackage(worker);
      if (!packagePath || typeof worker?.function !== "string" || worker.function.length === 0) continue;
      const ownedSource = sourceOwners.get(worker.path);
      if (ownedSource) {
        if (ownedSource.fragment !== record.ref) {
          diagnostics.push(diagnostic("invalid-fragment", `${record.ref} worker ${worker.path} must be owned by ${ownedSource.fragment}`));
        }
        continue;
      }
      const owner = goOwners.get(packagePath);
      if (!owner) {
        diagnostics.push(diagnostic("invalid-fragment", `${record.ref} worker ${worker.path} has no owned Go package`));
      } else if (owner !== record) {
        diagnostics.push(diagnostic("invalid-fragment", `${record.ref} worker ${worker.path} must be owned by ${owner.ref}`));
      }
    }
  }

  const merged = {
    schema_version: rootManifest.schema_version,
    module: rootManifest.module,
    allowed_targets: allowedTargets,
    go: {
      profiles: rootManifest.go?.profiles,
      units: goUnits,
    },
    browser: {
      entries: rootManifest.browser?.entries,
      exclusions: rootManifest.browser?.exclusions,
      units: browserUnits,
    },
    workers,
    source_ownership: sourceOwnership,
    fragments: normalizedRefs,
    load_diagnostics: diagnostics,
  };
  return merged;
}
