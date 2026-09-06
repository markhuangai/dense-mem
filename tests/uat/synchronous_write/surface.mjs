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

const TERMINAL_PROCESSING_STATES = new Set(["completed", "failed"]);
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

export function assertActionableInvalidInput(raw, component, label, expect) {
  const structured = raw?.result?.structuredContent;
  expect(!raw?.error && raw?.result?.isError === true && structured && typeof structured === "object" && !Array.isArray(structured), `${label} must return a structured MCP error`);
  const text = raw?.result?.content?.[0]?.text;
  let parsed;
  try {
    parsed = typeof text === "string" ? JSON.parse(text) : null;
  } catch {
    parsed = null;
  }
  expect(parsed && typeof parsed === "object" && !Array.isArray(parsed), `${label} must include valid JSON text content`);
  expect(stableJSON(parsed) === stableJSON(structured), `${label} text and structuredContent must be equivalent`);
  expect(structured.code === "invalid_input" && structured.reason_code === "invalid_request" &&
    structured.next_action === "correct_and_resubmit" && structured.retryable === false,
  `${label} must expose correct-and-resubmit guidance: ${JSON.stringify(structured)}`);
  const details = structured.details;
  expect(details && details.component === component && details.client_controlled === true,
    `${label} must identify the client-controlled component: ${JSON.stringify(structured)}`);
  const field = component.split(".").at(-1);
  expect(typeof structured.message === "string" && structured.message.includes(field),
    `${label} message must identify ${field}: ${JSON.stringify(structured)}`);
  return structured;
}

export function assertContractInvalidInput(raw, component, label, expect) {
  const data = raw?.error?.data;
  expect(!raw?.result && raw?.error?.code === -32602 && data && typeof data === "object" && !Array.isArray(data), `${label} must return a contract validation error`);
  expect(data.code === "invalid_input" && data.reason_code === "validation_failed" &&
    data.next_action === "correct_and_resubmit" && data.retryable === false,
  `${label} must expose validation guidance: ${JSON.stringify(data)}`);
  const field = component.split(".").at(-1);
  const issue = Array.isArray(data.issues) && data.issues.find((candidate) => typeof candidate?.message === "string" && candidate.message.includes(field));
  expect(issue, `${label} must identify ${field}: ${JSON.stringify(data)}`);
  return data;
}

function stableJSON(value) {
  if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}
