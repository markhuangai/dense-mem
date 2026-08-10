import { act, fireEvent, render } from "@testing-library/react";
import { useVisiblePolling } from "./useVisiblePolling";
import { afterEach, describe, expect, it, vi } from "vitest";

function Probe({ loader, dependency = "one" }: { loader: (signal: AbortSignal) => Promise<void>; dependency?: string }) {
  const refresh = useVisiblePolling(loader, [dependency], 100);
  return <button type="button" onClick={() => void refresh()}>Refresh</button>;
}

describe("useVisiblePolling", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("polls visible tabs, avoids overlap, and aborts on hide", async () => {
    vi.useFakeTimers();
    let resolvePending: (() => void) | undefined;
    const loader = vi.fn((signal: AbortSignal) => new Promise<void>((resolve) => {
      signal.addEventListener("abort", () => resolve(), { once: true });
      resolvePending = resolve;
    }));
    render(<Probe loader={loader} />);

    expect(loader).toHaveBeenCalledTimes(1);
    await act(async () => {
      vi.advanceTimersByTime(100);
    });
    expect(loader).toHaveBeenCalledTimes(1);

    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(loader.mock.calls[0][0].aborted).toBe(true);

    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(loader).toHaveBeenCalledTimes(2);
    resolvePending?.();
  });

  it("lets a manual refresh converge after the current request settles", async () => {
    vi.useFakeTimers();
    const loader = vi.fn(async () => undefined);
    const { getByRole } = render(<Probe loader={loader} />);
    await act(async () => {
      await Promise.resolve();
    });
    expect(loader).toHaveBeenCalledTimes(1);
    await act(async () => {
      fireEvent.click(getByRole("button", { name: "Refresh" }));
      await Promise.resolve();
    });
    expect(loader).toHaveBeenCalledTimes(2);

    await act(async () => {
      vi.advanceTimersByTime(100);
      await Promise.resolve();
    });
    expect(loader).toHaveBeenCalledTimes(3);
  });

  it("aborts the old request and starts a replacement when dependencies change", async () => {
    vi.useFakeTimers();
    const signals: AbortSignal[] = [];
    const loader = vi.fn((signal: AbortSignal) => {
      signals.push(signal);
      return new Promise<void>((resolve) => {
        signal.addEventListener("abort", () => resolve(), { once: true });
      });
    });
    const { rerender } = render(<Probe loader={loader} dependency="one" />);

    expect(loader).toHaveBeenCalledTimes(1);
    await act(async () => {
      rerender(<Probe loader={loader} dependency="two" />);
      await Promise.resolve();
    });

    expect(signals[0].aborted).toBe(true);
    expect(loader).toHaveBeenCalledTimes(2);
    expect(signals[1].aborted).toBe(false);
  });
});
