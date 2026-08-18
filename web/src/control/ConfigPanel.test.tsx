import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ControlApi, PrivateMemoryConfigInput } from "../api";
import { ConfigPanel } from "./ConfigPanel";

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("ConfigPanel private-memory retention", () => {
  it("shows the disabled default and saves a retention period", async () => {
    const privateConfig = {
      update_time: "2026-08-18T12:00:00Z",
      items: [{
        key: "PRIVATE_MEMORY_RETENTION_DAYS",
        value: "0",
        effective_value: "0",
        updated_at: "2026-08-18T12:00:00Z",
      }],
      effective: { retention_days: 0 },
    };
    const api = {
      getGeneralConfig: vi.fn(async () => ({ update_time: "", items: [], effective: { timezone: "UTC" } })),
      getPrivateMemoryConfig: vi.fn(async () => privateConfig),
      updatePrivateMemoryConfig: vi.fn(async (input: PrivateMemoryConfigInput) => ({
        ...privateConfig,
        items: privateConfig.items.map((item) => ({ ...item, value: input.items[0].value, effective_value: input.items[0].value })),
        effective: { retention_days: Number(input.items[0].value) },
      })),
    } as unknown as ControlApi;

    render(<ConfigPanel api={api} />);
    await userEvent.click(screen.getByRole("tab", { name: "Privacy" }));

    expect(await screen.findByText("Automatic erasure is disabled. Owner requests and legal holds still apply.")).toBeInTheDocument();
    const field = screen.getByLabelText("Private-memory retention days");
    await userEvent.clear(field);
    await userEvent.type(field, "30");
    await userEvent.click(screen.getByRole("button", { name: "Save config" }));

    await waitFor(() => expect(api.updatePrivateMemoryConfig).toHaveBeenCalledWith({
      items: [{ key: "PRIVATE_MEMORY_RETENTION_DAYS", value: "30" }],
    }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });
});
