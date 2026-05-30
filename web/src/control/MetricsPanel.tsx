import { useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { ControlApi, ControlMetrics, Team } from "../api";
import { dependencyStatusClass, formatCount, formatDate, formatLatency, formatPercent, readError, shortId } from "./utils";

export function MetricsPanel({ api, teams }: { api: ControlApi; teams: Team[] }) {
  const [metrics, setMetrics] = useState<ControlMetrics | null>(null);
  const [windowMinutes, setWindowMinutes] = useState(60);
  const [teamId, setTeamId] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function loadMetrics(nextWindow = windowMinutes, nextTeamId = teamId) {
    setLoading(true);
    setError("");
    try {
      setMetrics(await api.getMetrics({
        window_minutes: nextWindow,
        team_id: nextTeamId || undefined,
      }));
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void loadMetrics();
  }, [windowMinutes, teamId]);

  return (
    <section className="surface metrics-panel">
      <div className="section-heading">
        <div>
          <h2>Metrics</h2>
          {metrics && <p className="section-subtitle">{formatDate(metrics.window.from)} - {formatDate(metrics.window.to)}</p>}
        </div>
        <button className="icon-button" type="button" aria-label="Refresh metrics" onClick={() => void loadMetrics()} disabled={loading}>
          <RefreshCw size={16} aria-hidden="true" />
        </button>
      </div>
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

      {loading && !metrics && <div className="table-placeholder">Loading</div>}
      {metrics && (
        <>
          <MetricsSummary metrics={metrics} />
          <DependencySummary dependencies={metrics.dependencies} />
          <MetricsTeamTable teams={metrics.teams} />
          <MetricsKeyTable keys={metrics.keys} />
          <MetricsRouteTable routes={metrics.routes} />
        </>
      )}
    </section>
  );
}

function MetricsSummary({ metrics }: { metrics: ControlMetrics }) {
  return (
    <div className="metrics-summary" aria-label="Request metrics">
      <div className="summary-item">
        <span>Requests</span>
        <strong>{formatCount(metrics.system.requests)}</strong>
      </div>
      <div className="summary-item">
        <span>Errors</span>
        <strong>{formatCount(metrics.system.errors)}</strong>
      </div>
      <div className="summary-item">
        <span>Error rate</span>
        <strong>{formatPercent(metrics.system.errors, metrics.system.requests)}</strong>
      </div>
      <div className="summary-item">
        <span>Avg latency</span>
        <strong>{formatLatency(metrics.system.avg_latency_ms)}</strong>
      </div>
      <div className="summary-item">
        <span>Max latency</span>
        <strong>{formatLatency(metrics.system.max_latency_ms)}</strong>
      </div>
    </div>
  );
}

function DependencySummary({ dependencies }: { dependencies: ControlMetrics["dependencies"] }) {
  if (dependencies.length === 0) {
    return null;
  }
  return (
    <div className="metrics-block">
      <div className="list-toolbar">
        <div>
          <h3>Dependencies</h3>
          <span>{dependencies.length}</span>
        </div>
      </div>
      <div className="dependency-grid">
        {dependencies.map((dep) => (
          <div className="summary-item" key={dep.name}>
            <span>{dep.name}</span>
            <strong><span className={`status-pill ${dependencyStatusClass(dep.status)}`}>{dep.status}</span></strong>
            <small>{dep.latency_ms === null || dep.latency_ms === undefined ? "n/a" : formatLatency(dep.latency_ms)}</small>
          </div>
        ))}
      </div>
    </div>
  );
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
          <h3>Key usage</h3>
          <span>{keys.length}</span>
        </div>
      </div>
      {keys.length === 0 ? <div className="table-placeholder compact">No key usage</div> : (
        <div className="table-wrap">
          <table className="data-table metrics-table">
            <thead>
              <tr>
                <th>Profile</th>
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
