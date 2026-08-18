import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ControlApi, PrivateMemoryOperation, PrivateMemorySpace } from "../api";
import { PrivateMemoryPanel } from "./PrivateMemoryPanel";

const space: PrivateMemorySpace = {
  id: "11111111-1111-4111-8111-111111111111",
  team_id: "22222222-2222-4222-8222-222222222222",
  kind: "credential_private",
  owner_credential_id: "33333333-3333-4333-8333-333333333333",
  generation: 1,
  lifecycle_state: "active",
  private_content_at: "2026-07-01T12:00:00Z",
  created_at: "2026-06-01T12:00:00Z",
  updated_at: "2026-07-01T12:00:00Z",
};

const operation: PrivateMemoryOperation = {
  operation_id: "44444444-4444-4444-8444-444444444444",
  team_id: space.team_id,
  action: "erase_credential_private",
  actor_class: "control",
  reason_code: "control_request",
  retire_space: false,
  status: "queued",
  deleted_counts: {},
  requested_at: "2026-08-18T12:00:00Z",
  updated_at: "2026-08-18T12:00:00Z",
};

beforeEach(() => {
  vi.restoreAllMocks();
  vi.spyOn(window, "confirm").mockReturnValue(true);
  vi.spyOn(window, "prompt").mockReturnValue("case_hold");
});

describe("PrivateMemoryPanel", () => {
  it("loads the governance ledger and invokes held destructive actions explicitly", async () => {
    const api = privateMemoryApi();
    render(<PrivateMemoryPanel api={api} />);

    expect(await screen.findByText("credential_private")).toBeInTheDocument();
    expect(screen.getByLabelText("Private memory status")).toHaveTextContent("30 days");

    await userEvent.click(screen.getByRole("button", { name: `Place legal hold for ${space.id}` }));
    await waitFor(() => expect(api.placePrivateMemoryLegalHold).toHaveBeenCalledWith(space.id, "case_hold"));

    await userEvent.click(screen.getByRole("button", { name: `Erase private memory for ${space.id}` }));
    await waitFor(() => expect(api.requestPrivateMemoryErasure).toHaveBeenCalledWith(
      space.id,
      expect.stringMatching(/^control-erasure:/),
    ));

    await userEvent.click(screen.getByRole("button", { name: "Run retention" }));
    await waitFor(() => expect(api.runPrivateMemoryRetention).toHaveBeenCalledWith(
      expect.stringMatching(/^retention:/),
    ));
  });

  it("blocks erasure while held and exposes the release action", async () => {
    const held = {
      ...space,
      active_hold: {
        id: "55555555-5555-4555-8555-555555555555",
        team_id: space.team_id,
        space_id: space.id,
        reason_code: "case_hold",
        actor_class: "control",
        placed_at: "2026-08-18T12:00:00Z",
      },
    };
    const api = privateMemoryApi([held]);
    render(<PrivateMemoryPanel api={api} />);

    const erase = await screen.findByRole("button", { name: `Erase private memory for ${space.id}` });
    expect(erase).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: `Release legal hold for ${space.id}` }));
    await waitFor(() => expect(api.releasePrivateMemoryLegalHold).toHaveBeenCalledWith(space.id));
    expect(api.requestPrivateMemoryErasure).not.toHaveBeenCalled();
  });

  it("reuses the erasure idempotency key until the API accepts the intent", async () => {
    const api = privateMemoryApi();
    vi.mocked(api.requestPrivateMemoryErasure)
      .mockRejectedValueOnce(new Error("connection lost"))
      .mockResolvedValueOnce(operation);
    render(<PrivateMemoryPanel api={api} />);

    const erase = await screen.findByRole("button", { name: `Erase private memory for ${space.id}` });
    await userEvent.click(erase);
    expect(await screen.findByRole("alert")).toHaveTextContent("connection lost");
    await userEvent.click(erase);

    await waitFor(() => expect(api.requestPrivateMemoryErasure).toHaveBeenCalledTimes(2));
    const firstKey = vi.mocked(api.requestPrivateMemoryErasure).mock.calls[0][1];
    const secondKey = vi.mocked(api.requestPrivateMemoryErasure).mock.calls[1][1];
    expect(firstKey).toBe(secondKey);
  });

  it("clears a success message when the refresh after a mutation fails", async () => {
    const api = privateMemoryApi();
    let spaceLoads = 0;
    vi.mocked(api.listPrivateMemorySpaces).mockImplementation(async () => {
      spaceLoads += 1;
      if (spaceLoads > 1) {
        throw new Error("reload failed");
      }
      return { data: [space], pagination: { limit: 100, offset: 0 } };
    });
    render(<PrivateMemoryPanel api={api} />);

    await userEvent.click(await screen.findByRole("button", { name: `Place legal hold for ${space.id}` }));
    expect(await screen.findByRole("alert")).toHaveTextContent("reload failed");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});

function privateMemoryApi(spaces: PrivateMemorySpace[] = [space]) {
  return {
    getPrivateMemoryConfig: vi.fn(async () => ({
      update_time: "2026-08-18T12:00:00Z",
      items: [{
        key: "PRIVATE_MEMORY_RETENTION_DAYS",
        value: "30",
        effective_value: "30",
        updated_at: "2026-08-18T12:00:00Z",
      }],
      effective: { retention_days: 30 },
    })),
    listPrivateMemorySpaces: vi.fn(async () => ({ data: spaces, pagination: { limit: 100, offset: 0 } })),
    listPrivateMemoryErasures: vi.fn(async () => ({ data: [], pagination: { limit: 100, offset: 0 } })),
    listPrivateMemoryRetentionRuns: vi.fn(async () => ({ data: [], pagination: { limit: 100, offset: 0 } })),
    placePrivateMemoryLegalHold: vi.fn(async () => spaces[0].active_hold),
    releasePrivateMemoryLegalHold: vi.fn(async () => ({ released: true, hold: spaces[0].active_hold ?? null })),
    requestPrivateMemoryErasure: vi.fn(async () => operation),
    runPrivateMemoryRetention: vi.fn(async () => ({
      id: "66666666-6666-4666-8666-666666666666",
      actor_class: "control",
      cutoff: "2026-07-19T12:00:00Z",
      retention_days: 30,
      queued_count: 1,
      status: "completed",
      started_at: "2026-08-18T12:00:00Z",
      completed_at: "2026-08-18T12:00:00Z",
    })),
  } as unknown as ControlApi;
}
