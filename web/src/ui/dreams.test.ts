import { describe, expect, it } from "vitest";
import { runOutcome } from "./dreams";

describe("runOutcome", () => {
  it("reports evidence progress in two-pass units", () => {
    expect(runOutcome({
      lane: "evidence_discovery",
      evidence_targets: 3,
      evaluated_evidence_targets: 2,
      outcome_summary: { provider_proposals: 1 },
    })).toBe("2 of 6 evidence target passes evaluated");
  });

  it("stores evidence results after every target pass", () => {
    expect(runOutcome({
      lane: "evidence_discovery",
      evidence_targets: 1,
      evaluated_evidence_targets: 2,
      outcome_summary: { provider_proposals: 1 },
    })).toBe("Evidence discovery stored");
  });

  it("reports a completed one-pass run without proposals", () => {
    expect(runOutcome({
      lane: "evidence_discovery",
      evidence_targets: 3,
      evaluated_evidence_targets: 3,
      outcome_summary: { provider_proposals: 0 },
    })).toBe("Provider returned no supported relationship");
  });
});
