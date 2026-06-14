import { useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { ControlApi, OperationLog, OperationLogQuery, Team } from "../api";
import { SectionHeading } from "../ui/components";
import { formatDate, readError, shortId } from "./utils";

const SEVERITIES = ["", "DEBUG", "INFO", "WARN", "ERROR"];

export function LogsPanel({ api, teams }: { api: ControlApi; teams: Team[] }) {
  const [logs, setLogs] = useState<OperationLog[]>([]);
  const [total, setTotal] = useState(0);
  const [query, setQuery] = useState<OperationLogQuery>({ limit: 100, offset: 0, sort: "timestamp", direction: "desc" });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const requestSeqRef = useRef(0);

  async function loadLogs(nextQuery = query) {
    const requestSeq = requestSeqRef.current + 1;
    requestSeqRef.current = requestSeq;
    setLoading(true);
    setError("");
    try {
      const page = await api.listOperationLogs(nextQuery);
      if (requestSeq !== requestSeqRef.current) {
        return;
      }
      setLogs(page.data);
      setTotal(page.pagination.total);
      setQuery({ ...nextQuery, limit: page.pagination.limit, offset: page.pagination.offset });
    } catch (err) {
      if (requestSeq !== requestSeqRef.current) {
        return;
      }
      setError(readError(err));
    } finally {
      if (requestSeq === requestSeqRef.current) {
        setLoading(false);
      }
    }
  }

  useEffect(() => {
    void loadLogs();
  }, []);

  const teamNames = new Map(teams.map((team) => [team.id, team.name]));
  const offset = query.offset ?? 0;
  const limit = query.limit ?? 100;
  const rangeStart = total === 0 ? 0 : offset + 1;
  const rangeEnd = Math.min(offset + logs.length, total);

  return (
    <section className="surface">
      <SectionHeading
        title="Operation Logs"
        meta={total}
        actions={(
          <button className="icon-button" type="button" aria-label="Refresh logs" onClick={() => void loadLogs()}>
            <RefreshCw size={16} aria-hidden="true" />
          </button>
        )}
      />
      {error && <div className="banner error" role="alert">{error}</div>}
      <div className="metrics-toolbar">
        <label>
          Severity
          <select
            value={query.severity ?? ""}
            onChange={(event) => {
              const next = { ...query, severity: event.target.value, offset: 0 };
              setQuery(next);
              void loadLogs(next);
            }}
          >
            {SEVERITIES.map((severity) => (
              <option value={severity} key={severity}>{severity || "All"}</option>
            ))}
          </select>
        </label>
        <label>
          Sort
          <select
            value={query.sort ?? "timestamp"}
            onChange={(event) => {
              const next = { ...query, sort: event.target.value as OperationLogQuery["sort"], offset: 0 };
              setQuery(next);
              void loadLogs(next);
            }}
          >
            <option value="timestamp">Time</option>
            <option value="severity">Severity</option>
          </select>
        </label>
        <label>
          Direction
          <select
            value={query.direction ?? "desc"}
            onChange={(event) => {
              const next = { ...query, direction: event.target.value as OperationLogQuery["direction"], offset: 0 };
              setQuery(next);
              void loadLogs(next);
            }}
          >
            <option value="desc">Desc</option>
            <option value="asc">Asc</option>
          </select>
        </label>
      </div>
      {loading && logs.length === 0 ? (
        <div className="table-placeholder">Loading</div>
      ) : logs.length === 0 ? (
        <div className="table-placeholder">No logs</div>
      ) : (
        <div className="table-wrap">
          <table className="data-table logs-table">
            <thead>
              <tr>
                <th>Time</th>
                <th>Severity</th>
                <th>Message</th>
                <th>Source</th>
                <th>Team</th>
              </tr>
            </thead>
            <tbody>
              {logs.map((log) => (
                <tr key={log.id}>
                  <td>{formatDate(log.timestamp)}</td>
                  <td><span className={severityClass(log.severity)}>{log.severity}</span></td>
                  <td>
                    <strong>{log.message}</strong>
                    {log.error && <small>{log.error}</small>}
                  </td>
                  <td><code>{compactSource(log.source)}</code></td>
                  <td>{log.team_id ? (teamNames.get(log.team_id) ?? shortId(log.team_id)) : "System"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="table-actions">
        <span className="form-meta">{rangeStart}-{rangeEnd} of {total}</span>
        <button
          className="ghost-button"
          type="button"
          disabled={loading || offset === 0}
          onClick={() => {
            const next = { ...query, offset: Math.max(0, offset - limit) };
            setQuery(next);
            void loadLogs(next);
          }}
        >
          Previous
        </button>
        <button
          className="ghost-button"
          type="button"
          disabled={loading || offset + limit >= total}
          onClick={() => {
            const next = { ...query, offset: offset + limit };
            setQuery(next);
            void loadLogs(next);
          }}
        >
          Next
        </button>
      </div>
    </section>
  );
}

function severityClass(severity: string): string {
  switch (severity) {
    case "ERROR":
      return "status-pill error";
    case "WARN":
      return "status-pill warning";
    case "DEBUG":
      return "status-pill neutral";
    default:
      return "status-pill";
  }
}

function compactSource(source: string): string {
  return source.replace(/^.*\/dense-mem\//, "");
}
