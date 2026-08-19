import { Fragment, useEffect, useRef, useState } from "react";
import { Braces, RefreshCw } from "lucide-react";
import { ControlApi, OperationLog, OperationLogQuery, Team } from "../api";
import { LoadingState, SectionHeading } from "../ui/components";
import { formatDate, readError, shortId } from "./utils";

const SEVERITIES = ["", "DEBUG", "INFO", "WARN", "ERROR"];
const PAGE_SIZES = [25, 50, 100, 250, 500];
const DETAIL_KEYS = [
  "route",
  "remote_ip",
  "request_id",
  "correlation_id",
  "reference_type",
  "reference_id",
  "stage",
  "reason_code",
  "from",
  "to",
  "attempts",
  "max_attempts",
  "next_attempt_at",
  "provider_kind",
  "provider_found",
  "provider_enabled",
  "groups_from_userinfo",
  "group_count",
  "configured_group_claim_count",
  "id_token_claim_count",
  "userinfo_claim_count",
  "status",
  "latency",
];
const RAW_DUPLICATE_KEYS = new Set(["time", "timestamp", "level", "severity", "msg", "message"]);

export function LogsPanel({ api, teams }: { api: ControlApi; teams: Team[] }) {
  const [logs, setLogs] = useState<OperationLog[]>([]);
  const [total, setTotal] = useState(0);
  const [query, setQuery] = useState<OperationLogQuery>({ limit: 100, offset: 0, sort: "timestamp", direction: "desc" });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [expandedLogId, setExpandedLogId] = useState("");
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
        <LoadingState label="Loading logs" />
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
	                <th>Raw</th>
	              </tr>
            </thead>
            <tbody>
              {logs.map((log) => {
                const summary = logSummary(log);
                const expanded = expandedLogId === log.id;
                return (
                  <Fragment key={log.id}>
                    <tr>
                      <td>{formatDate(log.timestamp)}</td>
                      <td><span className={severityClass(log.severity)}>{log.severity}</span></td>
                      <td>
                        <div className="log-message-cell">
                          <strong>{summary.title}</strong>
                          {summary.details.length > 0 && (
                            <div className="log-detail-list" aria-label="Log details">
                              {summary.details.map((detail) => (
                                <span className="log-detail-chip" key={detail}>{detail}</span>
                              ))}
                            </div>
                          )}
                        </div>
                      </td>
                      <td><code>{compactSource(log.source)}</code></td>
                      <td>{log.team_id ? (teamNames.get(log.team_id) ?? shortId(log.team_id)) : "System"}</td>
                      <td className="actions-cell">
                        <button
                          className="icon-button"
                          type="button"
                          aria-label={`${expanded ? "Hide" : "View"} raw log ${summary.title}`}
                          title="Raw log"
                          onClick={() => setExpandedLogId(expanded ? "" : log.id)}
                        >
                          <Braces size={16} aria-hidden="true" />
                        </button>
                      </td>
                    </tr>
	                    {expanded && (
	                      <tr className="log-raw-row">
	                        <td colSpan={6}>
	                          <pre aria-label={`Raw log body ${summary.title}`}>{JSON.stringify(rawLogRecord(log), null, 2)}</pre>
	                        </td>
	                      </tr>
	                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <div className="table-actions">
        <span className="form-meta">{rangeStart}-{rangeEnd} of {total}</span>
        <label className="table-page-size">
          Rows
          <select
            value={limit}
            disabled={loading}
            onChange={(event) => {
              const next = { ...query, limit: Number(event.target.value), offset: 0 };
              setQuery(next);
              void loadLogs(next);
            }}
          >
            {PAGE_SIZES.map((pageSize) => (
              <option value={pageSize} key={pageSize}>{pageSize}</option>
            ))}
          </select>
        </label>
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

type LogSummary = {
  title: string;
  details: string[];
};

function logSummary(log: OperationLog): LogSummary {
  const attrs = log.attrs ?? {};
  const httpTitle = httpLogTitle(log, attrs);
  const title = log.error || httpTitle || eventLabel(log.message);
  const details = [
    log.message !== title ? `event=${log.message}` : "",
    ...DETAIL_KEYS.map((key) => detailForKey(key, attrs)),
    log.error && log.error !== title ? `error=${log.error}` : "",
  ].filter(Boolean) as string[];
  const uniqueDetails = uniqueStrings(details);
  const compactDetails = uniqueDetails.slice(0, 8);
  const nextAttempt = detailForKey("next_attempt_at", attrs);
  if (nextAttempt && !compactDetails.includes(nextAttempt)) {
    compactDetails[compactDetails.length - 1] = nextAttempt;
  }
  const remaining = uniqueDetails.length - compactDetails.length;
  if (remaining > 0) {
    compactDetails.push(`+${remaining} more`);
  }
  return { title, details: compactDetails };
}

function httpLogTitle(log: OperationLog, attrs: Record<string, unknown>): string {
  if (log.message !== "http_request" && log.message !== "control_http_request") {
    return "";
  }
  const method = stringValue(attrs.method);
  const uri = stringValue(attrs.uri) || stringValue(attrs.route);
  const status = stringValue(attrs.status);
  const latency = stringValue(attrs.latency);
  const parts = [method, uri, status && `status ${status}`, latency].filter(Boolean);
  return parts.length > 0 ? parts.join(" ") : eventLabel(log.message);
}

function detailForKey(key: string, attrs: Record<string, unknown>): string {
  const value = attrs[key];
  if (value === undefined || value === null || value === "") {
    return "";
  }
  return `${key}=${displayValue(value)}`;
}

function rawLogRecord(log: OperationLog): Record<string, unknown> {
  const attrs = log.attrs ?? {};
  const raw: Record<string, unknown> = {
    time: log.timestamp,
    level: log.severity,
    msg: log.message,
  };
  Object.entries(attrs).forEach(([key, value]) => {
    if (!RAW_DUPLICATE_KEYS.has(key)) {
      raw[key] = value;
    }
  });
  addRawField(raw, "id", log.id);
  addRawField(raw, "source", log.source);
  addRawField(raw, "team_id", log.team_id);
  addRawField(raw, "profile_id", log.profile_id);
  addRawField(raw, "correlation_id", log.correlation_id);
  addRawField(raw, "error", log.error);
  return raw;
}

function addRawField(raw: Record<string, unknown>, key: string, value: unknown) {
  if (value !== undefined && value !== null && value !== "") {
    raw[key] = value;
  }
}

function eventLabel(value: string): string {
  return value.replaceAll("_", " ");
}

function stringValue(value: unknown): string {
  if (typeof value === "string") {
    return value.trim();
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return "";
}

function displayValue(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.length}]`;
  }
  if (typeof value === "object" && value !== null) {
    return "{...}";
  }
  return stringValue(value);
}

function uniqueStrings(values: string[]): string[] {
  return [...new Set(values)];
}
