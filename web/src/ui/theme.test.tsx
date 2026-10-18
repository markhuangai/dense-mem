import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ThemeControl, readThemePreference, usePortalTheme } from "./theme";
import { ConfirmationProvider, useConfirmation } from "./confirmation";
import { useState } from "react";

function Appearance() {
  const { preference, theme, setPreference } = usePortalTheme("test.theme");
  return <><output aria-label="Resolved theme">{theme}</output><ThemeControl preference={preference} onChange={setPreference} /></>;
}
function ConfirmAction() {
  const confirm = useConfirmation();
  const [result, setResult] = useState("waiting");
  return <><button onClick={async () => setResult(await confirm({ title: "Revoke credential?", description: "Its API key will stop working.", confirmLabel: "Revoke", destructive: true }) ? "revoked" : "retained")}>Review revoke</button><output>{result}</output></>;
}

beforeEach(() => { localStorage.clear(); document.documentElement.classList.remove("dark"); });

describe("portal appearance and confirmation", () => {
  it("follows system changes until an explicit choice and preserves stored light/dark", async () => {
    const media = new EventTarget();
    let matches = true;
    Object.defineProperty(media, "matches", { get: () => matches });
    vi.stubGlobal("matchMedia", () => media);
    render(<Appearance />);
    expect(readThemePreference("test.theme")).toBe("system");
    expect(document.documentElement).toHaveClass("dark");
    matches = false;
    act(() => media.dispatchEvent(new Event("change")));
    expect(screen.getByLabelText("Resolved theme")).toHaveTextContent("light");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Choose theme" }));
    await user.click(screen.getByRole("menuitem", { name: "Dark" }));
    expect(localStorage.getItem("test.theme")).toBe("dark");
    expect(document.documentElement.style.colorScheme).toBe("dark");
    localStorage.setItem("old.theme", "light"); expect(readThemePreference("old.theme")).toBe("light");
    localStorage.setItem("old.theme", "dark"); expect(readThemePreference("old.theme")).toBe("dark");
    localStorage.setItem("old.theme", "invalid"); expect(readThemePreference("old.theme")).toBe("system");
    vi.unstubAllGlobals();
  });

  it("focuses the safe action and requires explicit confirmation", async () => {
    const user = userEvent.setup();
    render(<ConfirmationProvider><ConfirmAction /></ConfirmationProvider>);
    const trigger = screen.getByRole("button", { name: "Review revoke" });
    await user.click(trigger);
    expect(screen.getByRole("alertdialog")).toHaveAccessibleName("Revoke credential?");
    expect(screen.getByRole("button", { name: "Cancel" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.getByText("retained")).toBeInTheDocument();
    await user.click(trigger);
    await user.click(screen.getByRole("button", { name: "Revoke" }));
    expect(screen.getByText("revoked")).toBeInTheDocument();
  });
});
