import { Command } from "lucide-react";
import { type KeyboardEvent, type PointerEvent, useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { TelemetrySeries } from "../telemetry/types";
import { Button } from "./primitives/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle, DialogTrigger } from "./primitives/dialog";
import { Input } from "./primitives/input";

export function DenseMemMark({ size = 32 }: { size?: number }) {
  return <svg width={size} height={size} viewBox="0 0 32 32" fill="none" stroke="currentColor" strokeWidth={1.8} strokeLinejoin="round" aria-hidden="true">
    <path d="M4 10 16 4l12 6-12 6zM4 15l12 6 12-6M4 20l12 6 12-6" />
  </svg>;
}

export function useLaserIndicator<Container extends HTMLElement>(activeSelector: string, axis?: "horizontal" | "vertical") {
  const containerRef = useRef<Container>(null);
  const indicatorRef = useRef<HTMLSpanElement>(null);

  useLayoutEffect(() => {
    const container = containerRef.current;
    const indicator = indicatorRef.current;
    if (!container || !indicator) return;
    let frame = 0;
    let observed: Element | null = null;

    function measure() {
      if (!container || !indicator) return;
      const active = container.querySelector(activeSelector);
      if (active !== observed) {
        if (observed) resize?.unobserve(observed);
        observed = active;
        if (active) resize?.observe(active);
      }
      const box = active?.getBoundingClientRect();
      if (!box?.width || !box.height) {
        indicator.hidden = true;
        delete indicator.dataset.ready;
        cancelAnimationFrame(frame);
        return;
      }
      const bounds = container.getBoundingClientRect();
      const vertical = (axis ?? container.dataset.orientation) === "vertical";
      indicator.classList.toggle("laser-vertical", vertical);
      indicator.classList.toggle("laser-horizontal", !vertical);
      const offset = vertical
        ? box.top - bounds.top + container.scrollTop - container.clientTop
        : box.left - bounds.left + container.scrollLeft - container.clientLeft;
      indicator.style.transform = vertical
        ? `translate3d(0, ${offset}px, 0) scaleY(${box.height})`
        : `translate3d(${offset}px, 0, 0) scaleX(${box.width})`;
      if (indicator.hidden) {
        indicator.hidden = false;
        frame = requestAnimationFrame(() => {
          frame = requestAnimationFrame(() => { indicator.dataset.ready = "true"; });
        });
      }
    }

    const resize = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    resize?.observe(container);
    const mutations = new MutationObserver((records) => {
      if (records.some(({ target }) => target !== indicator)) measure();
    });
    mutations.observe(container, { attributes: true, attributeFilter: ["data-state", "aria-current", "aria-pressed", "class", "data-orientation"], childList: true, subtree: true });
    measure();
    return () => {
      cancelAnimationFrame(frame);
      resize?.disconnect();
      mutations.disconnect();
      indicator.hidden = true;
      delete indicator.dataset.ready;
    };
  }, [activeSelector, axis]);

  return { containerRef, indicatorRef };
}

export type CommandItem = {
  id: string;
  label: string;
  group?: string;
  onSelect: () => void | Promise<unknown>;
};

function activeDialog() {
  return document.querySelector('[role="dialog"]:not([data-state="closed"]):not([hidden]), [role="alertdialog"]:not([data-state="closed"]):not([hidden])');
}

export function CommandPalette({ items }: { items: CommandItem[] }) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const selection = useRef<CommandItem | null>(null);
  const listId = useId();
  const term = query.trim().toLocaleLowerCase();
  const matches = items.filter(({ label, group }) => `${label} ${group ?? ""}`.toLocaleLowerCase().includes(term));
  const index = Math.min(cursor, matches.length - 1);
  const selected = matches[index];
  const optionId = (optionIndex: number) => `${listId}-option-${optionIndex}`;
  const activeOptionId = selected ? optionId(index) : undefined;

  const changeOpen = useCallback((next: boolean) => {
    if (next && (selection.current || activeDialog())) return;
    if (next) {
      setQuery("");
      setCursor(0);
    }
    setOpen(next);
  }, []);

  useEffect(() => {
    function shortcut(event: globalThis.KeyboardEvent) {
      if (event.defaultPrevented || event.repeat || (!event.metaKey && !event.ctrlKey) || event.altKey || event.shiftKey || event.key.toLowerCase() !== "k") return;
      if (activeDialog() || selection.current) return;
      event.preventDefault();
      changeOpen(true);
    }
    window.addEventListener("keydown", shortcut);
    return () => window.removeEventListener("keydown", shortcut);
  }, [changeOpen]);

  useEffect(() => {
    if (open && activeOptionId) document.getElementById(activeOptionId)?.scrollIntoView?.({ block: "nearest" });
  }, [activeOptionId, open]);

  function choose(item: CommandItem) {
    selection.current = item;
    changeOpen(false);
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.nativeEvent.isComposing || !matches.length) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const step = event.key === "ArrowDown" ? 1 : -1;
      setCursor((index + step + matches.length) % matches.length);
    } else if (event.key === "Enter" && selected) {
      event.preventDefault();
      choose(selected);
    }
  }

  return <Dialog open={open} onOpenChange={changeOpen}>
    <DialogTrigger asChild>
      <Button variant="outline" size="sm" className="max-md:size-8 max-md:has-[>svg]:px-0" type="button" aria-label="Command" aria-keyshortcuts="Meta+K Control+K">
        <Command aria-hidden="true" /><span className="command-trigger-label">Command</span><kbd className="command-shortcut" aria-hidden="true">⌘K</kbd>
      </Button>
    </DialogTrigger>
    <DialogContent showCloseButton={false} className="command-palette gap-3 p-4 sm:max-w-md" onCloseAutoFocus={(event) => {
      const item = selection.current;
      if (!item) return;
      event.preventDefault();
      selection.current = null;
      void Promise.resolve(item.onSelect()).then(() => {
        requestAnimationFrame(() => {
          if (!activeDialog()) document.getElementById("portal-content")?.focus({ preventScroll: true });
        });
      });
    }}>
      <DialogTitle>Command palette</DialogTitle>
      <DialogDescription className="sr-only">Search sections and teams. Use the arrow keys to select a result and Enter to open it.</DialogDescription>
      <Input role="combobox" aria-label="Find a section or team" aria-expanded="true" aria-controls={listId} aria-activedescendant={activeOptionId} aria-autocomplete="list"
        placeholder="Find a section or team…" value={query} onKeyDown={onKeyDown} onChange={(event) => { setQuery(event.target.value); setCursor(0); }} />
      <div id={listId} role="listbox" aria-label="Commands" className="command-palette-options">
        {matches.map((item, optionIndex) => <div key={item.id} id={optionId(optionIndex)} role="option" tabIndex={-1}
          aria-selected={optionIndex === index} aria-label={item.group ? `${item.label}, ${item.group}` : item.label}
          className="command-palette-option" onPointerMove={() => setCursor(optionIndex)}
          onPointerDown={(event) => event.preventDefault()} onClick={() => choose(item)}>
          <span>{item.label}</span>{item.group && <small>{item.group}</small>}
        </div>)}
        {!matches.length && <div className="command-palette-empty" role="option" aria-selected="false" aria-disabled="true" tabIndex={-1}>No matches.</div>}
      </div>
      {!matches.length && <p className="sr-only" role="status">No matches.</p>}
    </DialogContent>
  </Dialog>;
}

const chartWidth = 640;
const chartHeight = 144;
const inset = 8;
const valueFormat = new Intl.NumberFormat(undefined, { maximumFractionDigits: 3 });
const timeFormat = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
const seriesStatus = {
  inactive: "Telemetry series inactive",
  unavailable: "Telemetry series unavailable",
  unsupported: "Telemetry series unsupported",
} as const;

function formatSampleTime(timestamp: number) {
  return Number.isFinite(timestamp) ? timeFormat.format(timestamp) : "Time unavailable";
}

export function TelemetryChart({ series }: { series: TelemetrySeries }) {
  const [cursor, setCursor] = useState<number | null>(null);
  const [announcement, setAnnouncement] = useState("");
  const instructionsId = useId();
  const values = series.points.map(({ value }) => value);
  const timestamps = series.points.map(({ timestamp }) => Date.parse(timestamp));
  const latest = values.length - 1;
  const activeIndex = Math.min(cursor ?? latest, latest);
  const activeValue = values[activeIndex];
  const finiteValues = values.filter(Number.isFinite);
  const singleton = finiteValues.length === 1 ? values.findIndex(Number.isFinite) : -1;
  const lower = Math.min(0, ...finiteValues);
  const upper = Math.max(0, ...finiteValues);
  const elapsed = timestamps[latest] - timestamps[0];
  const xPositions = timestamps.map((timestamp, index) => {
    const position = elapsed > 0 && Number.isFinite(timestamp) ? (timestamp - timestamps[0]) / elapsed : index / Math.max(latest, 1);
    return inset + (chartWidth - 2 * inset) * position;
  });
  const yPositions = values.map((value) => chartHeight - inset - (chartHeight - 2 * inset) * (value - lower) / (upper - lower || 1));
  const line = values.map((value, index) => Number.isFinite(value)
    ? `${Number.isFinite(values[index - 1]) ? "L" : "M"} ${xPositions[index]} ${yPositions[index]}`
    : "").join(" ");
  const statusLabel = series.status === "ready" ? null : series.status ? seriesStatus[series.status] : "Telemetry series status unknown";

  function readout(index: number) {
    const value = values[index];
    const display = Number.isFinite(value) ? `${valueFormat.format(value)} ${series.unit}` : "Sample unavailable";
    return `${formatSampleTime(timestamps[index])} · ${display}`;
  }

  function announce(index: number) {
    setCursor(index);
    setAnnouncement(readout(index));
  }

  function onChartKey(event: KeyboardEvent<HTMLDivElement>) {
    const next = { ArrowLeft: activeIndex - 1, ArrowRight: activeIndex + 1, Home: 0, End: latest }[event.key];
    if (next === undefined) return;
    event.preventDefault();
    announce(Math.max(0, Math.min(latest, next)));
  }

  function scrub(event: PointerEvent<SVGSVGElement>) {
    const bounds = event.currentTarget.getBoundingClientRect();
    if (!bounds.width) return;
    const position = (event.clientX - bounds.left) / bounds.width * chartWidth;
    const nearest = xPositions.reduce((best, sample, index) => Math.abs(sample - position) < Math.abs(xPositions[best] - position) ? index : best, 0);
    setCursor(nearest);
  }

  return <figure className="telemetry-chart">
    <figcaption><span>{series.label}</span>{finiteValues.length > 0 && <span className="telemetry-readout">{readout(activeIndex)}</span>}</figcaption>
    {statusLabel && <p className="banner warning" role="status">{statusLabel}{series.reason ? ` · ${series.reason}` : ""}</p>}
    {!finiteValues.length ? <p className="form-meta" role="status">No samples recorded in this window.</p> : <>
      <p className="sr-only" id={instructionsId}>Use Left and Right to step through samples, and Home and End to jump to the first or last sample.</p>
      <div className="telemetry-plot" role="group" aria-label={`${series.label} samples`} aria-describedby={instructionsId}
        tabIndex={0} onFocus={(event) => { if (event.currentTarget.matches(":focus-visible")) announce(activeIndex); }} onKeyDown={onChartKey}>
        <svg viewBox={`0 0 ${chartWidth} ${chartHeight}`} preserveAspectRatio="none" aria-hidden="true" onPointerDown={scrub} onPointerMove={scrub}>
          {[inset, chartHeight / 2, chartHeight - inset].map((row) => <line key={row} className="telemetry-gridline" x1={0} x2={chartWidth} y1={row} y2={row} vectorEffect="non-scaling-stroke" />)}
          <g className="telemetry-reveal">
            <path d={line} fill="none" stroke="currentColor" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
            {singleton >= 0 && <circle cx={xPositions[singleton]} cy={yPositions[singleton]} r={2.5} fill="currentColor" />}
          </g>
          <line className="telemetry-crosshair" x1={xPositions[activeIndex]} x2={xPositions[activeIndex]} y1={0} y2={chartHeight} vectorEffect="non-scaling-stroke" />
          {Number.isFinite(activeValue) && <circle cx={xPositions[activeIndex]} cy={yPositions[activeIndex]} r={2.5} fill="currentColor" />}
        </svg>
      </div>
      <div className="telemetry-axes"><span>{formatSampleTime(timestamps[0])}</span>{latest > 0 && <span>{formatSampleTime(timestamps[latest])}</span>}</div>
      <span className="sr-only" aria-live="polite">{announcement}</span>
    </>}
  </figure>;
}
