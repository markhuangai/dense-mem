import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApiError, ControlApi, Team } from "../api";
import type { EvidenceConflict, EvidenceConflictDetail, EvidenceConflictListPage } from "../evidence-conflict-api-types";
import { ConflictQueuePanel } from "./ConflictQueuePanel";

describe("Evidence conflicts",()=>{
  it("navigates evidence pages and appends paged review history", async () => {
    const first = evidenceConflictPage({ next_cursor: "evidence-cursor-2" });
    const second = evidenceConflictPage({ items: [evidenceConflict({ conflict_id: "second" })] });
    const detail = evidenceConflictDetail({ next_event_cursor: "event-cursor-2" });
    const older = evidenceConflictDetail({ events: [{ ...detail.conflict.events![0], event_id: "event-2", action: "recited", ordinal: 1 }], next_event_cursor: null });
    const listEvidenceConflicts = vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(second);
    const getEvidenceConflict = vi.fn().mockResolvedValueOnce(detail).mockResolvedValueOnce(older);
    const api = {
      listEvidenceConflicts,
      getEvidenceConflict,
    } as unknown as ControlApi;
    const user = userEvent.setup();

    render(<ConflictQueuePanel api={api} team={team()} />);
    expect(await screen.findByText("2 cited positions")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(listEvidenceConflicts).toHaveBeenLastCalledWith("team-1", { status: "open", limit: 25, cursor: "evidence-cursor-2" }));
    await screen.findByText(/second/);

    await user.click(screen.getByRole("button", { name: /cited positions/ }));
    await user.click(screen.getByText(/Review history/));
    await user.click(screen.getByRole("button", { name: "Load older history" }));
    await waitFor(() => expect(getEvidenceConflict).toHaveBeenLastCalledWith("team-1", "conflict-1", 50, "event-cursor-2"));
    expect(screen.getByText("Review history (2)")).toBeInTheDocument();
  });

  it("keeps the review reason after a stale resolution refetch", async () => {
    const detail = evidenceConflictDetail();
    const latest = evidenceConflictDetail({ version: 2 });
    let resolveLatest: ((value: EvidenceConflictDetail) => void) | undefined;
    const resolveEvidenceConflict = vi.fn().mockRejectedValue(new ApiError(409, "stale"));
    const api = {
      listEvidenceConflicts: vi.fn().mockResolvedValue(evidenceConflictPage()),
      getEvidenceConflict: vi.fn().mockResolvedValueOnce(detail).mockImplementationOnce(() => new Promise<EvidenceConflictDetail>((resolve) => { resolveLatest = resolve; })),
      resolveEvidenceConflict,
    } as unknown as ControlApi;
    const user = userEvent.setup();

    render(<ConflictQueuePanel api={api} team={team()} />);
    await user.click(await screen.findByRole("button", { name: /cited positions/ }));
    const textarea = screen.getByLabelText("Review reason");
    await user.type(textarea, "keep this reason");
    await user.click(screen.getByRole("button", { name: "Resolve" }));
    await waitFor(() => expect(resolveEvidenceConflict).toHaveBeenCalled());
    expect(screen.getByLabelText("Review reason")).toHaveValue("keep this reason");
    expect(await screen.findByText(/latest version is loaded/)).toBeInTheDocument();
    resolveLatest?.(latest);
    await waitFor(() => expect(screen.getByText("open · v2")).toBeInTheDocument());
    expect(screen.getByText(/latest version is loaded/)).toBeInTheDocument();
  });

  it("shows detail errors while an earlier selection remains visible", async () => {
    const detail = evidenceConflictDetail();
    const getEvidenceConflict = vi.fn()
      .mockResolvedValueOnce(detail)
      .mockRejectedValueOnce(new ApiError(500, "detail unavailable"));
    const api = {
      listEvidenceConflicts: vi.fn().mockResolvedValue(evidenceConflictPage()),
      getEvidenceConflict,
    } as unknown as ControlApi;
    const user = userEvent.setup();

    render(<ConflictQueuePanel api={api} team={team()} />);
    const card = await screen.findByRole("button", { name: /2 cited positions/ });
    await user.click(card);
    await screen.findByText("Cited evidence conflict");
    await user.click(card);

    expect(await screen.findByText("Evidence conflicts unavailable.")).toBeInTheDocument();
    expect(screen.getByText("detail unavailable")).toBeInTheDocument();
    expect(screen.getByText("Cited evidence conflict")).toBeInTheDocument();
  });

  it("ignores a stale resolution refetch after selecting another case", async () => {
    const first = evidenceConflict({ conflict_id: "first", positions: [{ ...evidenceConflict().positions[0], quote: "first detail" }, { ...evidenceConflict().positions[1] }] });
    const second = evidenceConflict({ conflict_id: "second", positions: [{ ...evidenceConflict().positions[0], quote: "second detail" }, { ...evidenceConflict().positions[1] }] });
    const latestFirst = evidenceConflictDetail({ version: 2, events: [] });
    let resolveLatestFirst: ((value: EvidenceConflictDetail) => void) | undefined;
    let resolveSecond: ((value: EvidenceConflictDetail) => void) | undefined;
    const getEvidenceConflict = vi.fn((_: string, conflictID: string) => {
      if (conflictID === "first" && getEvidenceConflict.mock.calls.filter(([, id]) => id === "first").length === 1) {
        return Promise.resolve({ conflict: first, next_event_cursor: null });
      }
      if (conflictID === "first") {
        return new Promise<EvidenceConflictDetail>((resolve) => { resolveLatestFirst = resolve; });
      }
      return new Promise<EvidenceConflictDetail>((resolve) => { resolveSecond = resolve; });
    });
    const api = {
      listEvidenceConflicts: vi.fn().mockResolvedValue(evidenceConflictPage({ items: [first, second] })),
      getEvidenceConflict,
      resolveEvidenceConflict: vi.fn().mockRejectedValue(new ApiError(409, "stale")),
    } as unknown as ControlApi;
    const user = userEvent.setup();

    render(<ConflictQueuePanel api={api} team={team()} />);
    const cards = await screen.findAllByRole("button", { name: /2 cited positions/ });
    await user.click(cards[0]);
    await screen.findAllByText("first detail");
    await user.type(screen.getByLabelText("Review reason"), "keep this reason");
    await user.click(screen.getByRole("button", { name: "Resolve" }));
    await waitFor(() => expect(getEvidenceConflict).toHaveBeenCalledTimes(2));

    await user.click(cards[1]);
    expect(screen.queryByText(/latest version is loaded/)).not.toBeInTheDocument();
    resolveSecond?.({ conflict: second, next_event_cursor: null });
    await waitFor(() => expect(screen.getAllByText("second detail").length).toBeGreaterThan(0));
    resolveLatestFirst?.(latestFirst);
    await waitFor(() => expect(screen.getAllByText("second detail").length).toBeGreaterThan(0));
    expect(screen.queryByText("first detail")).not.toBeInTheDocument();
    expect(screen.queryByText(/latest version is loaded/)).not.toBeInTheDocument();
  });

  it("invalidates a pending detail request when the evidence status changes", async () => {
    let resolveDetail: ((value: EvidenceConflictDetail) => void) | undefined;
    const getEvidenceConflict = vi.fn(() => new Promise<EvidenceConflictDetail>((resolve) => { resolveDetail = resolve; }));
    const api = {
      listEvidenceConflicts: vi.fn().mockResolvedValue(evidenceConflictPage({ items: [] })),
      getEvidenceConflict,
    } as unknown as ControlApi;
    const user = userEvent.setup();

    render(<ConflictQueuePanel api={api} team={team()} />);
    await screen.findByText("No evidence conflicts match this view.");
    // The empty list is replaced with a selectable case for this request only.
    const listEvidenceConflicts = api.listEvidenceConflicts as ReturnType<typeof vi.fn>;
    listEvidenceConflicts.mockResolvedValueOnce(evidenceConflictPage());
    await user.selectOptions(screen.getByLabelText("Show"), "resolved");
    await waitFor(() => expect(listEvidenceConflicts).toHaveBeenLastCalledWith("team-1", { status: "resolved", limit: 25, cursor: "" }));
    // Switch back to open to obtain a card, then leave it selected while its detail is pending.
    listEvidenceConflicts.mockResolvedValueOnce(evidenceConflictPage());
    await user.selectOptions(screen.getByLabelText("Show"), "open");
    const card = await screen.findByRole("button", { name: /2 cited positions/ });
    await user.click(card);
    await user.selectOptions(screen.getByLabelText("Show"), "resolved");
    resolveDetail?.(evidenceConflictDetail());
    await waitFor(() => expect(screen.queryByText("Cited evidence conflict")).not.toBeInTheDocument());
  });

  it("renders cited quotes as text", async () => {
    const malicious = evidenceConflict({ positions: [{ ...evidenceConflict().positions[0], quote: "<img src=x onerror=alert(1)>" }, { ...evidenceConflict().positions[1] }] });
    const api = {
      listEvidenceConflicts: vi.fn().mockResolvedValue(evidenceConflictPage({ items: [malicious] })),
      getEvidenceConflict: vi.fn().mockResolvedValue({ conflict: malicious, next_event_cursor: null }),
    } as unknown as ControlApi;
    const user = userEvent.setup();

    render(<ConflictQueuePanel api={api} team={team()} />);
    await user.click(await screen.findByRole("button", { name: /2 cited positions/ }));
    expect(screen.getAllByText("<img src=x onerror=alert(1)>", { exact: true })[0]).toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
  });

  it("refreshes the first evidence page after a successful resolution", async () => {
    const detail = evidenceConflictDetail();
    const listEvidenceConflicts = vi.fn()
      .mockResolvedValueOnce(evidenceConflictPage())
      .mockResolvedValueOnce(evidenceConflictPage({ items: [] }));
    const api = {
      listEvidenceConflicts,
      getEvidenceConflict: vi.fn().mockResolvedValue(detail),
      resolveEvidenceConflict: vi.fn().mockResolvedValue({ conflict: { ...detail.conflict, status: "resolved", version: 2 } }),
    } as unknown as ControlApi;
    const user = userEvent.setup();

    render(<ConflictQueuePanel api={api} team={team()} />);
    await user.click(await screen.findByRole("button", { name: /cited positions/ }));
    await user.type(screen.getByLabelText("Review reason"), "reviewed");
    await user.click(screen.getByRole("button", { name: "Resolve" }));

    await waitFor(() => expect(listEvidenceConflicts).toHaveBeenLastCalledWith("team-1", { status: "open", limit: 25, cursor: "" }));
    expect(await screen.findByText("No evidence conflicts match this view.")).toBeInTheDocument();
  });

  it("keeps the latest same-team detail selection when requests resolve out of order", async () => {
    const first = evidenceConflict({ conflict_id: "first", positions: [{ ...evidenceConflict().positions[0], quote: "first detail" }, { ...evidenceConflict().positions[1] }] });
    const second = evidenceConflict({ conflict_id: "second", positions: [{ ...evidenceConflict().positions[0], quote: "second detail" }, { ...evidenceConflict().positions[1] }] });
    let resolveFirst: ((value: EvidenceConflictDetail) => void) | undefined;
    let resolveSecond: ((value: EvidenceConflictDetail) => void) | undefined;
    const getEvidenceConflict = vi.fn((_: string, conflictID: string) => new Promise<EvidenceConflictDetail>((resolve) => {
      if (conflictID === "first") resolveFirst = resolve;
      else resolveSecond = resolve;
    }));
    const api = {
      listEvidenceConflicts: vi.fn().mockResolvedValue(evidenceConflictPage({ items: [first, second] })),
      getEvidenceConflict,
    } as unknown as ControlApi;
    const user = userEvent.setup();

    render(<ConflictQueuePanel api={api} team={team()} />);
    const cards = await screen.findAllByRole("button", { name: /2 cited positions/ });
    await user.click(cards[0]);
    await user.click(cards[1]);
    resolveSecond?.({ conflict: second, next_event_cursor: null });
    await waitFor(() => expect(screen.getAllByText("second detail").length).toBeGreaterThan(0));
    resolveFirst?.({ conflict: first, next_event_cursor: null });
    await waitFor(() => expect(screen.getAllByText("second detail").length).toBeGreaterThan(0));
    expect(screen.queryByText("first detail")).not.toBeInTheDocument();
  });

  it("clears evidence data on team switch and ignores an old detail response", async () => {
    const first = evidenceConflictPage();
    first.items[0].positions[0].quote = "team A quote";
    const second = evidenceConflictPage({ items: [evidenceConflict({ conflict_id: "team-b-conflict" })] });
    const listEvidenceConflicts = vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(second);
    let resolveDetail: ((value: EvidenceConflictDetail) => void) | undefined;
    const getEvidenceConflict = vi.fn(() => new Promise<EvidenceConflictDetail>((resolve) => {
      resolveDetail = resolve;
    }));
    const api = {
      listEvidenceConflicts,
      getEvidenceConflict,
    } as unknown as ControlApi;
    const { rerender } = render(<ConflictQueuePanel api={api} team={team()} />);
    const user = userEvent.setup();

    await screen.findByText("2 cited positions");
    await user.click(screen.getByRole("button", { name: /cited positions/ }));
    rerender(<ConflictQueuePanel api={api} team={team({ id: "team-2" })} />);

    await waitFor(() => expect(listEvidenceConflicts).toHaveBeenLastCalledWith("team-2", { status: "open", limit: 25, cursor: "" }));
    expect(screen.queryByText("team A quote")).not.toBeInTheDocument();
    resolveDetail?.(evidenceConflictDetail({ events: [], next_event_cursor: null }));
    await waitFor(() => expect(screen.queryByText("team A quote")).not.toBeInTheDocument());
  });
});
function team(overrides: Partial<Team> = {}): Team {
  return {
    id: "team-1",
    name: "Research",
    description: "",
    metadata: null,
    config: null,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    ...overrides,
  };
}

function evidenceConflict(overrides: Partial<EvidenceConflict> = {}): EvidenceConflict {
  return {
    team_id: "team-1",
    conflict_id: "conflict-1",
    space_id: "space-1",
    space_generation: 1,
    kind: "evidence_conflict",
    status: "open",
    version: 1,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    positions: [{
      position_id: "position-1", evidence_id: "evidence-1", occurrence_id: "occurrence-1",
      quote: "first quote", span_start: 0, span_end: 5, authority: "primary", submitted: true,
      created_at: "2026-08-01T00:00:00Z",
    }, {
      position_id: "position-2", evidence_id: "evidence-2", occurrence_id: "occurrence-2",
      quote: "other quote", span_start: 0, span_end: 5, authority: "secondary", submitted: false,
      created_at: "2026-08-01T00:00:00Z",
    }],
    ...overrides,
  };
}

function evidenceConflictPage(overrides: Partial<EvidenceConflictListPage> = {}): EvidenceConflictListPage {
  return { items: [evidenceConflict()], next_cursor: null, ...overrides };
}

function evidenceConflictDetail(overrides: { version?: number; next_event_cursor?: string | null; events?: EvidenceConflict["events"] } = {}): EvidenceConflictDetail {
  const conflict = evidenceConflict({ version: overrides.version ?? 1, events: overrides.events ?? [{
    event_id: "event-1", conflict_id: "conflict-1", ordinal: 2, action: "opened", status_after: "open", case_version: 1,
    actor_kind: "profile", citation_snapshot: [], created_at: "2026-08-01T00:00:00Z",
  }] });
  return { conflict, next_event_cursor: overrides.next_event_cursor ?? null };
}
