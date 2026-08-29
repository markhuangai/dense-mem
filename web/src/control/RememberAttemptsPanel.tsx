import { CSSProperties, MutableRefObject, useEffect, useRef, useState } from "react";
import { AlertTriangle, ArrowRight, Clock3, Download, RefreshCw } from "lucide-react";
import {
  ControlApi,
  RememberAttemptDiagnosticDetail,
  RememberAttemptDiagnosticEvent,
  RememberAttemptDiagnosticSummary,
  RememberFailureArtifactDescriptor,
  Team,
} from "../api";
import { LoadingState, SectionHeading } from "../ui/components";
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
  const artifactRequestRef = useRef(0);
  const selectedIDRef = useRef("");

  function selectAttempt(attemptID: string) {
    selectedIDRef.current = attemptID;
    setSelectedID(attemptID);
  }

  async function loadAttempts(nextOutcome = outcome, nextOffset = offset) {
    const requestID = ++listRequestRef.current;
    detailRequestRef.current += 1;
    artifactRequestRef.current += 1;
    setDetail(null);
    setDetailLoading(false);
    setLoading(true);
    setError("");
    try {
      const page = await api.listRememberAttemptDiagnostics({
        team_id: team.id,
        outcome: nextOutcome as "" | "completed" | "rejected" | "quarantined" | "failed" | "replayed",
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
    artifactRequestRef.current += 1;
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
      artifactRequestRef.current += 1;
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

      {detailLoading && !detail ? <LoadingState label="Loading Remember attempt details" /> : detail && <RememberAttemptDetailView api={api} team={team} detail={detail} requestRef={artifactRequestRef} />}
    </div>
  );
}

function RememberAttemptDetailView({
  api,
  team,
  detail,
  requestRef,
}: {
  api: ControlApi;
  team: Team;
  detail: RememberAttemptDiagnosticDetail;
  requestRef: MutableRefObject<number>;
}) {
  const [artifactID, setArtifactID] = useState("");
  const [artifactText, setArtifactText] = useState("");
  const [artifactLoading, setArtifactLoading] = useState(false);
  const [artifactError, setArtifactError] = useState("");

  useEffect(() => {
    setArtifactID("");
    setArtifactText("");
    setArtifactError("");
    setArtifactLoading(false);
  }, [detail.attempt_id]);

  async function loadArtifact(artifact: RememberFailureArtifactDescriptor) {
    const requestID = ++requestRef.current;
    setArtifactID(artifact.artifact_id);
    setArtifactText("");
    setArtifactError("");
    setArtifactLoading(true);
    try {
      const bytes = await api.getRememberFailureArtifact(team.id, detail.attempt_id, artifact.artifact_id);
      if (requestID !== requestRef.current) return;
      setArtifactText(new TextDecoder().decode(bytes));
    } catch (caught) {
      if (requestID === requestRef.current) setArtifactError(readError(caught));
    } finally {
      if (requestID === requestRef.current) setArtifactLoading(false);
    }
  }

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

      <section className="remember-artifacts" aria-label="Failure artifacts">
        <h3>Failure artifacts</h3>
        {detail.artifacts.length === 0 ? <div className="table-placeholder compact">No unexpired failure artifacts.</div> : detail.artifacts.map((artifact) => (
          <article className="remember-artifact" key={artifact.artifact_id}>
            <div><strong>{artifact.artifact_kind}</strong><small>{artifact.content_type} · {artifact.byte_count} bytes · {artifact.content_sha256}</small><small>Expires {formatDate(artifact.expires_at)}</small></div>
            <button className="ghost-button" type="button" onClick={() => void loadArtifact(artifact)} disabled={artifactLoading && artifactID === artifact.artifact_id}><Download size={14} aria-hidden="true" />{artifactLoading && artifactID === artifact.artifact_id ? "Loading" : "View"}</button>
            {artifactID === artifact.artifact_id && artifactError && <p className="field-error" role="alert">{artifactError}</p>}
            {artifactID === artifact.artifact_id && artifactText && <pre className="remember-artifact-content">{artifactText}</pre>}
          </article>
        ))}
      </section>
    </section>
  );
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
    case "rejected":
    case "quarantined": return "status-pill warning";
    default: return "status-pill";
  }
}
