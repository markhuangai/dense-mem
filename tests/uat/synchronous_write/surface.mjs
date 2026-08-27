export const LEGACY_SURFACE = "legacy";
export const TERMINAL_SURFACE = "terminal";

export const TERMINAL_TOOLS = Object.freeze([
  "remember",
  "retract_evidence",
  "correct_relationship",
  "recall_memory",
  "trace_memory",
  "submit_recall_session_feedback",
  "list_dreams",
  "get_dream",
  "resolve_dream_feedback",
  "export_memory_pack",
]);

export const LEGACY_TOOLS = Object.freeze([...TERMINAL_TOOLS, "get_submission_status"]);
// Kept as an alias for callers that used the original name before the exact
// legacy catalog was frozen.
export const LEGACY_REQUIRED_TOOLS = LEGACY_TOOLS;

function hasTool(names, name) {
  return names instanceof Set ? names.has(name) : names.includes(name);
}

function exactToolSet(names, expected) {
  return names.size === expected.length && expected.every((name) => names.has(name));
}

const TERMINAL_PROCESSING_STATES = new Set(["completed", "rejected", "quarantined", "failed"]);
const TERMINAL_SEARCH_STATES = new Set(["current", "not_required"]);

export function assertLegacyRememberReceipt(result) {
  if (!result || result.status_tool !== "get_submission_status" ||
      !Number.isInteger(result.check_after_seconds) || result.check_after_seconds < 1 ||
      result.check_after_seconds > 600) {
    throw new Error("legacy Remember receipt must advertise bounded status polling");
  }
  return result;
}

export function assertTerminalRememberResult(result) {
  if (!result || !TERMINAL_PROCESSING_STATES.has(result.processing_state) ||
      !TERMINAL_SEARCH_STATES.has(result.search_state) ||
      !Array.isArray(result.evidence) || !Array.isArray(result.relationship_results) ||
      Object.hasOwn(result, "status_tool") || Object.hasOwn(result, "check_after_seconds")) {
    throw new Error("terminal Remember result must be complete and polling-free");
  }
  return result;
}

export function assertTerminalCorrectionResult(result) {
  if (!result || !TERMINAL_PROCESSING_STATES.has(result.processing_state) ||
      !Array.isArray(result.relationship_results) ||
      typeof result.correlation_id !== "string" ||
      result.correlation_id.trim() === "" ||
      Object.hasOwn(result, "status_tool") || Object.hasOwn(result, "check_after_seconds")) {
    throw new Error("terminal correction result must be direct and complete");
  }
  return result;
}

export function assertRemovedStatusFailure(value) {
  if (value !== true) throw new Error("removed get_submission_status must fail with bounded method-not-found");
  return value;
}

// Classifies the observable staging contract and fails closed for a mixed
// surface so tests cannot accidentally assert the wrong mode.
export function classifyStagingSurface({ tools, remember, status, correction, removedToolFailure }) {
  const names = tools instanceof Set ? tools : new Set((tools || []).map((tool) => typeof tool === "string" ? tool : tool.name));
  let legacyReceipt = false;
  try {
    assertLegacyRememberReceipt(remember);
    legacyReceipt = true;
  } catch {
    // The terminal branch is evaluated below.
  }
  let terminalResult = false;
  try {
    assertTerminalRememberResult(remember);
    terminalResult = true;
  } catch {
    // The legacy branch is evaluated above.
  }
  let terminalCorrection = false;
  try {
    assertTerminalCorrectionResult(correction);
    terminalCorrection = true;
  } catch {
    // A terminal catalog is valid only when correction is direct as well.
  }
  const legacy = exactToolSet(names, LEGACY_TOOLS) &&
    legacyReceipt && status !== undefined;
  const terminal = exactToolSet(names, TERMINAL_TOOLS) && !hasTool(names, "get_submission_status") &&
    terminalResult && terminalCorrection && removedToolFailure === true &&
    status === undefined;
  if (legacy && !terminal) return LEGACY_SURFACE;
  if (terminal && !legacy) return TERMINAL_SURFACE;
  throw new Error("staging MCP surface is mixed or ambiguous");
}

export function assertStagingSurface(surface, payload) {
  const mode = classifyStagingSurface(payload);
  if (mode !== surface) {
    throw new Error(`expected ${surface} staging surface, found ${mode}`);
  }
  return mode;
}
