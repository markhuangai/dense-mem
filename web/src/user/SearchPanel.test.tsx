import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SearchPanel } from "./SearchPanel";
import { RecallPayload, UserApi } from "./api";

function payload(): RecallPayload {
  return {
    recall_id: "recall-1",
    results: [
      { evidence_id: "one", rank: 1, context: "Exact café Evidence.", source: "notes", equivalent_evidence_ids: ["equivalent"], equivalents_truncated: false, space_kind: "private", created_at: "2026-11-01T00:00:00Z" },
      { evidence_id: "two", rank: 2, context: "Another original observation.", source: "journal", equivalent_evidence_ids: [], equivalents_truncated: false, space_kind: "team_shared", created_at: "2026-11-02T00:00:00Z" },
    ],
    related_topics: [{ topic_id: "11111111-1111-4111-8111-111111111111", name: "Storage", description: "Durable storage notes", eligible_evidence_count: 2, rank: 1 }],
    topics_truncated: true, conflicts: [], search_states: { evidence: "current" }, degradations: [],
  };
}

async function search() {
  await userEvent.type(screen.getByLabelText("Keyword"), "postgres");
  await userEvent.click(screen.getByRole("button", { name: "Search" }));
  return screen.findByRole("listbox", { name: "Recall result list" });
}

describe("Evidence-only SearchPanel", () => {
  it("shows exact Evidence and follows a direct topic without a query", async () => {
    const recall = vi.fn().mockResolvedValue(payload());
    render(<SearchPanel api={{ recall } as unknown as UserApi} />);
    const list = await search();
    expect(within(list).getAllByRole("option")).toHaveLength(2);
    expect(list).toHaveTextContent("Exact café Evidence.");
    expect(screen.getByText("More related topics are available.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Storage · 2 Evidence" }));
    expect(recall).toHaveBeenLastCalledWith("", 10, { topicIDs: [payload().related_topics[0].topic_id] });
    expect(await screen.findByText("Topic: Storage")).toBeInTheDocument();
  });

  it("filters by source, sorts by date, and opens Evidence details by keyboard", async () => {
    render(<SearchPanel api={{ recall: vi.fn().mockResolvedValue(payload()) } as unknown as UserApi} />);
    const list = await search();
    await userEvent.selectOptions(screen.getByLabelText("Sort"), "date");
    expect(within(list).getAllByRole("option")[0]).toHaveTextContent("Another original observation.");
    await userEvent.selectOptions(screen.getByLabelText("Source"), "notes");
    const option = within(list).getByRole("option");
    option.focus();
    await userEvent.keyboard("{Enter}");
    const details = await screen.findByRole("dialog", { name: "Evidence details" });
    expect(details).toHaveTextContent("private");
    expect(details).toHaveTextContent("equivalent");
    expect(details).toHaveTextContent("Exact café Evidence.");
  });

  it("reports explicit topic failure and ordinary optional degradation visibly", async () => {
    const response = payload();
    response.degradations = [{ code: "topic_unavailable", message: "Topic discovery is unavailable." }];
    response.conflicts = [{ conflict_id: "conflict", status: "open" }];
    const recall = vi.fn().mockResolvedValueOnce(response).mockRejectedValueOnce(new Error("Topic is unavailable; refresh the topic list."));
    render(<SearchPanel api={{ recall } as unknown as UserApi} />);
    const list = await search();
    expect(screen.getByText("Topic discovery is unavailable.")).toBeInTheDocument();
    expect(screen.getByText("1 Evidence conflicts are included in this recall.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Storage · 2 Evidence" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Topic is unavailable");
    expect(list).toHaveTextContent("Exact café Evidence.");
  });

  it("ignores a late response after the authenticated API changes", async () => {
    let finish!: (value: RecallPayload) => void;
    const pending = new Promise<RecallPayload>((resolve) => { finish = resolve; });
    const first = { recall: vi.fn().mockReturnValue(pending) } as unknown as UserApi;
    const second = { recall: vi.fn().mockResolvedValue({ ...payload(), results: [] }) } as unknown as UserApi;
    const view = render(<SearchPanel api={first} />);
    await userEvent.type(screen.getByLabelText("Keyword"), "first team");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    view.rerender(<SearchPanel api={second} />);
    await act(async () => { finish(payload()); await pending; });
    expect(screen.queryByText("Exact café Evidence.")).not.toBeInTheDocument();
  });
});
