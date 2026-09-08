import { CSSProperties, useEffect, useRef, useState } from "react";
import { AlertTriangle, ArrowRight, Clock3, RefreshCw } from "lucide-react";
import {
  ControlApi,
  RememberAttemptDiagnosticDetail,
  RememberAttemptDiagnosticEvent,
  RememberAttemptDiagnosticSummary,
  RememberDiagnosticExchange,
  Team,
  type RememberAttemptOutcome,
} from "../api";
import { LoadingState, SectionHeading, writeClipboardText } from "../ui/components";
import { formatDate, readError, shortId } from "./utils";

const OUTCOMES = ["", "completed", "rejected", "quarantined", "failed", "replayed"] as const;
const PAGE_SIZE = 50;

export function RememberAttemptsPanel({ api, team }: { api: ControlApi; team: Team }) {
  const [items, setItems] = useState<RememberAttemptDiagnosticSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [outcome, setOutcome] = useState("");
  const [offset, setOffset] = useState(0);
  const [selectedID, setSelectedID] = useState("");
  const [detail, setDetail] = useState<RememberAttemptDiagnosticDetail | null>(null);
  const [loading, setLoading] = useState(false);
  const [detailLoading, setDetailLoading] = useState(false);
  const [error, setError] = useState("");
  const listRequestRef = useRef(0);
  const detailRequestRef = useRef(0);
  const selectedIDRef = useRef("");

  function selectAttempt(attemptID: string) {
    selectedIDRef.current = attemptID;
    setSelectedID(attemptID);
  }

  async function loadAttempts(nextOutcome = outcome, nextOffset = offset) {
    const requestID = ++listRequestRef.current;
    detailRequestRef.current += 1;
    setDetail(null);
    setDetailLoading(false);
    setLoading(true);
    setError("");
    try {
      const page = await api.listRememberAttemptDiagnostics({
        team_id: team.id,
        outcome: nextOutcome as "" | RememberAttemptOutcome,
        limit: PAGE_SIZE,
        offset: nextOffset,
      });
      if (requestID !== listRequestRef.current) return;
      setItems(page.data);
      setTotal(page.pagination.total);
      setOffset(page.pagination.offset);
      const currentSelected = selectedIDRef.current;
      const nextSelected = page.data.some((item) => item.attempt_id === currentSelected)
        ? currentSelected
        : page.data[0]?.attempt_id ?? "";
      selectAttempt(nextSelected);
      if (nextSelected) {
        void loadDetail(nextSelected);
      } else {
        setDetail(null);
      }
    } catch (caught) {
      if (requestID === listRequestRef.current) setError(readError(caught));
    } finally {
      if (requestID === listRequestRef.current) setLoading(false);
    }
  }

  async function loadDetail(attemptID: string) {
    const requestID = ++detailRequestRef.current;
    selectedIDRef.current = attemptID;
    setDetailLoading(true);
    setError("");
    setDetail(null);
    try {
      const nextDetail = await api.getRememberAttemptDiagnostic(team.id, attemptID);
      if (requestID !== detailRequestRef.current) return;
      setDetail(nextDetail);
    } catch (caught) {
      if (requestID === detailRequestRef.current) setError(readError(caught));
    } finally {
      if (requestID === detailRequestRef.current) setDetailLoading(false);
    }
  }

  useEffect(() => {
    selectedIDRef.current = "";
    setItems([]);
    setTotal(0);
    setOutcome("");
    setOffset(0);
    setSelectedID("");
    setDetail(null);
    setError("");
    void loadAttempts("", 0);
    return () => {
      listRequestRef.current += 1;
      detailRequestRef.current += 1;
    };
  }, [api, team.id]);

  const rangeStart = total === 0 ? 0 : offset + 1;
  const rangeEnd = Math.min(offset + items.length, total);

  return (
    <div className="team-embedded-panel remember-attempts">
      <section className="overview-panel">
        <SectionHeading
          title="Remember Attempts"
          meta={total}
          actions={(
            <button className="icon-button" type="button" aria-label="Refresh Remember attempts" onClick={() => void loadAttempts()}>
              <RefreshCw size={16} aria-hidden="true" />
            </button>
          )}
        />
        <p className="panel-intro">A durable, chronological transcript of synchronous Remember processing. Failure bytes are retained only for seven days.</p>
        {error && <div className="banner error" role="alert">{error}</div>}
        <div className="metrics-toolbar submission-toolbar">
          <label>
            Outcome
            <select
              aria-label="Remember attempt outcome"
              value={outcome}
              onChange={(event) => {
                const next = event.target.value;
                setOutcome(next);
                setOffset(0);
                setItems([]);
                setTotal(0);
                selectAttempt("");
                void loadAttempts(next, 0);
              }}
            >
              {OUTCOMES.map((value) => <option value={value} key={value}>{value ? outcomeLabel(value) : "All outcomes"}</option>)}
            </select>
          </label>
        </div>
        {loading && items.length === 0 ? (
          <LoadingState label="Loading Remember attempts" />
        ) : items.length === 0 ? (
          <div className="table-placeholder">No Remember attempts match this outcome.</div>
        ) : (
          <div className="table-wrap">
            <table className="data-table remember-attempts-table">
              <thead>
                <tr><th>Created</th><th>Attempt</th><th>Outcome</th><th>Phase / error</th><th>Counts</th><th>Action</th></tr>
              </thead>
              <tbody>
                {items.map((item) => (
                  <tr key={item.attempt_id} className={item.attempt_id === selectedID ? "selected-row" : undefined}>
                    <td><strong>{formatDate(item.created_at)}</strong><small className="table-subline">{shortId(item.attempt_id)}</small></td>
                    <td><span>{item.submission_kind}</span><small className="table-subline">{item.owner_profile_id ? `Owner ${shortId(item.owner_profile_id)}` : "Owner not recorded"}</small></td>
                    <td><span className={attemptOutcomeClass(item.outcome)}>{outcomeLabel(item.outcome)}</span></td>
                    <td><span>{item.failed_phase ? outcomeLabel(item.failed_phase) : "—"}</span>{item.error_code && <small className="table-subline">{outcomeLabel(item.error_code)}</small>}</td>
                    <td><span>{item.evidence_count} evidence · {item.relationship_count} relationships</span><small className="table-subline">{item.duration_ms} ms</small></td>
                    <td><button className="text-button" type="button" aria-label={`Inspect Remember attempt ${item.attempt_id}`} onClick={() => { selectAttempt(item.attempt_id); void loadDetail(item.attempt_id); }}>Inspect <ArrowRight size={14} aria-hidden="true" /></button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="table-actions">
          <span className="form-meta">{rangeStart}-{rangeEnd} of {total}</span>
          <button className="ghost-button" type="button" disabled={loading || offset === 0} onClick={() => void loadAttempts(outcome, Math.max(0, offset - PAGE_SIZE))}>Previous</button>
          <button className="ghost-button" type="button" disabled={loading || offset + PAGE_SIZE >= total} onClick={() => void loadAttempts(outcome, offset + PAGE_SIZE)}>Next</button>
        </div>
      </section>

      {detailLoading && !detail ? <LoadingState label="Loading Remember attempt details" /> : detail && <RememberAttemptDetailView detail={detail} />}
    </div>
  );
}

function RememberAttemptDetailView({ detail }: { detail: RememberAttemptDiagnosticDetail }) {

  const result = detail.public_result;
  return (
    <section className="overview-panel remember-attempt-detail" aria-label="Remember attempt details">
      <SectionHeading title="Attempt Detail" actions={<span className={attemptOutcomeClass(detail.outcome)}>{outcomeLabel(detail.outcome)}</span>} />
      <div className="submission-facts">
        <Fact label="Attempt" value={detail.attempt_id} code />
        <Fact label="Contract" value={detail.contract_version === "remember_request_hash_v1" ? "Migrated history" : detail.contract_version} />
        <Fact label="Owner" value={detail.owner_profile_id || "Not recorded"} code={Boolean(detail.owner_profile_id)} />
        <Fact label="Correlation" value={detail.correlation_id || "Not recorded"} code={Boolean(detail.correlation_id)} />
        <Fact label="Phase" value={detail.failed_phase ? outcomeLabel(detail.failed_phase) : "Completed"} />
        <Fact label="Created" value={formatDate(detail.created_at)} />
        <Fact label="Duration" value={`${detail.duration_ms} ms`} />
        <Fact label="Evidence" value={String(detail.evidence_count)} />
        <Fact label="Relationships" value={String(detail.relationship_count)} />
      </div>

      {detail.error_code && <div className="submission-guidance"><span className="submission-guidance-icon"><AlertTriangle size={18} aria-hidden="true" /></span><div><strong>{outcomeLabel(detail.error_code)}</strong><small className="table-subline">{detail.error_code}</small><p>Processing stopped during {detail.failed_phase || "execution"}.</p></div></div>}

      <div className="remember-detail-grid">
        <section>
          <h3>Evidence dispositions</h3>
          {result.evidence.length === 0 ? <div className="table-placeholder compact">No evidence dispositions recorded.</div> : <div className="mini-table">{result.evidence.map((item) => <div className="mini-table-row" style={{ "--mini-cols": 4 } as CSSProperties} key={`${item.evidence_index}:${item.evidence_id ?? "none"}`}><span>{item.evidence_index}</span><span>{item.disposition}</span><code>{item.evidence_id ? shortId(item.evidence_id) : "Not recorded"}</code><span>{item.reason || "—"}</span></div>)}</div>}
        </section>
        <section>
          <h3>Relationship dispositions</h3>
          {result.relationship_results.length === 0 ? <div className="table-placeholder compact">No relationship dispositions recorded.</div> : <div className="mini-table">{result.relationship_results.map((item) => <div className="mini-table-row" style={{ "--mini-cols": 3 } as CSSProperties} key={item.ref}><code>{item.ref}</code><span>{item.disposition}</span><span>{item.reason || "—"}</span></div>)}</div>}
        </section>
      </div>

      {result.errors.length > 0 && <section className="remember-errors"><h3>Safe errors</h3>{result.errors.map((item) => <p key={`${item.code}:${item.message}`}><strong>{item.code}</strong> · {item.message}</p>)}</section>}

      <section className="remember-events" aria-label="Remember attempt events">
        <h3>Event spine</h3>
        {detail.events.length === 0 ? <div className="table-placeholder compact">No retained events.</div> : <ol className="submission-timeline">{detail.events.map((event) => <RememberEvent key={`${event.sequence_no}:${event.event_kind}`} event={event} />)}</ol>}
      </section>

      <RememberDiagnosticSection detail={detail} />
    </section>
  );
}

function RememberDiagnosticSection({ detail }: { detail: RememberAttemptDiagnosticDetail }) {
  const diagnostics = detail.diagnostics ?? { original_request: null, provider_exchanges: [], caller_response: null };
  return (
    <section className="remember-diagnostics" aria-label="Remember diagnostic exchanges">
      <h3>Original request</h3>
      {diagnostics.original_request ? <DiagnosticExchange exchange={diagnostics.original_request} requestOnly /> : <DiagnosticUnavailable message="The original request was not captured for this attempt." />}
      <h3>AI provider exchanges</h3>
      {diagnostics.provider_exchanges.length === 0 ? <DiagnosticUnavailable message="No provider exchange was captured; the provider was not called or capture ended before dispatch." /> : diagnostics.provider_exchanges.map((exchange) => <DiagnosticExchange exchange={exchange} key={exchange.diagnostic_id} />)}
      <h3>Response returned to caller</h3>
      {diagnostics.caller_response ? <DiagnosticExchange exchange={diagnostics.caller_response} responseOnly /> : <DiagnosticUnavailable message="The caller response was not captured for this attempt." />}
    </section>
  );
}

function DiagnosticExchange({ exchange, requestOnly = false, responseOnly = false }: { exchange: RememberDiagnosticExchange; requestOnly?: boolean; responseOnly?: boolean }) {
  const state = exchange.capture_state || exchange.outcome;
  return (
    <article className="remember-diagnostic-exchange">
      <div className="remember-diagnostic-meta">
        <strong>{exchange.component || exchange.kind}</strong>
        {exchange.model && <span>{exchange.model}</span>}
        {exchange.status_code ? <span>HTTP {exchange.status_code}</span> : null}
        <span>{outcomeLabel(state)}</span>
        <span>{exchange.retained_by_legal_hold ? "Legal hold" : `Expires ${formatDate(exchange.expires_at)}`}</span>
      </div>
      {state === "expired" ? <DiagnosticUnavailable message="This capture expired after seven days and its body is no longer available." /> : (
        <>
          {!responseOnly && exchange.request_body !== undefined && <DiagnosticBody label={requestOnly ? "Request body" : "Provider request"} content={exchange.request_body} />}
          {!requestOnly && exchange.response_body !== undefined && <DiagnosticBody label={responseOnly ? "Caller response" : "Provider response"} content={exchange.response_body} />}
          {(state === "not_captured" || state === "provider_not_called") && <DiagnosticUnavailable message={state === "provider_not_called" ? "The provider was not called for this failed attempt." : "This body was not captured before the attempt ended."} />}
          {state === "hash_only" && <DiagnosticUnavailable message="Only a request hash and bounded metadata were retained because security scanning rejected this request." />}
          {state === "no_response" && <DiagnosticUnavailable message="The provider call did not produce an HTTP response." />}
          {state === "interrupted" && <DiagnosticUnavailable message="Capture was interrupted before the provider response was fully read." />}
          {state === "not_delivered" && <DiagnosticUnavailable message="No response was delivered to the caller because the request ended before the server could return it." />}
          {state === "truncated" && <DiagnosticUnavailable message="The capture exceeded the diagnostic size limit; the displayed body is truncated." />}
        </>
      )}
    </article>
  );
}

function DiagnosticBody({ label, content }: { label: string; content: string }) {
  const [copyState, setCopyState] = useState<"idle" | "copied" | "failed">("idle");
  const copyFallbackRef = useRef<HTMLInputElement>(null);
  async function copyBody() {
    setCopyState(await writeClipboardText(content, copyFallbackRef.current) ? "copied" : "failed");
  }
  return <div className="remember-diagnostic-body"><div className="remember-diagnostic-body-heading"><strong>{label}</strong><button className="ghost-button" type="button" onClick={() => void copyBody()} disabled={!content}>{copyState === "copied" ? "Copied" : copyState === "failed" ? "Copy failed" : "Copy"}</button></div><input ref={copyFallbackRef} className="sr-only" value={content} readOnly tabIndex={-1} aria-hidden="true" /><pre>{content || "(empty body)"}</pre></div>;
}

function DiagnosticUnavailable({ message }: { message: string }) {
  return <div className="table-placeholder compact">{message}</div>;
}

function RememberEvent({ event }: { event: RememberAttemptDiagnosticEvent }) {
  return <li><span className="timeline-marker" aria-hidden="true"><Clock3 size={13} /></span><div><strong>{outcomeLabel(event.event_kind)}</strong><small>{formatDate(event.created_at)} · {event.phase}</small><pre className="remember-event-metadata">{JSON.stringify(event.metadata ?? {}, null, 2)}</pre></div></li>;
}

function Fact({ label, value, code = false }: { label: string; value: string; code?: boolean }) {
  return <div><span>{label}</span>{code ? <code>{value}</code> : <strong>{value}</strong>}</div>;
}

function outcomeLabel(value: string): string {
  if (!value) return "Unknown";
  return value.replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function attemptOutcomeClass(outcome: string): string {
  switch (outcome) {
    case "completed": return "status-pill success";
    case "failed": return "status-pill error";
    default: return "status-pill";
  }
}
