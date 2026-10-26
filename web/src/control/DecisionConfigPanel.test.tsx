import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ControlApi, type DecisionConfig } from "../api";
import { jsonResponse } from "../App.test-helpers";
import { DecisionConfigPanel } from "./ConfigPanel";

const keys = ["DECISION_DEDUPLICATION_ENABLED", "DECISION_SECURITY_REVIEW_ENABLED", "DECISION_CONFLICT_REVIEW_ENABLED", "DECISION_RECALL_RERANKING_ENABLED"];
function snapshot(configured = true): DecisionConfig {
  const tasks = { deduplication: true, security_review: true, conflict_review: true, recall_reranking: true };
  return { update_time: "2026-10-26T12:00:00Z", items: keys.map((key) => ({ key, value: "", effective_value: String(configured), updated_at: "2026-10-26T12:00:00Z", source: "default" })), effective: { revision: "revision-one", provider: { configured, provider: "openai", model: "gpt-6-luna", max_input_tokens: 32000, max_question_input_tokens: 32000, long_context_enabled: false }, preferences: tasks, effective: { ...tasks, ...(!configured ? { deduplication: false, security_review: false, conflict_review: false, recall_reranking: false } : {}) }, overrides: { deduplication: false, security_review: false, conflict_review: false, recall_reranking: false }, stale: false, last_verified_at: "2026-10-26T12:00:00Z" } };
}
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("Decision preferences", () => {
  it("shows defaults and submits only the changed preference", async () => {
    let current = snapshot();
    const fetcher = vi.fn(async (_url: unknown, init?: RequestInit) => {
      if (init?.method === "PATCH") {
        expect(JSON.parse(String(init.body))).toEqual({ items: [{ key: keys[1], value: "false" }] });
        current = { ...current, items: current.items.map((item) => item.key === keys[1] ? { ...item, value: "false", effective_value: "false", source: "override" as const } : item), effective: { ...current.effective, revision: "revision-two", preferences: { ...current.effective.preferences, security_review: false }, effective: { ...current.effective.effective, security_review: false } } };
      }
      return jsonResponse({ data: current });
    });
    vi.stubGlobal("fetch", fetcher);
    render(<DecisionConfigPanel api={new ControlApi("http://localhost", "fixture-control")} />);
    expect(await screen.findByText(/Provider configured: openai/)).toBeVisible();
    expect(screen.getAllByRole("switch")).toHaveLength(4);
    for (const control of screen.getAllByRole("switch")) expect(control).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("button", { name: "Save config" })).toBeDisabled();
    fireEvent.click(screen.getByRole("switch", { name: /Security review/ }));
    fireEvent.click(screen.getByRole("button", { name: "Save config" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Changes saved");
    expect(screen.getByText("revision-two")).toBeVisible();
    expect(screen.getByText("Saved off · Effective off")).toBeVisible();
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("keeps default preferences on when the provider is unconfigured", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ data: snapshot(false) })));
    render(<DecisionConfigPanel api={new ControlApi("http://localhost", "fixture-control")} />);
    expect(await screen.findByText(/Decision provider is not configured/)).toBeVisible();
    for (const control of screen.getAllByRole("switch")) expect(control).toHaveAttribute("aria-checked", "true");
    expect(screen.getAllByText("Default on · Effective off")).toHaveLength(4);
  });

  it("shows loading, failed save, and stale snapshot states without losing edits", async () => {
    let resolve: (response: Response) => void = () => {};
    const fetcher = vi.fn().mockImplementationOnce(() => new Promise<Response>((done) => { resolve = done; })).mockRejectedValueOnce(new Error("configuration unavailable"));
    vi.stubGlobal("fetch", fetcher);
    render(<DecisionConfigPanel api={new ControlApi("http://localhost", "fixture-control")} />);
    expect(screen.getByText("Loading config")).toBeVisible();
    const config = snapshot(); config.effective.stale = true;
    resolve(jsonResponse({ data: config }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Settings refresh failed");
    fireEvent.click(screen.getByRole("switch", { name: /Recall reranking/ }));
    fireEvent.click(screen.getByRole("button", { name: "Save config" }));
    await waitFor(() => expect(screen.getAllByRole("alert").some((alert) => alert.textContent?.includes("configuration unavailable"))).toBe(true));
    expect(screen.getByRole("switch", { name: /Recall reranking/ })).toHaveAttribute("aria-checked", "false");
  });
});
