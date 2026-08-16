import { useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { ControlApi, ControlMetrics, Credential, Team } from "../api";
import { TelemetryDashboard } from "../telemetry/TelemetryDashboard";
import { TelemetrySnapshot, TelemetryWindowKey } from "../telemetry/types";
import { useVisiblePolling } from "../telemetry/useVisiblePolling";
import { LoadingState, SectionHeading, SummaryCard } from "../ui/components";
import { dependencyStatusClass, formatCount, formatDate, formatLatency, formatPercent, readError, shortId } from "./utils";

type TelemetryControlScope = "system" | "team" | "profile";

export function MetricsPanel({ api, teams }: { api: ControlApi; teams: Team[] }) {
  const [metrics, setMetrics] = useState<ControlMetrics | null>(null);
  const [telemetry, setTelemetry] = useState<TelemetrySnapshot | null>(null);
  const [windowMinutes, setWindowMinutes] = useState(60);
  const [telemetryWindow, setTelemetryWindow] = useState<TelemetryWindowKey>("1h");
  const [telemetryScope, setTelemetryScope] = useState<TelemetryControlScope>("system");
  const [teamId, setTeamId] = useState("");
  const [telemetryTeamId, setTelemetryTeamId] = useState("");
  const [telemetryProfileId, setTelemetryProfileId] = useState("");
  const [teamCredentials, setTeamCredentials] = useState<Credential[]>([]);
  const [error, setError] = useState("");
  const [telemetryError, setTelemetryError] = useState("");
  const [loading, setLoading] = useState(false);
  const [telemetryLoading, setTelemetryLoading] = useState(false);
  const metricsRequestRef = useRef(0);
  const telemetryRequestRef = useRef(0);

  async function loadMetrics(nextWindow = windowMinutes, nextTeamId = teamId, signal?: AbortSignal) {
    const requestID = ++metricsRequestRef.current;
    setLoading(true);
    setError("");
    try {
      setMetrics(await api.getMetrics({
        window_minutes: nextWindow,
        team_id: nextTeamId || undefined,
      }, signal));
    } catch (err) {
      if (!isAbortError(err)) {
        setError(readError(err));
      }
    } finally {
      if (metricsRequestRef.current === requestID) {
        setLoading(false);
      }
    }
  }

  const refreshMetrics = useVisiblePolling(
    (signal) => loadMetrics(windowMinutes, teamId, signal),
    [api, windowMinutes, teamId],
  );

  async function loadTeamCredentials(nextTeamId = telemetryTeamId) {
    if (!nextTeamId) {
      setTeamCredentials([]);
      setTelemetryProfileId("");
      return;
    }
    try {
      const page = await api.listTeamCredentials(nextTeamId);
      setTeamCredentials(page.data);
      setTelemetryProfileId((current) => (current && page.data.some((credential) => credential.id === current) ? current : page.data[0]?.id ?? ""));
    } catch (err) {
      setTelemetryError(readError(err));
      setTeamCredentials([]);
    }
  }

  async function loadTelemetry(
    nextWindow = telemetryWindow,
    nextScope = telemetryScope,
    nextTeamId = telemetryTeamId,
    nextProfileId = telemetryProfileId,
    signal?: AbortSignal,
  ) {
    const requestID = ++telemetryRequestRef.current;
    if (nextScope === "team" && !nextTeamId) {
      setTelemetryError("Select a team.");
      setTelemetryLoading(false);
      return;
    }
    if (nextScope === "profile" && !nextProfileId) {
      setTelemetryError("Select a credential.");
      setTelemetryLoading(false);
      return;
    }
    setTelemetryLoading(true);
    setTelemetryError("");
    try {
      setTelemetry(await api.getTelemetry({
        window: nextWindow,
        scope: nextScope,
        team_id: nextScope === "team" || nextScope === "profile" ? nextTeamId || undefined : undefined,
        profile_id: nextScope === "profile" ? nextProfileId || undefined : undefined,
      }, signal));
    } catch (err) {
      if (!isAbortError(err)) {
        setTelemetryError(readError(err));
      }
    } finally {
      if (telemetryRequestRef.current === requestID) {
        setTelemetryLoading(false);
      }
    }
  }

  const refreshTelemetry = useVisiblePolling(
    (signal) => loadTelemetry(telemetryWindow, telemetryScope, telemetryTeamId, telemetryProfileId, signal),
    [api, telemetryWindow, telemetryScope, telemetryTeamId, telemetryProfileId],
  );

  useEffect(() => {
    if (telemetryScope === "profile") {
      void loadTeamCredentials();
    } else {
      setTeamCredentials([]);
      setTelemetryProfileId("");
    }
  }, [telemetryScope, telemetryTeamId]);

  function changeTelemetryScope(scope: TelemetryControlScope) {
    setTelemetryScope(scope);
    if (scope === "system") {
      setTelemetryTeamId("");
      setTelemetryProfileId("");
    } else if (!telemetryTeamId && teams.length > 0) {
      setTelemetryTeamId(teams[0].id);
    }
  }

  return (
    <section className="surface metrics-panel">
      <TelemetryDashboard
        title="Telemetry"
        snapshot={telemetry}
        windowKey={telemetryWindow}
        loading={telemetryLoading}
        error={telemetryError}
        onWindowChange={setTelemetryWindow}
        onRefresh={() => void refreshTelemetry()}
        controls={(
          <>
            <label htmlFor="telemetry-scope">Scope</label>
            <select id="telemetry-scope" value={telemetryScope} onChange={(event) => changeTelemetryScope(event.target.value as TelemetryControlScope)}>
              <option value="system">All teams</option>
              <option value="team">Team</option>
              <option value="profile">Credential</option>
            </select>
            {telemetryScope !== "system" && (
              <>
                <label htmlFor="telemetry-team">Team</label>
                <select id="telemetry-team" value={telemetryTeamId} onChange={(event) => setTelemetryTeamId(event.target.value)}>
                  <option value="">Select team</option>
                  {teams.map((team) => (
                    <option key={team.id} value={team.id}>{team.name}</option>
                  ))}
                </select>
              </>
            )}
            {telemetryScope === "profile" && (
              <>
                <label htmlFor="telemetry-profile">Credential</label>
                <select id="telemetry-profile" value={telemetryProfileId} onChange={(event) => setTelemetryProfileId(event.target.value)} disabled={!telemetryTeamId}>
                  <option value="">Select credential</option>
                  {teamCredentials.map((credential) => (
                    <option key={credential.id} value={credential.id}>{credential.name || shortId(credential.id)}</option>
                  ))}
                </select>
              </>
            )}
          </>
        )}
      />

      <SectionHeading
        title="Usage Rollup"
        subtitle={metrics ? `${formatDate(metrics.window.from)} - ${formatDate(metrics.window.to)}` : undefined}
        actions={(
          <button className="icon-button" type="button" aria-label="Refresh metrics" onClick={() => void refreshMetrics()} disabled={loading}>
            <RefreshCw size={16} aria-hidden="true" />
          </button>
        )}
      />
      {error && <div className="banner error" role="alert">{error}</div>}

      <div className="metrics-toolbar">
        <label htmlFor="metrics-window">Window</label>
        <select
          id="metrics-window"
          value={windowMinutes}
          onChange={(event) => setWindowMinutes(Number.parseInt(event.target.value, 10))}
        >
          <option value={60}>1 hour</option>
          <option value={360}>6 hours</option>
          <option value={1440}>24 hours</option>
          <option value={10080}>7 days</option>
        </select>
        <label htmlFor="metrics-team">Team</label>
        <select id="metrics-team" value={teamId} onChange={(event) => setTeamId(event.target.value)}>
          <option value="">All teams</option>
          {teams.map((team) => (
            <option key={team.id} value={team.id}>{team.name}</option>
          ))}
        </select>
      </div>

      {loading && !metrics && <LoadingState label="Loading usage rollup" />}
      {metrics && (
        <>
          <MetricsSummary metrics={metrics} />
          <DependencySummary dependencies={metrics.dependencies} checkedAt={metrics.dependencies_checked_at} />
          <MetricsTeamTable teams={metrics.teams} />
          <MetricsKeyTable keys={metrics.keys} />
          <MetricsRouteTable routes={metrics.routes} />
        </>
      )}
    </section>
  );
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function MetricsSummary({ metrics }: { metrics: ControlMetrics }) {
  return (
    <div className="metrics-summary" aria-label="Request metrics">
      <SummaryCard label="Requests" value={formatCount(metrics.system.requests)} />
      <SummaryCard label="Errors" value={formatCount(metrics.system.errors)} tone={metrics.system.errors > 0 ? "warning" : "neutral"} />
      <SummaryCard label="Error rate" value={formatPercent(metrics.system.errors, metrics.system.requests)} />
      <SummaryCard label="Avg latency" value={formatLatency(metrics.system.avg_latency_ms)} />
      <SummaryCard label="Max latency" value={formatLatency(metrics.system.max_latency_ms)} />
    </div>
  );
}

function DependencySummary({ dependencies, checkedAt }: { dependencies: ControlMetrics["dependencies"]; checkedAt?: string }) {
  if (dependencies.length === 0) {
    return null;
  }
  return (
    <div className="metrics-block">
      <div className="list-toolbar">
        <div>
          <h3>Dependencies</h3>
          <span>{dependencies.filter((dependency) => dependency.status === "ok").length}/{dependencies.length} healthy{checkedAt ? ` · observed ${formatDate(checkedAt)}` : ""}</span>
        </div>
      </div>
      <div className="dependency-grid">
        {dependencies.map((dep) => (
          <SummaryCard
            key={dep.name}
            label={dep.name}
            value={<span className={`status-pill ${dependencyStatusClass(dep.status)}`}>{dep.status}</span>}
            detail={dependencyDetail(dep)}
            tone={dep.status === "error" ? "danger" : dep.status === "degraded" ? "warning" : "neutral"}
          />
        ))}
      </div>
    </div>
  );
}

function dependencyDetail(dependency: ControlMetrics["dependencies"][number]) {
  const latency = dependency.latency_ms === null || dependency.latency_ms === undefined ? "n/a" : formatLatency(dependency.latency_ms);
  if (dependency.reason_code) {
    return `${latency} · ${dependency.reason_code}${dependency.message ? ` · ${dependency.message}` : ""}`;
  }
  return latency;
}

function MetricsTeamTable({ teams }: { teams: ControlMetrics["teams"] }) {
  return (
    <div className="metrics-block">
      <div className="list-toolbar">
        <div>
          <h3>Team usage</h3>
          <span>{teams.length}</span>
        </div>
      </div>
      {teams.length === 0 ? <div className="table-placeholder compact">No team usage</div> : (
        <div className="table-wrap">
          <table className="data-table metrics-table">
            <thead>
              <tr>
                <th>Team</th>
                <th>Requests</th>
                <th>Errors</th>
                <th>Avg latency</th>
                <th>Max latency</th>
              </tr>
            </thead>
            <tbody>
              {teams.map((team) => (
                <tr key={team.team_id}>
                  <td>{team.team_name || shortId(team.team_id)}</td>
                  <td>{formatCount(team.requests)}</td>
                  <td>{formatCount(team.errors)}</td>
                  <td>{formatLatency(team.avg_latency_ms)}</td>
                  <td>{formatLatency(team.max_latency_ms)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function MetricsKeyTable({ keys }: { keys: ControlMetrics["keys"] }) {
  return (
    <div className="metrics-block">
      <div className="list-toolbar">
        <div>
          <h3>Credential usage</h3>
          <span>{keys.length}</span>
        </div>
      </div>
      {keys.length === 0 ? <div className="table-placeholder compact">No credential usage</div> : (
        <div className="table-wrap">
          <table className="data-table metrics-table">
            <thead>
              <tr>
                <th>Credential</th>
                <th>Key</th>
                <th>Team</th>
                <th>Requests</th>
                <th>Errors</th>
                <th>Avg latency</th>
              </tr>
            </thead>
            <tbody>
              {keys.map((key) => (
                <tr key={key.key_id}>
                  <td>{key.key_name || shortId(key.key_id)}</td>
                  <td><code>{key.key_suffix ? `******${key.key_suffix}` : shortId(key.key_id)}</code></td>
                  <td>{key.team_name || shortId(key.team_id)}</td>
                  <td>{formatCount(key.requests)}</td>
                  <td>{formatCount(key.errors)}</td>
                  <td>{formatLatency(key.avg_latency_ms)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function MetricsRouteTable({ routes }: { routes: ControlMetrics["routes"] }) {
  return (
    <div className="metrics-block">
      <div className="list-toolbar">
        <div>
          <h3>Routes</h3>
          <span>{routes.length}</span>
        </div>
      </div>
      {routes.length === 0 ? <div className="table-placeholder compact">No route usage</div> : (
        <div className="table-wrap">
          <table className="data-table metrics-table route-metrics-table">
            <thead>
              <tr>
                <th>Route</th>
                <th>Method</th>
                <th>Status</th>
                <th>Requests</th>
                <th>Errors</th>
              </tr>
            </thead>
            <tbody>
              {routes.map((route) => (
                <tr key={`${route.method}-${route.route}-${route.status_class}`}>
                  <td><code>{route.route}</code></td>
                  <td>{route.method}</td>
                  <td>{route.status_class}</td>
                  <td>{formatCount(route.requests)}</td>
                  <td>{formatCount(route.errors)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
