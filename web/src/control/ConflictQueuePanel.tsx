import { AlertTriangle, Clock3, RefreshCw, ShieldAlert } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { ApiError, ConflictQueueItem, ConflictQueuePage, ControlApi, Team } from "../api";
import { LoadingState, SectionHeading, SummaryCard } from "../ui/components";
import { formatDate, formatCount, readError, shortId } from "./utils";
import "./ConflictQueuePanel.css";

type QueueFilter = "" | "open" | "overdue";

export function ConflictQueuePanel({ api, team }: { api: ControlApi; team: Team }) {
  const [filter, setFilter] = useState<QueueFilter>("");
  const [pageSize, setPageSize] = useState(25);
  const [cursorHistory, setCursorHistory] = useState<string[]>([]);
  const [page, setPage] = useState<ConflictQueuePage | null>(null);
  const [queueError, setQueueError] = useState<unknown>(null);
  const [telemetryError, setTelemetryError] = useState<unknown>(null);
  const [telemetryDegraded, setTelemetryDegraded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [refreshNonce, setRefreshNonce] = useState(0);
  const requestSequence = useRef(0);
  const currentCursor = cursorHistory[cursorHistory.length - 1] ?? "";

  useEffect(() => {
    const requestId = ++requestSequence.current;
    let active = true;
    setLoading(true);
    setQueueError(null);
    setTelemetryError(null);
    setTelemetryDegraded(false);

    Promise.allSettled([
      api.getConflictQueue(team.id, { status: filter, limit: pageSize, cursor: currentCursor }),
      api.getTelemetry({ window: "1h", scope: "system" }),
    ]).then(([queueResult, telemetryResult]) => {
      if (!active || requestId !== requestSequence.current) {
        return;
      }
      if (queueResult.status === "fulfilled") {
        setPage(queueResult.value);
      } else {
        setQueueError(queueResult.reason);
        setPage(null);
      }
      if (telemetryResult.status === "fulfilled") {
        const snapshot = telemetryResult.value;
        const card = snapshot.current_cards?.find((item) => item.id === "conflict_queue_collection_success");
        if (!snapshot.available || !card || !card.available) {
          setTelemetryError(new Error("Conflict queue collection telemetry is unavailable."));
        } else {
          setTelemetryDegraded(card.value < 1);
        }
      } else {
        setTelemetryError(telemetryResult.reason);
      }
    }).finally(() => {
      if (active && requestId === requestSequence.current) {
        setLoading(false);
      }
    });

    return () => {
      active = false;
    };
  }, [api, team.id, filter, pageSize, currentCursor, refreshNonce]);

  function changeFilter(value: QueueFilter) {
    setFilter(value);
    setCursorHistory([]);
  }

  function changePageSize(value: number) {
    setPageSize(value);
    setCursorHistory([]);
  }

  const summary = page?.summary;
  const oldestActive = Math.max(summary?.oldest_open_age_seconds ?? 0, summary?.oldest_overdue_age_seconds ?? 0);
  const queueErrorStatus = queueError instanceof ApiError ? queueError.status : 0;

  return (
    <section className="team-embedded-panel conflict-queue-panel" aria-label="Conflict queue">
      <SectionHeading
        title="Conflict queue"
        subtitle={summary ? `Snapshot ${formatDate(summary.collected_at)}` : "Read-only review workspace"}
        actions={(
          <button
            className="icon-button"
            type="button"
            aria-label="Refresh conflict queue"
            onClick={() => setRefreshNonce((value) => value + 1)}
            disabled={loading}
          >
            <RefreshCw size={16} aria-hidden="true" />
          </button>
        )}
      />

      {Boolean(telemetryError) && <div className="banner warning" role="status">Queue telemetry is unavailable; queue data may still be current.</div>}
      {telemetryDegraded && <div className="banner warning" role="status"><ShieldAlert size={15} aria-hidden="true" /> Queue collector is degraded. Gauge values are suppressed until the next successful scrape.</div>}

      {summary && (
        <div className="summary-strip conflict-summary" aria-label="Conflict queue summary">
          <SummaryCard label="Open" value={formatCount(summary.open_count)} detail={`${formatCount(summary.active_lease_count)} active leases`} />
          <SummaryCard label="Overdue" value={formatCount(summary.overdue_count)} detail={`${formatCount(summary.expired_lease_count)} expired leases`} tone={summary.overdue_count > 0 ? "warning" : "neutral"} />
          <SummaryCard label="Oldest active" value={formatAge(oldestActive)} detail="Since case creation" />
          <SummaryCard label="Assessment failures" value={formatCount(summary.failed_assessment_count_24h)} detail="Last 24 hours" tone={summary.failed_assessment_count_24h > 0 ? "warning" : "neutral"} />
          <SummaryCard label="LWW resolutions" value={formatCount(summary.lww_resolution_count_24h)} detail="Last 24 hours" />
          <SummaryCard label="Failed derived tasks" value={formatCount(summary.failed_derived_task_count)} detail={`${formatCount(summary.pending_derived_task_count)} pending`} tone={summary.failed_derived_task_count > 0 ? "danger" : "neutral"} />
        </div>
      )}

      <div className="conflict-queue-toolbar" aria-label="Conflict queue controls">
        <label htmlFor="conflict-queue-filter">Show</label>
        <select id="conflict-queue-filter" value={filter} onChange={(event) => changeFilter(event.target.value as QueueFilter)}>
          <option value="">All active</option>
          <option value="open">Open</option>
          <option value="overdue">Overdue</option>
        </select>
        <label htmlFor="conflict-queue-page-size">Rows</label>
        <select id="conflict-queue-page-size" value={pageSize} onChange={(event) => changePageSize(Number(event.target.value))}>
          <option value={25}>25</option>
          <option value={50}>50</option>
          <option value={100}>100</option>
        </select>
        <span className="conflict-queue-order"><Clock3 size={14} aria-hidden="true" /> Overdue first, then next review</span>
      </div>

      {loading && <LoadingState label="Loading conflict queue" />}
      {!loading && Boolean(queueError) && <QueueErrorState status={queueErrorStatus} message={readError(queueError)} />}
      {!loading && !queueError && page && page.items.length === 0 && <div className="table-placeholder" role="status">No active conflicts match this view.</div>}
      {!loading && !queueError && page && page.items.length > 0 && <ConflictQueueTable items={page.items} />}

      {!loading && !queueError && page && (
        <div className="conflict-queue-pagination" aria-label="Conflict queue pagination">
          <button className="ghost-button" type="button" disabled={cursorHistory.length === 0} onClick={() => setCursorHistory((current) => current.slice(0, -1))}>Previous</button>
          <span>Page {cursorHistory.length + 1}</span>
          <button className="ghost-button" type="button" disabled={!page.next_cursor} onClick={() => page.next_cursor && setCursorHistory((current) => [...current, page.next_cursor as string])}>Next</button>
        </div>
      )}
    </section>
  );
}

function ConflictQueueTable({ items }: { items: ConflictQueueItem[] }) {
  return (
    <div className="conflict-queue-table-wrap">
      <table className="conflict-queue-table">
        <caption className="visually-hidden">Active relationship conflicts</caption>
        <thead>
          <tr>
            <th scope="col">Status</th>
            <th scope="col">Question / predicate</th>
            <th scope="col">Positions and supporters</th>
            <th scope="col">Attempts</th>
            <th scope="col">Lease</th>
            <th scope="col">Failure</th>
            <th scope="col">Timestamps</th>
          </tr>
        </thead>
        <tbody>
          {items.map((item) => <ConflictQueueRow key={item.conflict_id} item={item} />)}
        </tbody>
      </table>
    </div>
  );
}

function ConflictQueueRow({ item }: { item: ConflictQueueItem }) {
  return (
    <tr>
      <td data-label="Status">
        <div className={`conflict-status-cell ${item.status}`}>
          <span className="conflict-urgency-spine" aria-hidden="true" />
          <strong>{item.status === "overdue" ? "Overdue" : "Open"}</strong>
          <span>{formatDue(item.next_review_at)}</span>
          <small>v{item.version} · {shortId(item.conflict_id)}</small>
        </div>
      </td>
      <td data-label="Question / predicate">
        <div className="conflict-question">{item.question || "Unspecified conflict question"}</div>
        <code>{item.predicate_key}</code>
      </td>
      <td data-label="Positions and supporters">
        <div className="conflict-position-list">
          {item.positions.map((position) => (
            <div className="conflict-position" key={position.position_id}>
              <div className="conflict-position-heading">
                <strong>{position.position_key}</strong>
                <span>{position.support_group_count} groups · {position.authoritative_group_count} authoritative</span>
              </div>
              <div className="conflict-supporter-list">
                {position.supporters.map((supporter) => (
                  <span className="conflict-supporter" key={`${position.position_id}-${supporter.profile_id}`}>
                    {supporter.profile_name || "Unnamed profile"} <small>{supporter.profile_id}</small>
                  </span>
                ))}
              </div>
              {position.supporters_truncated && <small className="conflict-truncation">Showing {position.supporters.length} of {position.supporter_count} supporters.</small>}
            </div>
          ))}
        </div>
      </td>
      <td data-label="Attempts"><strong>{item.attempt_count}</strong></td>
      <td data-label="Lease"><span className={`status-pill ${item.lease_state === "active" ? "warning" : "neutral"}`}>{item.lease_state}</span>{item.lease_until && <small>{formatDate(item.lease_until)}</small>}</td>
      <td data-label="Failure"><span className={item.last_failure_class === "none" ? "muted-text" : "status-pill warning"}>{item.last_failure_class}</span></td>
      <td data-label="Timestamps"><small>Created {formatDate(item.created_at)}</small><small>Updated {formatDate(item.updated_at)}</small><small>Due {formatDate(item.review_due_at)}</small></td>
    </tr>
  );
}

function QueueErrorState({ status, message }: { status: number; message: string }) {
  if (status === 401 || status === 403) {
    return <div className="queue-state error-state" role="alert"><ShieldAlert size={22} aria-hidden="true" /><strong>Conflict queue access is not authorized.</strong><span>Use a control credential with access to this team.</span></div>;
  }
  if (status === 404) {
    return <div className="queue-state error-state" role="alert"><AlertTriangle size={22} aria-hidden="true" /><strong>Team queue not found.</strong><span>This team may have been removed or is not available to this operator.</span></div>;
  }
  return <div className="queue-state error-state" role="alert"><AlertTriangle size={22} aria-hidden="true" /><strong>Conflict queue unavailable.</strong><span>{message || "Try refreshing after the service recovers."}</span></div>;
}

function formatDue(value: string): string {
  const diff = new Date(value).getTime() - Date.now();
  if (!Number.isFinite(diff)) {
    return "Review time unavailable";
  }
  if (diff <= 0) {
    return "Review due now";
  }
  return `Review in ${formatAge(Math.round(diff / 1000))}`;
}

function formatAge(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) {
    return "0m";
  }
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (days > 0) {
    return `${days}d ${hours}h`;
  }
  if (hours > 0) {
    return `${hours}h ${minutes}m`;
  }
  return `${Math.max(1, minutes)}m`;
}
