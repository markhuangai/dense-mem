import { CSSProperties, useEffect, useRef, useState } from "react";
import { AlertTriangle, ArrowRight, Clock3, RefreshCw } from "lucide-react";
import {
  ControlApi,
  OperationLog,
  SubmissionDiagnosticDetail,
  SubmissionOperatorDiagnostic,
  SubmissionDiagnosticSummary,
  Team,
} from "../api";
import { LoadingState, SectionHeading } from "../ui/components";
import { formatDate, readError, shortId } from "./utils";

const PROCESSING_STATES = ["", "queued", "processing", "completed", "quarantined", "failed"];
const PAGE_SIZE = 50;

export function SubmissionsPanel({ api, team }: { api: ControlApi; team: Team }) {
  const [items, setItems] = useState<SubmissionDiagnosticSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [state, setState] = useState("");
  const [offset, setOffset] = useState(0);
  const [selectedID, setSelectedID] = useState("");
  const [detail, setDetail] = useState<SubmissionDiagnosticDetail | null>(null);
  const [timeline, setTimeline] = useState<OperationLog[]>([]);
  const [timelineUnavailable, setTimelineUnavailable] = useState(false);
  const [loading, setLoading] = useState(false);
  const [detailLoading, setDetailLoading] = useState(false);
  const [error, setError] = useState("");
  const listRequestRef = useRef(0);
  const detailRequestRef = useRef(0);

  async function loadSubmissions(nextState = state, nextOffset = offset) {
    const requestID = ++listRequestRef.current;
    setLoading(true);
    setError("");
    try {
      const page = await api.listSubmissionDiagnostics({
        team_id: team.id,
        processing_state: nextState,
        limit: PAGE_SIZE,
        offset: nextOffset,
      });
      if (requestID !== listRequestRef.current) {
        return;
      }
      setItems(page.data);
      setTotal(page.pagination.total);
      setOffset(page.pagination.offset);
      const nextSelected = page.data.some((item) => item.submission_id === selectedID)
        ? selectedID
        : page.data[0]?.submission_id ?? "";
      setSelectedID(nextSelected);
      if (nextSelected) {
        void loadDetail(nextSelected);
      } else {
        detailRequestRef.current += 1;
        setDetail(null);
        setTimeline([]);
        setTimelineUnavailable(false);
      }
    } catch (caught) {
      if (requestID === listRequestRef.current) {
        setError(readError(caught));
      }
    } finally {
      if (requestID === listRequestRef.current) {
        setLoading(false);
      }
    }
  }

  async function loadDetail(submissionID: string) {
    const requestID = ++detailRequestRef.current;
    setDetailLoading(true);
    setError("");
    setDetail(null);
    setTimeline([]);
    setTimelineUnavailable(false);
    try {
      const [nextDetail, logResult] = await Promise.all([
        api.getSubmissionDiagnostic(team.id, submissionID),
        api.listOperationLogs({
          team_id: team.id,
          reference_type: "submission",
          reference_id: submissionID,
          limit: 100,
          offset: 0,
          sort: "timestamp",
          direction: "asc",
        }).then((page) => ({ page })).catch(() => ({ page: null })),
      ]);
      if (requestID !== detailRequestRef.current) {
        return;
      }
      setDetail(nextDetail);
      setTimeline(logResult.page?.data ?? []);
      setTimelineUnavailable(logResult.page === null);
    } catch (caught) {
      if (requestID === detailRequestRef.current) {
        setError(readError(caught));
      }
    } finally {
      if (requestID === detailRequestRef.current) {
        setDetailLoading(false);
      }
    }
  }

  useEffect(() => {
    setItems([]);
    setTotal(0);
    setState("");
    setOffset(0);
    setSelectedID("");
    setDetail(null);
    setTimeline([]);
    setTimelineUnavailable(false);
    setDetailLoading(false);
    void loadSubmissions("", 0);
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
          title="Memory Submissions"
          meta={total}
          actions={(
            <button className="icon-button" type="button" aria-label="Refresh submissions" onClick={() => void loadSubmissions()}>
              <RefreshCw size={16} aria-hidden="true" />
            </button>
          )}
        />
        <p className="panel-intro">Durable placement state is authoritative. The event timeline below is supplemental operational history.</p>
        {error && <div className="banner error" role="alert">{error}</div>}
        <div className="metrics-toolbar submission-toolbar">
          <label>
            Processing state
            <select
              aria-label="Processing state"
              value={state}
              onChange={(event) => {
                const next = event.target.value;
                setState(next);
                setOffset(0);
                setItems([]);
                setTotal(0);
                setSelectedID("");
                detailRequestRef.current += 1;
                setDetail(null);
                setTimeline([]);
                setTimelineUnavailable(false);
                setDetailLoading(false);
                void loadSubmissions(next, 0);
              }}
            >
              {PROCESSING_STATES.map((value) => (
                <option value={value} key={value}>{value ? stateLabel(value) : "All states"}</option>
              ))}
            </select>
          </label>
        </div>
        {loading && items.length === 0 ? (
          <LoadingState label="Loading submissions" />
        ) : items.length === 0 ? (
          <div className="table-placeholder">No submissions match this state.</div>
        ) : (
          <div className="table-wrap">
            <table className="data-table submissions-table">
              <thead>
                <tr>
                  <th>Submitted</th>
                  <th>About</th>
                  <th>State</th>
                  <th>Attempts</th>
                  <th>Evidence</th>
                  <th>Correlation</th>
                  <th>Action</th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => (
                  <tr key={item.submission_id} className={item.submission_id === selectedID ? "selected-row" : undefined}>
                    <td>
                      <strong>{formatDate(item.submitted_at)}</strong>
                      <small className="table-subline">{shortId(item.submission_id)}</small>
                    </td>
                    <td>
                      <span className="submission-source-summary">{item.source_summary || "Unlabelled submission"}</span>
                      {item.source_summary_truncated && <small className="table-subline">Summary truncated</small>}
                    </td>
                    <td>
                      <span className={submissionStateClass(item.processing_state)}>{stateLabel(item.processing_state)}</span>
                      {item.next_attempt_at && <small className="table-subline">Retry {formatDate(item.next_attempt_at)}</small>}
                    </td>
                    <td>{item.attempts} / {item.max_attempts}</td>
                    <td>{item.evidence_count}</td>
                    <td><code>{item.correlation_id ? shortId(item.correlation_id) : "—"}</code></td>
                    <td>
                      <button
                        className="text-button"
                        type="button"
                        aria-label={`Inspect submission ${item.submission_id}`}
                        onClick={() => {
                          setSelectedID(item.submission_id);
                          void loadDetail(item.submission_id);
                        }}
                      >
                        Inspect <ArrowRight size={14} aria-hidden="true" />
                      </button>
                    </td>
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
            onClick={() => void loadSubmissions(state, Math.max(0, offset - PAGE_SIZE))}
          >
            Previous
          </button>
          <button
            className="ghost-button"
            type="button"
            disabled={loading || offset + PAGE_SIZE >= total}
            onClick={() => void loadSubmissions(state, offset + PAGE_SIZE)}
          >
            Next
          </button>
        </div>
      </section>

      {detailLoading && !detail ? <LoadingState label="Loading submission details" /> : detail && (
        <SubmissionDetail detail={detail} timeline={timeline} timelineUnavailable={timelineUnavailable} />
      )}
    </div>
  );
}

function SubmissionDetail({
  detail,
  timeline,
  timelineUnavailable,
}: {
  detail: SubmissionDiagnosticDetail;
  timeline: OperationLog[];
  timelineUnavailable: boolean;
}) {
  const operatorDiagnostics = detail.operator_diagnostics ?? [];
  return (
    <section className="overview-panel submission-detail" aria-label="Submission details">
      <SectionHeading
        title="Submission Detail"
        actions={<span className={submissionStateClass(detail.processing_state)}>{stateLabel(detail.processing_state)}</span>}
      />
      <div className="submission-facts">
        <Fact label="Submission" value={detail.submission_id} code />
        <Fact label="Correlation" value={detail.correlation_id || "Not recorded"} code={Boolean(detail.correlation_id)} />
        <Fact label="About" value={detail.source_summary || "Unlabelled submission"} />
        <Fact label="Attempts" value={`${detail.attempts ?? 0} / ${detail.max_attempts ?? "—"}`} />
        <Fact label="Search" value={stateLabel(detail.search_state)} />
        <Fact label="Submitted" value={detail.submitted_at ? formatDate(detail.submitted_at) : "Not recorded"} />
        <Fact label="Updated" value={detail.updated_at ? formatDate(detail.updated_at) : "Not recorded"} />
      </div>

      {detail.errors.map((statusError) => (
        <article className="submission-guidance" key={`${statusError.code}:${statusError.message}`}>
          <span className="submission-guidance-icon"><AlertTriangle size={18} aria-hidden="true" /></span>
          <div>
            <strong>{statusError.code}</strong>
            <p>{statusError.message}</p>
            <p className="submission-remediation">{statusError.remediation}</p>
            {statusError.resubmission_issues?.map((issue, index) => (
              <div className="submission-resubmission-issue" key={`${issue.code}:${issue.relationship_ref ?? ""}:${index}`}>
                <strong>{issue.code}</strong>
                {(issue.relationship_ref || issue.component) && (
                  <small>
                    {issue.relationship_ref ? `Relationship ${issue.relationship_ref}` : ""}
                    {issue.relationship_ref && issue.component ? " · " : ""}
                    {issue.component ? `Component ${issue.component}` : ""}
                  </small>
                )}
                <p>{issue.message}</p>
              </div>
            ))}
            {statusError.resubmission_issues_truncated && (
              <p className="submission-remediation">Additional resubmission issues are not shown.</p>
            )}
          </div>
          <span className={statusError.retryable ? "status-pill warning" : "status-pill error"}>
            {actionLabel(statusError.next_action)}
          </span>
        </article>
      ))}

      {detail.operator_diagnostic && operatorDiagnostics.length === 0 && (
        <OperatorDiagnosticBlock diagnostic={detail.operator_diagnostic} />
      )}

      {operatorDiagnostics.length > 0 && (
        <section className="submission-operator-diagnostics" aria-label="Operator diagnostics">
          <h3>Placement diagnostics</h3>
          <ol className="operator-diagnostic-list">
            {operatorDiagnostics.map((diagnostic, index) => (
              <li key={diagnostic.id ?? `${diagnostic.occurred_at ?? "diagnostic"}-${index}`}>
                <OperatorDiagnosticBlock diagnostic={diagnostic} compact />
              </li>
            ))}
          </ol>
      </section>
      )}
      <div className="submission-detail-grid">
        <section>
          <h3>Evidence placement</h3>
          <div className="mini-table">
            <div className="mini-table-row heading" style={{ "--mini-cols": 4 } as CSSProperties}>
              <span>Index</span><span>Search</span><span>Evidence ID</span><span>Error guidance</span>
            </div>
            {detail.evidence.map((evidence) => (
              <div className="mini-table-row" style={{ "--mini-cols": 4 } as CSSProperties} key={evidence.evidence_id}>
                <span>{evidence.evidence_index}</span>
                <span>{stateLabel(evidence.search_state)}</span>
                <code>{shortId(evidence.evidence_id)}</code>
                <span className="submission-evidence-error">
                  {evidence.error ? (
                    <>
                      <code>{evidence.error.code}</code>
                      <small>{evidence.error.message}</small>
                      <strong>{evidence.error.remediation}</strong>
                    </>
                  ) : "—"}
                </span>
              </div>
            ))}
          </div>
        </section>
        <section>
          <h3>Operational timeline</h3>
          {timelineUnavailable ? (
            <div className="table-placeholder compact">Operational timeline unavailable. Durable placement state remains authoritative.</div>
          ) : timeline.length === 0 ? (
            <div className="table-placeholder compact">No retained lifecycle events.</div>
          ) : (
            <ol className="submission-timeline">
              {timeline.map((event) => (
                <li key={event.id}>
                  <span className="timeline-marker" aria-hidden="true"><Clock3 size={13} /></span>
                  <div>
                    <strong>{eventLabel(event.message)}</strong>
                    <small>{formatDate(event.timestamp)}</small>
                    <TimelineDetails event={event} />
                  </div>
                </li>
              ))}
            </ol>
          )}
        </section>
      </div>
    </section>
  );
}

function OperatorDiagnosticBlock({ diagnostic, compact = false }: { diagnostic: SubmissionOperatorDiagnostic; compact?: boolean }) {
  const labels = [diagnostic.failure_stage, diagnostic.failure_class].filter(Boolean).join(" · ");
  const measurement = diagnostic.failure_measurement;
  return (
    <article className={compact ? "operator-diagnostic compact" : "operator-diagnostic"}>
      <div>
        <strong>{diagnostic.failure_reason_code || "placement_failure"}</strong>
        {labels && <small>{labels}</small>}
        {diagnostic.message && <p>{diagnostic.message}</p>}
        {diagnostic.validation_stage && <small>Validation: {diagnostic.validation_stage}</small>}
        {diagnostic.validation_field_families && diagnostic.validation_field_families.length > 0 && (
          <small>Fields: {diagnostic.validation_field_families.join(", ")}</small>
        )}
        {measurement && (
          <small>
            Measured {measurement.observed_at_least !== undefined
              ? `at least ${measurement.observed_at_least}`
              : measurement.observed ?? 0} {measurement.unit}; limit {measurement.limit}
          </small>
        )}
      </div>
      {diagnostic.occurred_at && (
        <time dateTime={diagnostic.occurred_at}>{formatDate(diagnostic.occurred_at)}</time>
      )}
    </article>
  );
}

function TimelineDetails({ event }: { event: OperationLog }) {
  const attrs = event.attrs ?? {};
  const values = [
    transitionValue(attrs.from, attrs.to),
    typeof attrs.stage === "string" ? `stage ${attrs.stage}` : "",
    typeof attrs.reason_code === "string" ? attrs.reason_code : "",
    typeof attrs.failure_stage === "string" ? `failure stage ${attrs.failure_stage}` : "",
    typeof attrs.failure_reason_code === "string" ? attrs.failure_reason_code : "",
    typeof attrs.failure_class === "string" ? `failure class ${attrs.failure_class}` : "",
    typeof attrs.validation_stage === "string" ? `validation ${attrs.validation_stage}` : "",
    typeof attrs.provider_status === "number" ? `provider status ${attrs.provider_status}` : "",
    typeof attrs.assessor_turns === "number" ? `assessor turns ${attrs.assessor_turns}` : "",
    typeof attrs.next_attempt_at === "string" ? `next ${formatDate(attrs.next_attempt_at)}` : "",
  ].filter(Boolean);
  return values.length > 0 ? <p>{values.join(" · ")}</p> : null;
}

function Fact({ label, value, code = false }: { label: string; value: string; code?: boolean }) {
  return <div><span>{label}</span>{code ? <code>{value}</code> : <strong>{value}</strong>}</div>;
}

function transitionValue(from: unknown, to: unknown): string {
  return typeof from === "string" && typeof to === "string" ? `${stateLabel(from)} → ${stateLabel(to)}` : "";
}

function eventLabel(value: string): string {
  return stateLabel(value.replace(/^submission_/, ""));
}

function stateLabel(value: string): string {
  if (!value) {
    return "Unknown";
  }
  return value.replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function actionLabel(value: string): string {
  return value === "none" ? "No action" : stateLabel(value);
}

function submissionStateClass(state: string): string {
  switch (state) {
    case "completed":
      return "status-pill success";
    case "queued":
    case "processing":
      return "status-pill";
    default:
      return "status-pill error";
  }
}
