import { ResponsiveInspector, useCompactWorkspace } from "../ui/inspector";
import { Button } from "../ui/primitives/button";
import { Label } from "../ui/primitives/label";
import { NativeSelect, NativeSelectOption } from "../ui/primitives/native-select";
import { Input } from "../ui/primitives/input";
import { Table, TableHeader, TableRow, TableHead, TableBody, TableCell } from "../ui/primitives/table";
import { Fragment, useEffect, useRef, useState } from "react";
import { Braces, RefreshCw } from "lucide-react";
import { ControlApi, OperationLog, OperationLogQuery, Team } from "../api";
import { LoadingState, SectionHeading } from "../ui/components";
import { formatDate, readError, shortId } from "./utils";

const SEVERITIES = ["", "TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"];
const PAGE_SIZES = [25, 50, 100, 250, 500];
const DETAIL_KEYS = [
  "route",
  "remote_ip",
  "request_id",
  "correlation_id",
  "worker_kind",
  "submission_id",
  "reference_type",
  "reference_id",
  "stage",
  "reason_code",
  "failure_stage",
  "failure_reason_code",
  "failure_class",
  "validation_stage",
  "provider_status",
  "assessor_turns",
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
  "delivery_stage",
];
const RAW_DUPLICATE_KEYS = new Set(["time", "timestamp", "level", "severity", "msg", "message"]);
const EMPTY_LOG_QUERY: OperationLogQuery = {};
const FREE_TEXT_FILTER_DEBOUNCE_MS = 300;

export function LogsPanel({ api, teams, initialQuery = EMPTY_LOG_QUERY }: { api: ControlApi; teams: Team[]; initialQuery?: OperationLogQuery }) {
  const { ref, compact } = useCompactWorkspace(760);
  const [logs, setLogs] = useState<OperationLog[]>([]);
  const [total, setTotal] = useState(0);
  const [query, setQuery] = useState<OperationLogQuery>({ limit: 100, offset: 0, sort: "timestamp", direction: "desc", ...initialQuery });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [expandedLogId, setExpandedLogId] = useState("");
  const requestSeqRef = useRef(0);
  const freeTextFilterTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  function clearFreeTextFilterTimer() {
    if (freeTextFilterTimerRef.current !== null) {
      clearTimeout(freeTextFilterTimerRef.current);
      freeTextFilterTimerRef.current = null;
    }
  }

  async function loadLogs(nextQuery = query) {
    clearFreeTextFilterTimer();
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

  function scheduleFreeTextLoad(nextQuery: OperationLogQuery) {
    clearFreeTextFilterTimer();
    requestSeqRef.current += 1;
    freeTextFilterTimerRef.current = setTimeout(() => {
      freeTextFilterTimerRef.current = null;
      void loadLogs(nextQuery);
    }, FREE_TEXT_FILTER_DEBOUNCE_MS);
  }

  useEffect(() => {
    void loadLogs({ limit: 100, offset: 0, sort: "timestamp", direction: "desc", ...initialQuery });
  }, [initialQuery]);

  useEffect(() => clearFreeTextFilterTimer, []);

  const teamNames = new Map(teams.map((team) => [team.id, team.name]));
  const offset = query.offset ?? 0;
  const limit = query.limit ?? 100;
  const rangeStart = total === 0 ? 0 : offset + 1;
  const rangeEnd = Math.min(offset + logs.length, total);

  return (
    <section className="surface" ref={ref}>
      <SectionHeading
        title="Operation Logs"
        meta={total}
        actions={(
          <Button variant="ghost" size="icon" className="icon-button" type="button" aria-label="Refresh logs" onClick={() => void loadLogs()}>
            <RefreshCw size={16} aria-hidden="true" />
          </Button>
        )}
      />
      {error && <div className="banner error" role="alert">{error}</div>}
      <div className="metrics-toolbar">
        <Label>
          Severity
          <NativeSelect
            value={query.severity ?? ""}
            onChange={(event) => {
              const next = { ...query, severity: event.target.value, offset: 0 };
              setQuery(next);
              void loadLogs(next);
            }}
          >
            {SEVERITIES.map((severity) => (
              <NativeSelectOption value={severity} key={severity}>{severity || "All"}</NativeSelectOption>
            ))}
          </NativeSelect>
        </Label>
        <Label>
          Sort
          <NativeSelect
            value={query.sort ?? "timestamp"}
            onChange={(event) => {
              const next = { ...query, sort: event.target.value as OperationLogQuery["sort"], offset: 0 };
              setQuery(next);
              void loadLogs(next);
            }}
          >
            <NativeSelectOption value="timestamp">Time</NativeSelectOption>
            <NativeSelectOption value="severity">Severity</NativeSelectOption>
          </NativeSelect>
        </Label>
        <Label>
          Direction
          <NativeSelect
            value={query.direction ?? "desc"}
            onChange={(event) => {
              const next = { ...query, direction: event.target.value as OperationLogQuery["direction"], offset: 0 };
              setQuery(next);
              void loadLogs(next);
            }}
          >
            <NativeSelectOption value="desc">Desc</NativeSelectOption>
            <NativeSelectOption value="asc">Asc</NativeSelectOption>
          </NativeSelect>
        </Label>
        <Label>
          Correlation
          <Input
            aria-label="Correlation ID"
            value={query.correlation_id ?? ""}
            onChange={(event) => {
              const next = { ...query, correlation_id: event.target.value, offset: 0 };
              setQuery(next);
              scheduleFreeTextLoad(next);
            }}
          />
        </Label>
        <Label>
          Request hash
          <Input
            aria-label="Request hash"
            value={query.request_hash ?? ""}
            onChange={(event) => {
              const next = { ...query, request_hash: event.target.value, offset: 0 };
              setQuery(next);
              scheduleFreeTextLoad(next);
            }}
          />
        </Label>
        <Label>
          Invocation
          <Input
            aria-label="Invocation ID"
            value={query.invocation_id ?? ""}
            onChange={(event) => {
              const next = { ...query, invocation_id: event.target.value, offset: 0 };
              setQuery(next);
              scheduleFreeTextLoad(next);
            }}
          />
        </Label>
        <Label>
          Attempt
          <Input
            aria-label="Attempt ID"
            value={query.attempt_id ?? ""}
            onChange={(event) => {
              const next = { ...query, attempt_id: event.target.value, offset: 0 };
              setQuery(next);
              scheduleFreeTextLoad(next);
            }}
          />
        </Label>
        <Label>
          Call kind
          <NativeSelect
            aria-label="Call classification"
            value={query.classification ?? ""}
            onChange={(event) => {
              const next = { ...query, classification: event.target.value as OperationLogQuery["classification"], offset: 0 };
              setQuery(next);
              void loadLogs(next);
            }}
          >
            <NativeSelectOption value="">All calls</NativeSelectOption>
            <NativeSelectOption value="execution">Execution</NativeSelectOption>
            <NativeSelectOption value="replay">Replay</NativeSelectOption>
            <NativeSelectOption value="conflict">Conflict</NativeSelectOption>
          </NativeSelect>
        </Label>
        <Label>
          Retryable
          <NativeSelect
            aria-label="Retryable"
            value={query.retryable === "" || query.retryable === undefined ? "" : String(query.retryable)}
            onChange={(event) => {
              const raw = event.target.value;
              const retryable: OperationLogQuery["retryable"] = raw === "" ? "" : raw === "true";
              const next = { ...query, retryable, offset: 0 };
              setQuery(next);
              void loadLogs(next);
            }}
          >
            <NativeSelectOption value="">Any</NativeSelectOption>
            <NativeSelectOption value="true">Yes</NativeSelectOption>
            <NativeSelectOption value="false">No</NativeSelectOption>
          </NativeSelect>
        </Label>
      </div>
      {loading && logs.length === 0 ? (
        <LoadingState label="Loading logs" />
      ) : logs.length === 0 ? (
        <div className="table-placeholder">No logs</div>
      ) : (
        <div className="table-wrap">
          <Table className="data-table logs-table">
            <TableHeader>
              <TableRow>
                <TableHead>Time</TableHead>
                <TableHead>Severity</TableHead>
	                <TableHead>Message</TableHead>
	                <TableHead>Source</TableHead>
	                <TableHead>Team</TableHead>
	                <TableHead>Raw</TableHead>
	              </TableRow>
            </TableHeader>
            <TableBody>
              {logs.map((log) => {
                const summary = logSummary(log);
                const expanded = expandedLogId === log.id;
                return (
                  <Fragment key={log.id}>
                    <TableRow>
                      <TableCell>{formatDate(log.timestamp)}</TableCell>
                      <TableCell><span className={severityClass(log.severity)}>{log.severity}</span></TableCell>
                      <TableCell>
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
                      </TableCell>
                      <TableCell><code>{compactSource(log.source)}</code></TableCell>
                      <TableCell>{log.team_id ? (teamNames.get(log.team_id) ?? shortId(log.team_id)) : "System"}</TableCell>
                      <TableCell className="actions-cell">
                        <Button variant="ghost" size="icon"
                          className="icon-button"
                          type="button"
                          aria-label={`${expanded ? "Hide" : "View"} raw log ${summary.title}`}
                          title="Raw log"
                          onClick={() => setExpandedLogId(expanded ? "" : log.id)}
                        >
                          <Braces size={16} aria-hidden="true" />
                        </Button>
                      </TableCell>
                    </TableRow>
	                    {expanded && !compact && (
	                      <TableRow className="log-raw-row">
	                        <TableCell colSpan={6}>
	                          <pre aria-label={`Raw log body ${summary.title}`}>{JSON.stringify(rawLogRecord(log), null, 2)}</pre>
	                        </TableCell>
	                      </TableRow>
	                    )}
                  </Fragment>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}
      <div className="table-actions">
        <span className="form-meta">{rangeStart}-{rangeEnd} of {total}</span>
        <Label className="table-page-size">
          Rows
          <NativeSelect
            value={limit}
            disabled={loading}
            onChange={(event) => {
              const next = { ...query, limit: Number(event.target.value), offset: 0 };
              setQuery(next);
              void loadLogs(next);
            }}
          >
            {PAGE_SIZES.map((pageSize) => (
              <NativeSelectOption value={pageSize} key={pageSize}>{pageSize}</NativeSelectOption>
            ))}
          </NativeSelect>
        </Label>
        <Button variant="outline"
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
        </Button>
        <Button variant="outline"
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
        </Button>
      </div>
      <ResponsiveInspector compact open={compact && Boolean(expandedLogId)} onOpenChange={(open) => { if (!open) setExpandedLogId(""); }} title="Raw log">
        {logs.filter((log) => log.id === expandedLogId).map((log) => <pre key={log.id} aria-label={`Raw log body ${logSummary(log).title}`}>{JSON.stringify(rawLogRecord(log), null, 2)}</pre>)}
      </ResponsiveInspector>
    </section>
  );
}

function severityClass(severity: string): string {
  switch (severity) {
    case "FATAL":
    case "ERROR":
      return "status-pill error";
    case "WARN":
      return "status-pill warning";
    case "DEBUG":
    case "TRACE":
      return "status-pill neutral";
    default:
      return "status-pill";
  }
}

function compactSource(source?: string): string {
  return source ? source.replace(/^.*\/dense-mem\//, "") : "Not recorded";
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
