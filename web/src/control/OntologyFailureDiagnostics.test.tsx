import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ControlApi, OperationLog } from "../api";
import { OntologyFailureDiagnostics, reconstructOntologyExchanges } from "./OntologyFailureDiagnostics";

const { createHash, webcrypto } = await vi.importActual<{
  createHash(algorithm: "sha256"): { update(value: string): { digest(encoding: "hex"): string } };
  webcrypto: Crypto;
}>("node:crypto");

beforeEach(() => vi.stubGlobal("crypto", webcrypto));
afterEach(() => vi.unstubAllGlobals());

function fixture() {
  const base: OperationLog = { id: "manifest", timestamp: "2026-11-02T22:00:00Z", severity: "DEBUG", severity_rank: 10, message: "ontology_failure_capture", team_id: "team-a", profile_id: null, correlation_id: "", error: "", attrs: { record_count: 3, exchange_count: 1, attempt_id: "capture", reference_id: "run", assessment_id: "assessment", batch_id: "batch", failure_code: "provider_response_invalid", attempts: [{ validation: { code: "item_locked_classification" } }] } };
  const request = ` {"secret":"ordinary admitted fact", "messages":["original context"]} `;
  const response = ` {"choices":[{"message":{"content":"malformed response"}}]} `;
  const chunks = [request.slice(0, 20), request.slice(20), response];
  const rows: OperationLog[] = chunks.map((body, index) => ({ ...base, id: `payload-${index}`, message: "ontology_failure_payload", attrs: { ...base.attrs, exchange: 0, direction: index === 2 ? "response" : "request", component: "assessor", model: "configured-model", status_code: 200, capture_state: "captured", payload_chunk: { index: index === 2 ? 0 : index, count: index === 2 ? 1 : 2, body, state: "captured", sha256: createHash("sha256").update(index === 2 ? response : request).digest("hex") } } }));
  return { manifest: base, rows, request, response };
}

describe("Ontology failure exchanges", () => {
  it("reconstructs and verifies complete bodies regardless of row ordering", async () => {
    const f = fixture();
    const exchanges = await reconstructOntologyExchanges(f.manifest, [...f.rows].reverse());
    expect(exchanges[0].request.body).toBe(f.request);
    expect(exchanges[0].response.body).toBe(f.response);
  });

  it("rejects missing, duplicate, altered and cross-team capture rows", async () => {
    const f = fixture();
    await expect(reconstructOntologyExchanges(f.manifest, f.rows.slice(1))).rejects.toThrow("incomplete or expired");
    await expect(reconstructOntologyExchanges(f.manifest, [f.rows[0], f.rows[0], f.rows[2]])).rejects.toThrow("missing or inconsistent");
    const altered = structuredClone(f.rows);
    (altered[0].attrs!.payload_chunk as { body: string }).body += "changed";
    await expect(reconstructOntologyExchanges(f.manifest, altered)).rejects.toThrow("integrity verification failed");
    await expect(reconstructOntologyExchanges(f.manifest, f.rows.map((row) => ({ ...row, team_id: "team-b" })))).rejects.toThrow("identity does not match");
  });

  it.each([{ reason: 2, state: "over_budget", message: "The body exceeded the diagnostic capture budget." }, { reason: 6, state: "unavailable", message: "Capture state: unavailable." }])("preserves and displays capture reason $reason", async ({ reason, state, message }) => {
    const f = fixture();
    const rows = f.rows.map((row, index) => index === 2 ? { ...row, attrs: { ...row.attrs, payload_chunk: { index: 0, count: 1, body: "", state: "unavailable", reason } } } : row);
    const exchanges = await reconstructOntologyExchanges(f.manifest, rows);
    expect(exchanges[0].response).toEqual({ body: "", state, reason: `credential_protection_${reason}` });
    const api = { listOperationLogs: vi.fn().mockImplementation((query) => Promise.resolve(query.event === "ontology_failure_capture" ? { data: [f.manifest], pagination: { total: 1 } } : { data: rows, pagination: { total: 3 } })) } as unknown as ControlApi;
    render(<OntologyFailureDiagnostics api={api} runId="run" />);
    fireEvent.click(await screen.findByRole("button", { name: "View failed assessment assessme" }));
    expect(await screen.findByText(new RegExp(message.replaceAll(".", "\\.")))).toHaveTextContent(`credential_protection_${reason}`);
  });

  it("displays a failure's request, raw response and validation diagnostics on demand", async () => {
    const f = fixture();
    const api = { listOperationLogs: vi.fn().mockImplementation((query) => Promise.resolve(query.event === "ontology_failure_capture" ? { data: [f.manifest], pagination: { total: 1 } } : { data: f.rows, pagination: { total: 3 } })) } as unknown as ControlApi;
    render(<OntologyFailureDiagnostics api={api} runId="run" />);
    expect(screen.queryByText(f.request)).not.toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "View failed assessment assessme" }));
    expect(await screen.findByText(f.request.trim(), { exact: false })).toBeVisible();
    expect(screen.getByText(f.response.trim(), { exact: false })).toBeVisible();
    expect(screen.getByText(/item_locked_classification/)).toBeInTheDocument();
    expect(api.listOperationLogs).toHaveBeenLastCalledWith(expect.objectContaining({ team_id: "team-a", attempt_id: "capture", reference_id: "run" }));
  });

  it("reports expired captures instead of displaying an empty successful response", async () => {
    const f = fixture();
    const api = { listOperationLogs: vi.fn().mockImplementation((query) => Promise.resolve(query.event === "ontology_failure_capture" ? { data: [f.manifest], pagination: { total: 1 } } : { data: [], pagination: { total: 0 } })) } as unknown as ControlApi;
    render(<OntologyFailureDiagnostics api={api} runId="run" />);
    fireEvent.click(await screen.findByRole("button", { name: "View failed assessment assessme" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("incomplete or expired");
    expect(screen.queryByText("AI response")).not.toBeInTheDocument();
  });
});
