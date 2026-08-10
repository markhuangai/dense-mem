import { ReactNode } from "react";
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { RefreshCw } from "lucide-react";
import { TelemetrySnapshot, TelemetryWindowKey, telemetryWindowOptions } from "./types";
import { LoadingState, SectionHeading, SummaryCard } from "../ui/components";
import "./telemetry.css";

type TelemetryDashboardProps = {
  title: string;
  snapshot: TelemetrySnapshot | null;
  windowKey: TelemetryWindowKey;
  loading: boolean;
  error: string;
  controls?: ReactNode;
  onWindowChange: (window: TelemetryWindowKey) => void;
  onRefresh: () => void;
};

export function TelemetryDashboard({
  title,
  snapshot,
  windowKey,
  loading,
  error,
  controls,
  onWindowChange,
  onRefresh,
}: TelemetryDashboardProps) {
  const windowedCards = snapshot ? telemetryWindowedCards(snapshot) : [];
  const currentCards = snapshot ? telemetryCurrentCards(snapshot) : [];
  const activitySeries = snapshot ? telemetryActivitySeries(snapshot) : [];
  const stateSeries = snapshot ? telemetryStateSeries(snapshot) : [];
  const nonReadyItems = [
    ...windowedCards,
    ...currentCards,
    ...activitySeries,
    ...stateSeries,
  ].filter((item) => itemStatus(item) !== "ready");
  const windowControlId = `${title.toLowerCase().replace(/[^a-z0-9_-]+/g, "-").replace(/^-|-$/g, "") || "telemetry"}-telemetry-window`;

  return (
    <div className="telemetry-dashboard">
      <SectionHeading
        title={title}
        subtitle={snapshot ? `${formatTelemetryTime(snapshot.window.from)} - ${formatTelemetryTime(snapshot.window.to)}${snapshot.generated_at ? ` · observed ${formatTelemetryTime(snapshot.generated_at)}` : ""}` : undefined}
        actions={(
          <button className="icon-button" type="button" aria-label="Refresh telemetry" onClick={onRefresh} disabled={loading}>
            <RefreshCw size={16} aria-hidden="true" />
          </button>
        )}
      />

      <div className="metrics-toolbar telemetry-toolbar">
        <label htmlFor={windowControlId}>Telemetry range</label>
        <select
          id={windowControlId}
          value={windowKey}
          onChange={(event) => onWindowChange(event.target.value as TelemetryWindowKey)}
        >
          {telemetryWindowOptions.map((option) => (
            <option key={option.value} value={option.value}>{option.label}</option>
          ))}
        </select>
        {controls}
      </div>

      {error && <div className="banner error" role="alert">{error}</div>}
      {snapshot?.status === "degraded" && <div className="banner warning" role="status">Some telemetry is unavailable. Successful items remain visible.</div>}
      {snapshot?.status === "unavailable" && <div className="banner error" role="alert">Telemetry is unavailable.</div>}
      {snapshot?.message && snapshot.status !== "degraded" && <div className="banner neutral">{snapshot.message}</div>}
      {loading && !snapshot && <LoadingState label="Loading telemetry" compact />}

      {snapshot && (
        <>
          <TelemetryCardSection
            title="Windowed activity"
            ariaLabel={`${title} totals`}
            cards={windowedCards}
          />
          <TelemetryCardSection
            title="Current knowledge state"
            ariaLabel={`${title} current state`}
            cards={currentCards}
          />
          <TelemetryChartSection
            title="Activity charts"
            ariaLabel={`${title} charts`}
            series={activitySeries}
            from={snapshot.window.from}
            to={snapshot.window.to}
          />
          <TelemetryChartSection
            title="State history"
            ariaLabel={`${title} state history`}
            series={stateSeries}
            from={snapshot.window.from}
            to={snapshot.window.to}
          />
          <TelemetryReasonLedger items={nonReadyItems} />
        </>
      )}
    </div>
  );
}

function TelemetryCardSection({ title, ariaLabel, cards }: {
  title: string;
  ariaLabel: string;
  cards: TelemetrySnapshot["cards"];
}) {
  const readyCards = cards.filter((card) => itemStatus(card) === "ready");
  if (readyCards.length === 0) {
    return null;
  }
  return (
    <section className="telemetry-section">
      <h3>{title}</h3>
      <div className="telemetry-card-grid" aria-label={ariaLabel}>
        {readyCards.map((card) => (
          <SummaryCard key={card.id} label={card.label} value={formatTelemetryCardValue(card)} />
        ))}
      </div>
    </section>
  );
}

function TelemetryChartSection({ title, ariaLabel, series, from, to }: {
  title: string;
  ariaLabel: string;
  series: TelemetrySnapshot["series"];
  from: string;
  to: string;
}) {
  const readySeries = series.filter((item) => itemStatus(item) === "ready");
  if (readySeries.length === 0) {
    return null;
  }
  return (
    <section className="telemetry-section">
      <h3>{title}</h3>
      <div className="telemetry-chart-grid" aria-label={ariaLabel}>
        {readySeries.map((item) => {
          const samplePoints = Array.isArray(item.points) ? item.points : [];
          const chartPoints = telemetryChartPoints(samplePoints, from, to);

          return (
            <div className="telemetry-chart" key={item.id}>
              <div className="telemetry-chart-head">
                <h3>{item.label}</h3>
                <span>{item.unit}</span>
              </div>
              <div className="telemetry-chart-body">
                <ResponsiveContainer
                  width="100%"
                  height="100%"
                  minWidth={280}
                  minHeight={180}
                  initialDimension={{ width: 640, height: 240 }}
                >
                  <LineChart data={chartPoints} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
                    <CartesianGrid stroke="var(--border)" strokeDasharray="3 3" />
                    <XAxis
                      dataKey="timestamp"
                      tickFormatter={formatTelemetryTick}
                      tick={{ fill: "var(--muted)", fontSize: 11 }}
                      minTickGap={28}
                    />
                    <YAxis
                      width={44}
                      tick={{ fill: "var(--muted)", fontSize: 11 }}
                      tickFormatter={(value) => formatTelemetryAxisTick(Number(value), item.unit)}
                      domain={telemetryYAxisDomain(samplePoints)}
                    />
                    <Tooltip
                      contentStyle={{
                        background: "var(--panel)",
                        border: "1px solid var(--border)",
                        borderRadius: 7,
                        color: "var(--text)",
                      }}
                      labelFormatter={(label) => formatTelemetryTime(String(label))}
                      formatter={(value) => [formatTelemetryValue(Number(value), item.unit), item.label]}
                    />
                    <Line
                      type="monotone"
                      dataKey="value"
                      stroke="var(--accent)"
                      strokeWidth={2}
                      dot={false}
                      isAnimationActive={false}
                    />
                  </LineChart>
                </ResponsiveContainer>
              </div>
            </div>
          );
        })}
      </div>
    </section>
  );
}

function TelemetryReasonLedger({ items }: { items: Array<TelemetrySnapshot["cards"][number] | TelemetrySnapshot["series"][number]> }) {
  if (items.length === 0) {
    return null;
  }
  const groups = new Map<string, typeof items>();
  for (const item of items) {
    const key = item.reason_code || itemStatus(item);
    const group = groups.get(key) ?? [];
    group.push(item);
    groups.set(key, group);
  }
  return (
    <details className="telemetry-reason-ledger">
      <summary>Inactive / unavailable ({items.length})</summary>
      <div className="telemetry-reason-groups">
        {[...groups.entries()].map(([reason, group]) => (
          <div className="telemetry-reason-group" key={reason}>
            <strong>{reason}</strong>
            <span>{group.map((item) => item.label).join(", ")}</span>
            {group[0].reason && <small>{group[0].reason}</small>}
          </div>
        ))}
      </div>
    </details>
  );
}

export function telemetryWindowedCards(snapshot: TelemetrySnapshot) {
  return Array.isArray(snapshot.windowed_cards) ? snapshot.windowed_cards : snapshot.cards;
}

export function telemetryCurrentCards(snapshot: TelemetrySnapshot) {
  return Array.isArray(snapshot.current_cards) ? snapshot.current_cards : [];
}

export function telemetryActivitySeries(snapshot: TelemetrySnapshot) {
  return Array.isArray(snapshot.activity_series) ? snapshot.activity_series : snapshot.series;
}

export function telemetryStateSeries(snapshot: TelemetrySnapshot) {
  return Array.isArray(snapshot.state_series) ? snapshot.state_series : [];
}

export function formatTelemetryCardValue(card: Pick<TelemetrySnapshot["cards"][number], "available" | "unit" | "value">) {
  if (card.available === false || !Number.isFinite(card.value)) {
    return "No data";
  }
  return formatTelemetryValue(card.value, card.unit);
}

function itemStatus(item: { status?: string; available?: boolean; points?: TelemetrySnapshot["series"][number]["points"] }) {
  if (item.status) {
    return item.status;
  }
  if (item.available === false) {
    return "unavailable";
  }
  if ("points" in item) {
    return (item.points?.length ?? 0) > 0 ? "ready" : "inactive";
  }
  return "ready";
}

export function formatTelemetryValue(value: number, unit: string) {
  if (!Number.isFinite(value)) {
    return "No data";
  }
  if (unit === "percent") {
    return `${value.toFixed(value >= 10 ? 0 : 1)}%`;
  }
  if (unit === "ms") {
    return `${value.toFixed(value >= 100 ? 0 : 1)} ms`;
  }
  if (unit === "USD") {
    return formatTelemetryUSD(value);
  }
  if (unit.includes("/s") || unit === "rps") {
    return value.toFixed(value >= 10 ? 1 : 2);
  }
  return compactNumber(value);
}

export function formatTelemetryAxisTick(value: number, unit: string) {
  if (!Number.isFinite(value)) {
    return "";
  }
  if (unit === "percent") {
    return `${trimFixed(value, value >= 10 ? 0 : 1)}%`;
  }
  if (unit === "USD") {
    return `$${compactNumber(value)}`;
  }
  if (unit.includes("/s") || unit === "rps") {
    return formatRateTick(value);
  }
  return compactNumber(value);
}

function formatTelemetryUSD(value: number) {
  const fractionDigits = value > 0 && value < 0.01 ? 6 : 2;
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: fractionDigits,
    maximumFractionDigits: fractionDigits,
  }).format(value);
}

export function telemetryChartPoints(
  points: TelemetrySnapshot["series"][number]["points"],
  from: string,
  to: string,
) {
  return points;
}

function telemetryYAxisDomain(points: TelemetrySnapshot["series"][number]["points"]) {
  if (points.length === 0) {
    return [0, 1];
  }
  const values = points.map((point) => point.value).filter(Number.isFinite);
  if (values.length === 0) {
    return [0, 1];
  }
  const min = Math.min(...values);
  const max = Math.max(...values);
  if (min === max) {
    if (max === 0) {
      return [0, 1];
    }
    const padding = Math.abs(max) * 0.2;
    return [Math.max(0, max - padding), max + padding];
  }
  return ["auto", "auto"];
}

function formatRateTick(value: number) {
  if (value === 0) {
    return "0";
  }
  const absValue = Math.abs(value);
  if (absValue >= 10) {
    return compactNumber(value);
  }
  if (absValue >= 1) {
    return trimFixed(value, 1);
  }
  if (absValue >= 0.1) {
    return trimFixed(value, 2);
  }
  if (absValue >= 0.01) {
    return trimFixed(value, 3);
  }
  if (absValue >= 0.001) {
    return trimFixed(value, 4);
  }
  return trimFixed(value, 5);
}

function trimFixed(value: number, digits: number) {
  return value.toFixed(digits).replace(/\.?0+$/, "");
}

function compactNumber(value: number) {
  return new Intl.NumberFormat(undefined, {
    notation: Math.abs(value) >= 10000 ? "compact" : "standard",
    maximumFractionDigits: value >= 1000 ? 0 : 1,
  }).format(value);
}

function formatTelemetryTick(value: string) {
  return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" }).format(new Date(value));
}

function formatTelemetryTime(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value));
}
