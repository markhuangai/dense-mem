import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SecretBox } from "./components";

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
