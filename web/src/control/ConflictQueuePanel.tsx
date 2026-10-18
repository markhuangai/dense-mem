import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/primitives/tabs";
import { useUnsavedChanges, useViewTransition } from "../ui/navigation";
import { Button } from "../ui/primitives/button";
import { Label } from "../ui/primitives/label";
import { NativeSelect, NativeSelectOption } from "../ui/primitives/native-select";
import { Textarea } from "../ui/primitives/textarea";
import { Table, TableCaption, TableHeader, TableRow, TableHead, TableBody, TableCell } from "../ui/primitives/table";
import { AlertTriangle, Clock3, RefreshCw, ShieldAlert } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { ApiError, ConflictQueueItem, ConflictQueuePage, ControlApi, Team } from "../api";
import type { EvidenceConflict, EvidenceConflictDetail } from "../evidence-conflict-api-types";
import { FieldRow, LoadingState, SectionHeading, SummaryCard } from "../ui/components";
import { formatDate, formatCount, readError, shortId } from "./utils";
import "./ConflictQueuePanel.css";

type QueueFilter = "" | "open" | "overdue";

export function ConflictQueuePanel({ api, team }: { api: ControlApi; team: Team }) {
  const transition = useViewTransition();
  const [view, setView] = useState<"relationships" | "evidence">("relationships");
  const [filter, setFilter] = useState<QueueFilter>("");
  const [pageSize, setPageSize] = useState(25);
  const [cursorHistory, setCursorHistory] = useState<string[]>([]);
  const [page, setPage] = useState<ConflictQueuePage | null>(null);
  const [queueError, setQueueError] = useState<unknown>(null);
  const [telemetryError, setTelemetryError] = useState<unknown>(null);
  const [telemetryDegraded, setTelemetryDegraded] = useState(false);
  const [loading, setLoading] = useState(true);
  const [refreshNonce, setRefreshNonce] = useState(0);
  const [loadedTeamID, setLoadedTeamID] = useState<string | null>(null);
  const requestSequence = useRef(0);
  const lastTeamID = useRef(team.id);
  const currentCursor = cursorHistory[cursorHistory.length - 1] ?? "";

  useEffect(() => {
    if (view === "evidence") {
      return;
    }
    if (lastTeamID.current !== team.id) {
      lastTeamID.current = team.id;
      if (cursorHistory.length > 0) {
        setCursorHistory([]);
        return;
      }
    }
    const requestId = ++requestSequence.current;
    let active = true;
    setLoading(true);
    setQueueError(null);
    setTelemetryError(null);
    setTelemetryDegraded(false);

    api.getConflictQueue(team.id, { status: filter, limit: pageSize, cursor: currentCursor }).then((value) => {
      if (!active || requestId !== requestSequence.current) {
        return;
      }
      setPage(value);
      setLoadedTeamID(team.id);
    }).catch((reason) => {
      if (!active || requestId !== requestSequence.current) {
        return;
      }
      setQueueError(reason);
      setPage(null);
      setLoadedTeamID(null);
    }).finally(() => {
      if (active && requestId === requestSequence.current) {
        setLoading(false);
      }
    });

    api.getTelemetry({ window: "1h", scope: "system" }).then((snapshot) => {
      if (!active || requestId !== requestSequence.current) {
        return;
      }
      const card = snapshot.current_cards?.find((item) => item.id === "conflict_queue_collection_success");
      if (!snapshot.available || !card || !card.available) {
        setTelemetryError(new Error("Conflict queue collection telemetry is unavailable."));
      } else {
        setTelemetryDegraded(card.value < 1);
      }
    }).catch((reason) => {
      if (active && requestId === requestSequence.current) {
        setTelemetryError(reason);
      }
    });

    return () => {
      active = false;
    };
  }, [api, team.id, filter, pageSize, currentCursor, refreshNonce, view]);

  function changeFilter(value: QueueFilter) {
    setFilter(value);
    setCursorHistory([]);
  }

  function changePageSize(value: number) {
    setPageSize(value);
    setCursorHistory([]);
  }

  const visiblePage = loadedTeamID === team.id ? page : null;
  const summary = visiblePage?.summary;
  const oldestActive = Math.max(summary?.oldest_open_age_seconds ?? 0, summary?.oldest_overdue_age_seconds ?? 0);
  const queueErrorStatus = queueError instanceof ApiError ? queueError.status : 0;

  return (
    <section className="team-embedded-panel conflict-queue-panel" aria-label="Conflict queue">
      <SectionHeading
        title="Conflict queue"
        subtitle={summary ? `Snapshot ${formatDate(summary.collected_at)}` : "Read-only review workspace"}
        actions={(
          <Button variant="ghost" size="icon"
            className="icon-button"
            type="button"
            aria-label="Refresh conflict queue"
            onClick={() => setRefreshNonce((value) => value + 1)}
            disabled={loading}
          >
            <RefreshCw size={16} aria-hidden="true" />
          </Button>
        )}
      />

      <Tabs className="panel-tabs" value={view} onValueChange={(value) => void transition(() => setView(value as "relationships" | "evidence"))}>
        <TabsList aria-label="Conflict type"><TabsTrigger value="relationships">Relationships</TabsTrigger><TabsTrigger value="evidence">Evidence</TabsTrigger></TabsList>
        <TabsContent value="evidence"><EvidenceConflictView api={api} team={team} refreshNonce={refreshNonce} /></TabsContent>
        <TabsContent value="relationships">
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
        <Label htmlFor="conflict-queue-filter">Show</Label>
        <NativeSelect id="conflict-queue-filter" value={filter} onChange={(event) => changeFilter(event.target.value as QueueFilter)}>
          <NativeSelectOption value="">All active</NativeSelectOption>
          <NativeSelectOption value="open">Open</NativeSelectOption>
          <NativeSelectOption value="overdue">Overdue</NativeSelectOption>
        </NativeSelect>
        <Label htmlFor="conflict-queue-page-size">Rows</Label>
        <NativeSelect id="conflict-queue-page-size" value={pageSize} onChange={(event) => changePageSize(Number(event.target.value))}>
          <NativeSelectOption value={25}>25</NativeSelectOption>
          <NativeSelectOption value={50}>50</NativeSelectOption>
          <NativeSelectOption value={100}>100</NativeSelectOption>
        </NativeSelect>
        <span className="conflict-queue-order"><Clock3 size={14} aria-hidden="true" /> Overdue first, then next review</span>
      </div>

      {loading && <LoadingState label="Loading conflict queue" />}
      {!loading && Boolean(queueError) && <QueueErrorState status={queueErrorStatus} message={readError(queueError)} />}
      {!loading && !queueError && visiblePage && visiblePage.items.length === 0 && <div className="table-placeholder" role="status">No active conflicts match this view.</div>}
      {!loading && !queueError && visiblePage && visiblePage.items.length > 0 && <ConflictQueueTable items={visiblePage.items} />}

      {!loading && !queueError && visiblePage && (
        <div className="conflict-queue-pagination" aria-label="Conflict queue pagination">
          <Button variant="outline" className="ghost-button" type="button" disabled={cursorHistory.length === 0} onClick={() => setCursorHistory((current) => current.slice(0, -1))}>Previous</Button>
          <span>Page {cursorHistory.length + 1}</span>
          <Button variant="outline" className="ghost-button" type="button" disabled={!visiblePage.next_cursor} onClick={() => visiblePage.next_cursor && setCursorHistory((current) => [...current, visiblePage.next_cursor as string])}>Next</Button>
        </div>
      )}
      </TabsContent></Tabs>
    </section>
  );
}

function EvidenceConflictView({ api, team, refreshNonce }: { api: ControlApi; team: Team; refreshNonce: number }) {
  const transition = useViewTransition();
  const [items, setItems] = useState<EvidenceConflict[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [selected, setSelected] = useState<EvidenceConflictDetail | null>(null);
  const [status, setStatus] = useState<"open" | "resolved" | "dismissed">("open");
  const [cursorHistory, setCursorHistory] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [eventError, setEventError] = useState<unknown>(null);
  const [eventLoading, setEventLoading] = useState(false);
  const [reason, setReason] = useState("");
  useUnsavedChanges(Boolean(reason));
  const [preferred, setPreferred] = useState("");
  const [saving, setSaving] = useState(false);
  const [stale, setStale] = useState(false);
  const [listRefreshNonce, setListRefreshNonce] = useState(0);
  const [loadedTeamID, setLoadedTeamID] = useState<string | null>(null);
  const [selectedTeamID, setSelectedTeamID] = useState<string | null>(null);
  const requestSequence = useRef(0);
  const detailRequestSequence = useRef(0);
  const lastTeamID = useRef(team.id);
  const teamEpoch = useRef(0);
  const skipResetCursorFetch = useRef(false);
  const cursor = cursorHistory[cursorHistory.length - 1] ?? "";

  useEffect(() => {
    const teamChanged = lastTeamID.current !== team.id;
    if (teamChanged) {
      lastTeamID.current = team.id;
      teamEpoch.current++;
      requestSequence.current++;
      detailRequestSequence.current++;
      if (cursorHistory.length > 0) {
        skipResetCursorFetch.current = true;
        setCursorHistory([]);
      }
      setItems([]);
      setNextCursor(null);
      setSelected(null);
      setLoadedTeamID(null);
      setSelectedTeamID(null);
      setReason("");
      setPreferred("");
      setStale(false);
    }
    if (!teamChanged && skipResetCursorFetch.current) {
      skipResetCursorFetch.current = false;
      return;
    }
    const requestID = ++requestSequence.current;
    const requestTeamID = team.id;
    const requestCursor = teamChanged ? "" : cursor;
    setLoading(true);
    setError(null);
    api.listEvidenceConflicts(requestTeamID, { status, limit: 25, cursor: requestCursor }).then((page) => {
      if (requestID !== requestSequence.current) return;
      setItems(page.items);
      setNextCursor(page.next_cursor);
      setLoadedTeamID(requestTeamID);
    }).catch((nextError) => {
      if (requestID === requestSequence.current) {
        setError(nextError);
        setItems([]);
        setNextCursor(null);
        setLoadedTeamID(null);
      }
    }).finally(() => {
      if (requestID === requestSequence.current) setLoading(false);
    });
  }, [api, team.id, status, cursor, cursorHistory.length, refreshNonce, listRefreshNonce]);

  async function openConflict(item: EvidenceConflict) {
    const requestTeamID = team.id;
    const requestEpoch = teamEpoch.current;
    const requestID = ++detailRequestSequence.current;
    setStale(false);
    try {
      const detail = await api.getEvidenceConflict(requestTeamID, item.conflict_id, 50);
      if (requestEpoch !== teamEpoch.current || requestID !== detailRequestSequence.current) return;
      setSelected(detail);
      setSelectedTeamID(requestTeamID);
      setError(null);
      setReason("");
      setPreferred(detail.conflict.preferred_position_id ?? "");
      setStale(false);
      setEventError(null);
    } catch (nextError) {
      if (requestEpoch === teamEpoch.current && requestID === detailRequestSequence.current) {
        setError(nextError);
      }
    }
  }

  async function loadMoreEvents() {
    if (!selected?.next_event_cursor || eventLoading) return;
    const conflictID = selected.conflict.conflict_id;
    const requestTeamID = team.id;
    const requestEpoch = teamEpoch.current;
    const requestID = detailRequestSequence.current;
    setEventLoading(true);
    setEventError(null);
    try {
      const next = await api.getEvidenceConflict(requestTeamID, conflictID, 50, selected.next_event_cursor);
      if (requestEpoch !== teamEpoch.current || requestID !== detailRequestSequence.current) return;
      setSelected((current) => {
        if (!current || current.conflict.conflict_id !== conflictID) return current;
        return {
          ...next,
          conflict: {
            ...next.conflict,
            events: [...(current.conflict.events ?? []), ...(next.conflict.events ?? [])],
          },
        };
      });
    } catch (nextError) {
      if (requestEpoch === teamEpoch.current && requestID === detailRequestSequence.current) {
        setEventError(nextError);
      }
    } finally {
      setEventLoading(false);
    }
  }

  async function resolve(decision: "resolve" | "dismiss") {
    if (!selected || reason.trim() === "") return;
    const requestTeamID = team.id;
    const requestEpoch = teamEpoch.current;
    const requestID = detailRequestSequence.current;
    const conflictID = selected.conflict.conflict_id;
    setSaving(true);
    try {
      await api.resolveEvidenceConflict(requestTeamID, conflictID, {
        expected_version: selected.conflict.version,
        decision,
        reason: reason.trim(),
        ...(decision === "resolve" && preferred ? { preferred_position_id: preferred } : {}),
      });
      if (requestEpoch !== teamEpoch.current || requestID !== detailRequestSequence.current) return;
      setStale(false);
      setReason("");
      setSelected(null);
      setSelectedTeamID(null);
      setCursorHistory([]);
      setListRefreshNonce((value) => value + 1);
    } catch (nextError) {
      if (requestEpoch !== teamEpoch.current || requestID !== detailRequestSequence.current) return;
      setStale(nextError instanceof ApiError && nextError.status === 409);
      setError(nextError);
      if (nextError instanceof ApiError && nextError.status === 409) {
        try {
          const latest = await api.getEvidenceConflict(requestTeamID, conflictID, 50);
          if (requestEpoch !== teamEpoch.current || requestID !== detailRequestSequence.current) return;
          setSelected(latest);
          setSelectedTeamID(requestTeamID);
          setError(null);
          setPreferred((current) => latest.conflict.positions.some((position) => position.position_id === current) ? current : "");
        } catch { /* preserve the stale banner and form input */ }
      }
    } finally {
      setSaving(false);
    }
  }

  const visibleItems = loadedTeamID === team.id ? items : [];
  const visibleSelected = selectedTeamID === team.id ? selected : null;

  return <div className="evidence-conflict-view" aria-label="Evidence conflicts">
    <div className="conflict-queue-toolbar">
      <Label htmlFor="evidence-conflict-status">Show</Label>
      <NativeSelect id="evidence-conflict-status" value={status} onChange={(event) => { detailRequestSequence.current++; setStale(false); setStatus(event.target.value as typeof status); setCursorHistory([]); setSelected(null); setSelectedTeamID(null); setNextCursor(null); }}>
        <NativeSelectOption value="open">Open</NativeSelectOption><NativeSelectOption value="resolved">Resolved</NativeSelectOption><NativeSelectOption value="dismissed">Dismissed</NativeSelectOption>
      </NativeSelect>
    </div>
    {stale && <div className="banner warning" role="status">This conflict changed in another review. The latest version is loaded; your reason remains.</div>}
    {loading && <LoadingState label="Loading evidence conflicts" />}
    {!loading && Boolean(error) && <QueueErrorState status={error instanceof ApiError ? error.status : 0} message={readError(error)} />}
    {!loading && !error && visibleItems.length === 0 && <div className="table-placeholder" role="status">No evidence conflicts match this view.</div>}
    {!loading && visibleItems.length > 0 && <div className="evidence-conflict-list">
      {visibleItems.map((item) => <Button variant="outline" type="button" className={`evidence-conflict-card ${visibleSelected?.conflict.conflict_id === item.conflict_id ? "selected" : ""}`} key={item.conflict_id} onClick={() => void transition(() => openConflict(item))}>
        <span className="status-pill neutral">{item.status} · v{item.version}</span>
        <strong>{item.positions.length} cited positions</strong>
        <small>{formatDate(item.updated_at)} · {shortId(item.conflict_id)}</small>
      </Button>)}
    </div>}
    {!loading && !error && <div className="conflict-queue-pagination" aria-label="Evidence conflict page navigation">
      <Button variant="outline" className="ghost-button" type="button" disabled={cursorHistory.length === 0} onClick={() => setCursorHistory((current) => current.slice(0, -1))}>Previous</Button>
      <span>Page {cursorHistory.length + 1}</span>
      <Button variant="outline" className="ghost-button" type="button" disabled={!nextCursor} onClick={() => nextCursor && setCursorHistory((current) => [...current, nextCursor])}>Next</Button>
    </div>}
    {visibleSelected && <div className="evidence-conflict-detail">
      <div className="evidence-conflict-detail-heading"><div><span className="status-pill neutral">{visibleSelected.conflict.status} · v{visibleSelected.conflict.version}</span><h3>Cited evidence conflict</h3></div><Button variant="outline" className="ghost-button" type="button" onClick={() => void transition(() => { detailRequestSequence.current++; setStale(false); setSelected(null); setSelectedTeamID(null); setReason(""); })}>Close</Button></div>
      <div className="evidence-position-grid">{visibleSelected.conflict.positions.map((position) => <article className="evidence-position-card" key={position.position_id}><span className="status-pill neutral">{position.authority}{position.submitted ? " · submitted" : " · existing"}</span><p>{position.quote}</p><code>{position.evidence_id} · {position.occurrence_id}</code><small>Runes {position.span_start}–{position.span_end}</small></article>)}</div>
      <Label htmlFor="evidence-conflict-reason">Review reason</Label>
      <Textarea id="evidence-conflict-reason" value={reason} maxLength={512} onChange={(event) => setReason(event.target.value)} placeholder="Explain the review decision" />
      {visibleSelected.conflict.status === "open" && <>
        <FieldRow label="Preferred position (optional)" htmlFor="evidence-conflict-preferred">
          <NativeSelect id="evidence-conflict-preferred" value={preferred} onChange={(event) => setPreferred(event.target.value)}><NativeSelectOption value="">No preferred position</NativeSelectOption>{visibleSelected.conflict.positions.map((position) => <NativeSelectOption key={position.position_id} value={position.position_id}>{position.quote}</NativeSelectOption>)}</NativeSelect>
        </FieldRow>
        <div className="evidence-conflict-actions"><Button variant="default" className="primary-button" type="button" disabled={saving || reason.trim() === ""} onClick={() => resolve("resolve")}>Resolve</Button><Button variant="outline" className="ghost-button" type="button" disabled={saving || reason.trim() === "" || preferred !== ""} onClick={() => resolve("dismiss")}>Dismiss</Button></div>
      </>}
      {visibleSelected.conflict.events && visibleSelected.conflict.events.length > 0 && <details><summary>Review history ({visibleSelected.conflict.events.length})</summary><div className="evidence-conflict-history">{visibleSelected.conflict.events.map((event) => <div key={event.event_id}><strong>{event.action} · v{event.case_version}</strong><small>{formatDate(event.created_at)}{event.reason ? ` · ${event.reason}` : ""}</small></div>)}{Boolean(eventError) && <div className="banner warning" role="status">{readError(eventError)}</div>}{visibleSelected.next_event_cursor && <Button variant="outline" className="ghost-button" type="button" disabled={eventLoading} onClick={() => void loadMoreEvents()}>{eventLoading ? "Loading history…" : "Load older history"}</Button>}</div></details>}
    </div>}
  </div>;
}

function ConflictQueueTable({ items }: { items: ConflictQueueItem[] }) {
  return (
    <div className="conflict-queue-table-wrap">
      <Table className="conflict-queue-table">
        <TableCaption className="visually-hidden">Active relationship conflicts</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead scope="col">Status</TableHead>
            <TableHead scope="col">Question / predicate</TableHead>
            <TableHead scope="col">Positions and supporters</TableHead>
            <TableHead scope="col">Attempts</TableHead>
            <TableHead scope="col">Lease</TableHead>
            <TableHead scope="col">Failure</TableHead>
            <TableHead scope="col">Timestamps</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((item) => <ConflictQueueRow key={item.conflict_id} item={item} />)}
        </TableBody>
      </Table>
    </div>
  );
}

function ConflictQueueRow({ item }: { item: ConflictQueueItem }) {
  return (
    <TableRow>
      <TableCell data-label="Status">
        <div className={`conflict-status-cell ${item.status}`}>
          <span className="conflict-urgency-spine" aria-hidden="true" />
          <strong>{item.status === "overdue" ? "Overdue" : "Open"}</strong>
          <span>{formatDue(item.next_review_at)}</span>
          <small>v{item.version} · {shortId(item.conflict_id)}</small>
        </div>
      </TableCell>
      <TableCell data-label="Question / predicate">
        <div className="conflict-question">{item.question || "Unspecified conflict question"}{item.question_truncated && <small className="conflict-truncation">Question truncated.</small>}</div>
        <code>{item.predicate_key}</code>{item.predicate_key_truncated && <small className="conflict-truncation">Predicate truncated.</small>}
      </TableCell>
      <TableCell data-label="Positions and supporters">
        <div className="conflict-position-list">
          {item.positions.map((position) => (
            <div className="conflict-position" key={position.position_id}>
              <div className="conflict-position-heading">
                <strong>{position.position_key}</strong>
                <span>{position.supporter_count} supporters</span>
              </div>
              <div className="conflict-supporter-list">
                {position.supporters.map((supporter) => (
                  <span className="conflict-supporter" key={`${position.position_id}-${supporter.profile_id}`}>
					{supporter.profile_name || "Unnamed owner"} <small>{shortId(supporter.profile_id)}</small>
                  </span>
                ))}
              </div>
              {position.supporters_truncated && <small className="conflict-truncation">Showing {position.supporters.length} of {position.supporter_count} supporters.</small>}
            </div>
          ))}
          {item.positions_truncated && <small className="conflict-truncation">Some positions are not shown.</small>}
        </div>
      </TableCell>
      <TableCell data-label="Attempts"><strong>{item.attempt_count}</strong></TableCell>
      <TableCell data-label="Lease"><span className={`status-pill ${item.lease_state === "active" ? "warning" : "neutral"}`}>{item.lease_state}</span>{item.lease_until && <small>{formatDate(item.lease_until)}</small>}</TableCell>
      <TableCell data-label="Failure"><span className={item.last_failure_class === "none" ? "muted-text" : "status-pill warning"}>{item.last_failure_class}</span></TableCell>
      <TableCell data-label="Timestamps"><small>Created {formatDate(item.created_at)}</small><small>Updated {formatDate(item.updated_at)}</small><small>Due {formatDate(item.review_due_at)}</small></TableCell>
    </TableRow>
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
