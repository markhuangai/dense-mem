import { useEffect, useRef, useState } from "react";
import { ArrowRight, Clock3, RefreshCw } from "lucide-react";
import {
  ControlApi,
  RememberFailureArtifactDescriptor,
  SubmissionDiagnosticDetail,
  SubmissionDiagnosticSummary,
  Team,
} from "../api";
import { LoadingState, SectionHeading } from "../ui/components";
import { formatDate, readError, shortId } from "./utils";

const PROCESSING_STATES = ["", "completed", "rejected", "quarantined", "failed", "replayed"];
const PAGE_SIZE = 50;

export function SubmissionsPanel({ api, team }: { api: ControlApi; team: Team }) {
  const [items, setItems] = useState<SubmissionDiagnosticSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [state, setState] = useState("");
  const [offset, setOffset] = useState(0);
  const [selectedID, setSelectedID] = useState("");
  const [detail, setDetail] = useState<SubmissionDiagnosticDetail | null>(null);
  const [loading, setLoading] = useState(false);
  const [detailLoading, setDetailLoading] = useState(false);
  const [error, setError] = useState("");
  const listRequestRef = useRef(0);
  const detailRequestRef = useRef(0);

  async function loadAttempts(nextState = state, nextOffset = offset) {
    const requestID = ++listRequestRef.current;
    detailRequestRef.current += 1;
    setLoading(true);
    setError("");
    try {
      const page = await api.listSubmissionDiagnostics({
        team_id: team.id, processing_state: nextState, limit: PAGE_SIZE, offset: nextOffset,
      });
      if (requestID !== listRequestRef.current) return;
      setItems(page.data);
      setTotal(page.pagination.total);
      setOffset(page.pagination.offset);
      const nextSelected = page.data.some((item) => item.submission_id === selectedID)
        ? selectedID : page.data[0]?.submission_id ?? "";
      setSelectedID(nextSelected);
      if (nextSelected) void loadDetail(nextSelected);
      else setDetail(null);
    } catch (caught) {
      if (requestID === listRequestRef.current) setError(readError(caught));
    } finally {
      if (requestID === listRequestRef.current) setLoading(false);
    }
  }

  async function loadDetail(submissionID: string) {
    const requestID = ++detailRequestRef.current;
    const requestTeamID = team.id;
    setDetailLoading(true);
    setError("");
    try {
      const nextDetail = await api.getSubmissionDiagnostic(requestTeamID, submissionID);
      if (requestID !== detailRequestRef.current || requestTeamID !== team.id) return;
      setDetail(nextDetail);
    } catch (caught) {
      if (requestID === detailRequestRef.current && requestTeamID === team.id) setError(readError(caught));
    } finally {
      if (requestID === detailRequestRef.current && requestTeamID === team.id) setDetailLoading(false);
    }
  }

  useEffect(() => {
    setItems([]);
    setTotal(0);
    setState("");
    setOffset(0);
    setSelectedID("");
    setDetail(null);
    void loadAttempts("", 0);
    return () => {
      listRequestRef.current += 1;
      detailRequestRef.current += 1;
    };
  }, [api, team.id]);

  const rangeStart = total === 0 ? 0 : offset + 1;
  const rangeEnd = Math.min(offset + items.length, total);

  return (
    <div className="team-embedded-panel submission-diagnostics">
      <section className="overview-panel">
        <SectionHeading
          title="Remember Attempts"
          meta={total}
          actions={<button className="icon-button" type="button" aria-label="Refresh Remember attempts" onClick={() => void loadAttempts()}><RefreshCw size={16} aria-hidden="true" /></button>}
        />
        <p className="panel-intro">Terminal attempts and their chronological events are the authoritative Remember diagnostic record.</p>
        {error && <div className="banner error" role="alert">{error}</div>}
        <div className="metrics-toolbar submission-toolbar">
          <label>Outcome
            <select aria-label="Processing state" value={state} onChange={(event) => {
              const next = event.target.value;
              setState(next); setOffset(0); setItems([]); setSelectedID(""); setDetail(null);
              void loadAttempts(next, 0);
            }}>
              {PROCESSING_STATES.map((value) => <option value={value} key={value}>{value ? stateLabel(value) : "All outcomes"}</option>)}
            </select>
          </label>
        </div>
        {loading && items.length === 0 ? <LoadingState label="Loading Remember attempts" /> : items.length === 0 ? (
          <div className="table-placeholder">No Remember attempts match this outcome.</div>
        ) : (
          <div className="table-wrap"><table className="data-table submissions-table">
            <thead><tr><th>Handled</th><th>Outcome</th><th>Failed phase</th><th>Evidence</th><th>Documents</th><th>Duration</th><th>Action</th></tr></thead>
            <tbody>{items.map((item) => (
              <tr key={item.submission_id} className={item.submission_id === selectedID ? "selected-row" : undefined}>
                <td><strong>{formatDate(item.completed_at ?? item.created_at)}</strong><small className="table-subline">{shortId(item.submission_id)}</small></td>
                <td><span className={submissionStateClass(item.processing_state)}>{stateLabel(item.processing_state)}</span>{item.historical && <small className="table-subline">Migrated history</small>}</td>
                <td>{item.failed_phase || item.error_code || "—"}</td>
                <td>{item.evidence_count}</td><td>{diagnosticMetric(item.historical, item.document_count)}</td><td>{diagnosticMetric(item.historical, item.duration_ms, " ms")}</td>
                <td><button className="text-button" type="button" aria-label={"Inspect Remember attempt " + item.submission_id} onClick={() => { setSelectedID(item.submission_id); void loadDetail(item.submission_id); }}>Inspect <ArrowRight size={14} aria-hidden="true" /></button></td>
              </tr>
            ))}</tbody>
          </table></div>
        )}
        <div className="table-actions">
          <span className="form-meta">{rangeStart}-{rangeEnd} of {total}</span>
          <button className="ghost-button" type="button" disabled={loading || offset === 0} onClick={() => void loadAttempts(state, Math.max(0, offset - PAGE_SIZE))}>Previous</button>
          <button className="ghost-button" type="button" disabled={loading || offset + PAGE_SIZE >= total} onClick={() => void loadAttempts(state, offset + PAGE_SIZE)}>Next</button>
        </div>
      </section>
      {detailLoading && !detail ? <LoadingState label="Loading Remember attempt details" /> : detail && <AttemptDetail api={api} detail={detail} />}
    </div>
  );
}

function AttemptDetail({ api, detail }: { api: ControlApi; detail: SubmissionDiagnosticDetail }) {
  const [artifactContents, setArtifactContents] = useState<Record<string, string>>({});
  const [artifactLoading, setArtifactLoading] = useState<Record<string, boolean>>({});
  const [artifactErrors, setArtifactErrors] = useState<Record<string, string>>({});
  const artifacts = detail.failure_artifacts ?? [];

  async function loadArtifact(artifact: RememberFailureArtifactDescriptor) {
    if (artifactContents[artifact.artifact_id] !== undefined || artifactLoading[artifact.artifact_id]) return;
    setArtifactLoading((current) => ({ ...current, [artifact.artifact_id]: true }));
    setArtifactErrors((current) => ({ ...current, [artifact.artifact_id]: "" }));
    try {
      const content = await api.getRememberFailureArtifact(detail.team_id, detail.submission_id, artifact.artifact_id);
      setArtifactContents((current) => ({ ...current, [artifact.artifact_id]: content }));
    } catch {
      setArtifactErrors((current) => ({ ...current, [artifact.artifact_id]: "Payload expired or is unavailable." }));
    } finally {
      setArtifactLoading((current) => ({ ...current, [artifact.artifact_id]: false }));
    }
  }

  return (
    <section className="overview-panel submission-detail" aria-label="Remember attempt details">
      <SectionHeading title="Remember Attempt Detail" actions={<span className={submissionStateClass(detail.processing_state)}>{stateLabel(detail.processing_state)}</span>} />
      <div className="submission-facts">
        <Fact label="Attempt" value={detail.submission_id} code />
        <Fact label="Correlation" value={detail.correlation_id || "Not recorded"} code={Boolean(detail.correlation_id)} />
        <Fact label="Handled" value={formatDate(detail.completed_at ?? detail.created_at)} />
        {detail.historical && <Fact label="Origin" value="Migrated history" />}
        <Fact label="Duration" value={diagnosticMetric(detail.historical, detail.duration_ms, " ms")} />
        <Fact label="Evidence / documents" value={detail.evidence_count + " / " + diagnosticMetric(detail.historical, detail.document_count)} />
        <Fact label="Assessor turns" value={diagnosticMetric(detail.historical, detail.assessor_turns)} />
        {detail.failed_phase && <Fact label="Failed phase" value={detail.failed_phase} />}
      </div>
      {detail.errors.map((item) => <article className="submission-guidance" key={item.code}><strong>{item.code}</strong><p>{item.message}</p><p className="submission-remediation">{item.remediation}</p></article>)}
      <div className="submission-detail-grid">
        <section><h3>Evidence results</h3><div className="mini-table">
          {detail.evidence.map((item) => <div className="mini-table-row" key={item.evidence_index}><span>{item.evidence_index}</span><span>{item.disposition}</span><span>{item.reason || item.search_state}</span></div>)}
        </div></section>
        <section><h3>Chronological events</h3>
          {detail.events.length === 0 ? <div className="table-placeholder compact">Event history unavailable for this attempt.</div> : (
            <ol className="submission-timeline">{detail.events.map((event) => <li key={event.sequence_no}><span className="timeline-marker" aria-hidden="true"><Clock3 size={13} /></span><div><strong>{stateLabel(event.event_kind)}</strong><small>{formatDate(event.created_at)}</small><p>{event.phase}{event.outcome ? " · " + event.outcome : ""}</p></div></li>)}</ol>
          )}
        </section>
      </div>
      {artifacts.length > 0 && <section className="submission-artifacts"><h3>Failure payloads</h3>
        {artifacts.map((artifact) => <article key={artifact.artifact_id} className="submission-artifact">
          <div className="submission-artifact-heading"><strong>{artifact.artifact_kind}</strong><small>{artifact.byte_count} bytes · expires {formatDate(artifact.expires_at)}</small></div>
          {artifactContents[artifact.artifact_id] !== undefined ? <pre>{artifactContents[artifact.artifact_id]}</pre> : <button className="ghost-button" type="button" onClick={() => void loadArtifact(artifact)} disabled={artifactLoading[artifact.artifact_id]}>{artifactLoading[artifact.artifact_id] ? "Loading payload…" : "Load payload"}</button>}
          {artifactErrors[artifact.artifact_id] && <p className="form-meta error-text">{artifactErrors[artifact.artifact_id]}</p>}
        </article>)}
      </section>}
    </section>
  );
}

function Fact({ label, value, code = false }: { label: string; value: string; code?: boolean }) {
  return <div><span>{label}</span>{code ? <code>{value}</code> : <strong>{value}</strong>}</div>;
}

function stateLabel(value: string): string {
  return value ? value.replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toUpperCase()) : "Unknown";
}

function submissionStateClass(state: string): string {
  return state === "completed" || state === "replayed" ? "status-pill success" : state === "rejected" || state === "quarantined" || state === "failed" ? "status-pill error" : "status-pill";
}

function diagnosticMetric(historical: boolean, value: number, suffix = ""): string {
  return historical ? "Not recorded" : value + suffix;
}
