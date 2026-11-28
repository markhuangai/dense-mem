import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { TelemetrySeries } from "../telemetry/types";
import { CommandPalette, DenseMemMark, TelemetryChart } from "./motion";
import { PortalEnvironment, usePortalNavigation, useUnsavedChanges } from "./navigation";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "./primitives/dialog";
import { Tabs, TabsList, TabsTrigger } from "./primitives/tabs";

afterEach(() => vi.restoreAllMocks());

it("renders the decorative strata mark at the requested size", () => {
  const { container } = render(<DenseMemMark size={18} />);
  expect(container.querySelector("svg")).toHaveAttribute("width", "18");
  expect(container.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  expect(container.querySelector("path")).toHaveAttribute("d", expect.stringContaining("M4 20"));
});

it("moves the laser when Radix changes its active tab", async () => {
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.getAttribute("role") === "tablist") return new DOMRect(10, 20, 220, 36);
    return this.textContent === "Settings" ? new DOMRect(86, 20, 92, 32) : new DOMRect(14, 20, 68, 32);
  });
  const { container } = render(<Tabs defaultValue="overview"><TabsList aria-label="Test sections">
    <TabsTrigger value="overview">Overview</TabsTrigger>
    <TabsTrigger value="settings">Settings</TabsTrigger>
  </TabsList></Tabs>);
  const marker = container.querySelector<HTMLElement>(".laser-indicator");
  expect(marker?.style.transform).toBe("translate3d(4px, 0px, 0px) scaleX(68)");
  expect(marker).not.toHaveAttribute("data-ready");
  fireEvent.mouseDown(screen.getByRole("tab", { name: "Settings" }), { button: 0 });
  await waitFor(() => expect(marker?.style.transform).toBe("translate3d(76px, 0px, 0px) scaleX(92)"));
  await waitFor(() => expect(marker).toHaveAttribute("data-ready", "true"));
});

function PortalCommands({ dirty = false }: { dirty?: boolean }) {
  const { navigate, parameters } = usePortalNavigation();
  useUnsavedChanges(dirty);
  return <>
    <CommandPalette items={[
      { id: "teams", label: "All teams", group: "Workspace", onSelect: () => navigate({ section: "all-teams" }) },
      { id: "logs", label: "Logs", group: "Operations", onSelect: () => navigate({ section: "logs" }) },
    ]} />
    <div id="portal-content" tabIndex={-1}>{parameters.get("section") ?? "Overview"}</div>
  </>;
}

describe("Command palette", () => {
  it("keeps focus in the combobox, scrolls options, and navigates on Enter", async () => {
    const scrollIntoView = vi.fn();
    const descriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollIntoView");
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scrollIntoView });
    try {
      render(<PortalEnvironment><PortalCommands /></PortalEnvironment>);
      fireEvent.keyDown(window, { key: "k", metaKey: true });
      const input = await screen.findByRole("combobox");
      const list = screen.getByRole("listbox", { name: "Commands" });
      const [teams, logs] = within(list).getAllByRole("option");
      expect(input).toHaveFocus();
      expect(input).toHaveAttribute("aria-expanded", "true");
      expect(input).toHaveAttribute("aria-controls", list.id);
      expect(input).toHaveAttribute("aria-activedescendant", teams.id);
      expect(logs.tagName).toBe("DIV");
      expect(logs).toHaveAttribute("tabindex", "-1");
      fireEvent.keyDown(input, { key: "ArrowUp" });
      expect(input).toHaveAttribute("aria-activedescendant", logs.id);
      expect(scrollIntoView).toHaveBeenLastCalledWith({ block: "nearest" });
      fireEvent.keyDown(input, { key: "ArrowDown" });
      expect(input).toHaveAttribute("aria-activedescendant", teams.id);
      fireEvent.change(input, { target: { value: "log" } });
      expect(within(list).getAllByRole("option")).toHaveLength(1);
      fireEvent.keyDown(input, { key: "Enter" });
      const content = await screen.findByText("logs", { selector: "#portal-content" });
      expect(window.location.search).toBe("?section=logs");
      await waitFor(() => expect(content).toHaveFocus());
    } finally {
      if (descriptor) Object.defineProperty(HTMLElement.prototype, "scrollIntoView", descriptor);
      else Reflect.deleteProperty(HTMLElement.prototype, "scrollIntoView");
    }
  });

  it("returns focus to the trigger on Escape and ignores a shortcut over another dialog", async () => {
    const { rerender } = render(<PortalEnvironment><PortalCommands /></PortalEnvironment>);
    const trigger = screen.getByRole("button", { name: "Command" });
    fireEvent.click(trigger);
    const input = await screen.findByRole("combobox");
    fireEvent.change(input, { target: { value: "nothing matches" } });
    expect(screen.getByRole("status")).toHaveTextContent("No matches.");
    expect(input).not.toHaveAttribute("aria-activedescendant");
    fireEvent.keyDown(input, { key: "Escape" });
    await waitFor(() => expect(trigger).toHaveFocus());

    rerender(<PortalEnvironment><PortalCommands /><Dialog open><DialogContent>
      <DialogTitle>Other dialog</DialogTitle><DialogDescription>Already open</DialogDescription>
    </DialogContent></Dialog></PortalEnvironment>);
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  });

  it("hands focus to the unsaved changes confirmation and honors both decisions", async () => {
    render(<PortalEnvironment><PortalCommands dirty /></PortalEnvironment>);
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    await screen.findByRole("combobox");
    fireEvent.click(screen.getByRole("option", { name: "Logs, Operations" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Discard unsaved changes?" });
    const cancel = within(dialog).getByRole("button", { name: "Cancel" });
    expect(cancel).toHaveFocus();
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    fireEvent.click(cancel);
    expect(window.location.search).toBe("");
    expect(await screen.findByText("Overview", { selector: "#portal-content" })).toBeVisible();
    await waitFor(() => expect(dialog).not.toBeInTheDocument());

    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    fireEvent.click(await screen.findByRole("option", { name: "Logs, Operations" }));
    const reopened = await screen.findByRole("alertdialog", { name: "Discard unsaved changes?" });
    fireEvent.click(within(reopened).getByRole("button", { name: "Discard changes" }));
    const content = await screen.findByText("logs", { selector: "#portal-content" });
    expect(window.location.search).toBe("?section=logs");
    await waitFor(() => expect(content).toHaveFocus());
  });
});

const series: TelemetrySeries = {
  id: "http_rps", label: "HTTP requests", status: "ready", unit: "rps",
  points: [
    { timestamp: "2026-11-27T20:00:00Z", value: 1.25 },
    { timestamp: "2026-11-27T20:01:00Z", value: Number.NaN },
    { timestamp: "2026-11-27T20:02:00Z", value: 3.5 },
  ],
};

describe("Telemetry chart", () => {
  it("scrubs on keyboard and pointer, announcing only keyboard samples", () => {
    const { container } = render(<TelemetryChart series={series} />);
    const plot = screen.getByRole("group", { name: "HTTP requests samples" });
    const live = container.querySelector('[aria-live="polite"]');
    expect(live).toBeEmptyDOMElement();
    act(() => plot.focus());
    expect(live).toHaveTextContent("3.5 rps");
    fireEvent.keyDown(plot, { key: "ArrowLeft" });
    expect(live).toHaveTextContent("Sample unavailable");
    expect(plot.querySelector("path")).not.toHaveAttribute("d", expect.stringContaining("NaN"));
    fireEvent.keyDown(plot, { key: "Home" });
    expect(live).toHaveTextContent("1.25 rps");
    fireEvent.keyDown(plot, { key: "End" });
    fireEvent.keyDown(plot, { key: "ArrowRight" });
    expect(live).toHaveTextContent("3.5 rps");

    const svg = plot.querySelector("svg")!;
    vi.spyOn(svg, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 0, 640, 144));
    fireEvent(svg, new MouseEvent("pointermove", { bubbles: true, clientX: 8 }));
    expect(container.querySelector(".telemetry-readout")).toHaveTextContent("1.25 rps");
    expect(live).toHaveTextContent("3.5 rps");
  });

  it("renders an available single sample and explains degraded or empty data", () => {
    const { rerender } = render(<TelemetryChart series={{ ...series, status: "inactive", reason: "No activity", points: [series.points[0]] }} />);
    expect(screen.getByRole("status")).toHaveTextContent("Telemetry series inactive · No activity");
    expect(screen.getByRole("group", { name: "HTTP requests samples" }).querySelector(".telemetry-reveal circle")).toBeInTheDocument();
    rerender(<TelemetryChart series={{ ...series, points: [] }} />);
    expect(screen.getByRole("status")).toHaveTextContent("No samples recorded in this window.");
    expect(screen.queryByRole("group", { name: "HTTP requests samples" })).not.toBeInTheDocument();
  });
});
