import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LoadingState, SecretBox, UsageMeter } from "./components";

afterEach(() => {
  vi.useRealTimers();
});

describe("SecretBox", () => {
  it("resets copied feedback after a short delay", async () => {
    vi.useFakeTimers();
    vi.mocked(navigator.clipboard.writeText).mockResolvedValue(undefined);

    render(
      <SecretBox
        value=" dm_secret "
        valueLabel="Generated secret"
        copyLabel="Copy secret"
        dismissLabel="Dismiss secret"
        onDismiss={() => {}}
      />,
    );

    const copyButton = screen.getByRole("button", { name: "Copy secret" });
    await act(async () => {
      fireEvent.click(copyButton);
    });

    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("dm_secret");
    expect(copyButton.querySelector(".lucide-check")).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(2000);
    });

    expect(copyButton.querySelector(".lucide-copy")).toBeInTheDocument();
  });
});

describe("LoadingState", () => {
  it("announces the loading label and renders animation parts", () => {
    render(<LoadingState label="Loading telemetry" compact />);

    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("Loading telemetry");
    expect(status).toHaveClass("loading-state");
    expect(status).toHaveClass("compact");
    expect(status.querySelector(".loading-orbit")).toBeInTheDocument();
    expect(status.querySelectorAll(".loading-skeleton span")).toHaveLength(3);
  });
});

describe("UsageMeter", () => {
  it("preserves exact overrun counts while bounding the visual progress", () => {
    render(<UsageMeter label="Charged input" value={1200} limit={1000} detail="900 reported · 300 reserved" />);
    const meter = screen.getByRole("progressbar", { name: "Charged input" });
    expect(meter).toHaveAttribute("aria-valuenow", "100");
    expect(meter).toHaveAttribute("aria-valuetext", "1,200 of 1,000");
    expect(screen.getByText("900 reported · 300 reserved")).toBeVisible();
  });

  it("shows zero counts without inventing a coverage percentage", () => {
    render(<UsageMeter label="Organized / eligible" value={0} limit={0} detail="No eligible records discovered" />);
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    expect(screen.getByText("No eligible records discovered")).toBeVisible();
    expect(screen.getByText("/ 0").parentElement).toHaveTextContent("0 / 0");
  });
});
