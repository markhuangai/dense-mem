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

const TERMINAL_PROCESSING_STATES = new Set(["completed", "rejected", "quarantined", "failed"]);
const CORRECTION_PROCESSING_STATES = new Set(["awaiting_confirmation", "completed", "rejected", "failed"]);
const TERMINAL_SEARCH_STATES = new Set(["current", "not_required"]);

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
  if (!result || !CORRECTION_PROCESSING_STATES.has(result.processing_state) ||
      !Array.isArray(result.errors) ||
      typeof result.correlation_id !== "string" ||
      result.correlation_id.trim() === "" ||
      Object.hasOwn(result, "status_tool") || Object.hasOwn(result, "check_after_seconds")) {
    throw new Error("terminal correction result must be direct and complete");
  }
  if (result.processing_state === "awaiting_confirmation" && !result.awaiting_confirmation) {
    throw new Error("awaiting correction result must include confirmation details");
  }
  if (result.processing_state === "completed" && !result.correction_result) {
    throw new Error("completed correction result must include correction details");
  }
  if ((result.processing_state === "rejected" || result.processing_state === "failed") && result.errors.length === 0) {
    throw new Error("rejected correction result must include errors");
  }
  return result;
}
