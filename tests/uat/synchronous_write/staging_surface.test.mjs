import assert from "node:assert/strict";
import test from "node:test";

import {
  LEGACY_TOOLS,
  LEGACY_SURFACE,
  TERMINAL_SURFACE,
  TERMINAL_TOOLS,
  assertTerminalCorrectionResult,
  assertTerminalRememberResult,
  classifyStagingSurface,
} from "./surface.mjs";

test("classifies the legacy polling surface", () => {
  assert.equal(classifyStagingSurface({
    tools: [...TERMINAL_TOOLS, "get_submission_status"],
    remember: { status_tool: "get_submission_status", check_after_seconds: 60 },
    status: {},
  }), LEGACY_SURFACE);
});

test("classifies the terminal surface", () => {
  const remember = { processing_state: "completed", search_state: "current", evidence: [], relationship_results: [] };
  const correction = { processing_state: "completed", search_state: "current", correction_result: {}, errors: [], correlation_id: "corr" };
  assert.equal(classifyStagingSurface({
    tools: TERMINAL_TOOLS,
    remember,
    correction,
    removedToolFailure: true,
  }), TERMINAL_SURFACE);
  assert.doesNotThrow(() => assertTerminalRememberResult(remember));
  assert.doesNotThrow(() => assertTerminalCorrectionResult(correction));
});

test("fails closed on mixed or ambiguous surfaces", () => {
  assert.throws(() => classifyStagingSurface({
    tools: [...TERMINAL_TOOLS, "get_submission_status"],
    remember: { processing_state: "completed", search_state: "current", evidence: [], relationship_results: [] },
    status: {},
  }), /mixed or ambiguous/);
  assert.throws(() => classifyStagingSurface({
    tools: TERMINAL_TOOLS,
    remember: { processing_state: "completed", search_state: "current", evidence: [], relationship_results: [] },
    removedToolFailure: false,
  }), /mixed or ambiguous/);
  assert.throws(() => classifyStagingSurface({
    tools: LEGACY_TOOLS.filter((name) => name !== "trace_memory"),
    remember: { status_tool: "get_submission_status", check_after_seconds: 60 },
    status: {},
  }), /mixed or ambiguous/);
  assert.throws(() => classifyStagingSurface({
    tools: TERMINAL_TOOLS,
    remember: { processing_state: "completed", search_state: "current", evidence: [], relationship_results: [] },
    correction: { processing_state: "completed", search_state: "current", correction_result: {}, errors: [], correlation_id: "corr", status_tool: "get_submission_status" },
    removedToolFailure: true,
  }), /mixed or ambiguous/);
  assert.throws(() => classifyStagingSurface({
    tools: TERMINAL_TOOLS,
    remember: { processing_state: "completed", search_state: "current", evidence: [], relationship_results: [], check_after_seconds: 60 },
    correction: { processing_state: "completed", search_state: "current", correction_result: {}, errors: [], correlation_id: "corr" },
    removedToolFailure: true,
  }), /mixed or ambiguous/);
});
