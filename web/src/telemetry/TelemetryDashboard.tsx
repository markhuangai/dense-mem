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
  return (
    <div className="telemetry-dashboard">
      <div className="section-heading">
        <div>
          <h2>{title}</h2>
          {snapshot && (
            <p className="section-subtitle">
              {formatTelemetryTime(snapshot.window.from)} - {formatTelemetryTime(snapshot.window.to)}
            </p>
          )}
        </div>
        <button className="icon-button" type="button" aria-label="Refresh telemetry" onClick={onRefresh} disabled={loading}>
          <RefreshCw size={16} aria-hidden="true" />
        </button>
      </div>

      <div className="metrics-toolbar telemetry-toolbar">
        <label htmlFor={`${title}-telemetry-window`}>Telemetry range</label>
        <select
          id={`${title}-telemetry-window`}
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
      {snapshot?.message && <div className="banner neutral">{snapshot.message}</div>}
      {loading && !snapshot && <div className="table-placeholder compact">Loading telemetry</div>}

      {snapshot && (
        <>
          <div className="telemetry-card-grid" aria-label={`${title} totals`}>
            {snapshot.cards.map((card) => (
              <div className="summary-item" key={card.id}>
                <span>{card.label}</span>
                <strong>{formatTelemetryValue(card.value, card.unit)}</strong>
              </div>
            ))}
          </div>
          <div className="telemetry-chart-grid" aria-label={`${title} charts`}>
            {snapshot.series.map((series) => (
              <div className="telemetry-chart" key={series.id}>
                <div className="telemetry-chart-head">
                  <h3>{series.label}</h3>
                  <span>{series.unit}</span>
                </div>
                {series.points.length === 0 ? (
                  <div className="chart-empty">No samples</div>
                ) : (
                  <div className="telemetry-chart-body">
                    <ResponsiveContainer width="100%" height="100%" minWidth={280} minHeight={180}>
                      <LineChart data={series.points} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
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
                          tickFormatter={(value) => compactNumber(Number(value))}
                        />
                        <Tooltip
                          contentStyle={{
                            background: "var(--panel)",
                            border: "1px solid var(--border)",
                            borderRadius: 7,
                            color: "var(--text)",
                          }}
                          labelFormatter={(label) => formatTelemetryTime(String(label))}
                          formatter={(value) => [formatTelemetryValue(Number(value), series.unit), series.label]}
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
                )}
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

export function formatTelemetryValue(value: number, unit: string) {
  if (unit === "percent") {
    return `${value.toFixed(value >= 10 ? 0 : 1)}%`;
  }
  if (unit === "ms") {
    return `${value.toFixed(value >= 100 ? 0 : 1)} ms`;
  }
  if (unit.includes("/s") || unit === "rps") {
    return value.toFixed(value >= 10 ? 1 : 2);
  }
  return compactNumber(value);
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
