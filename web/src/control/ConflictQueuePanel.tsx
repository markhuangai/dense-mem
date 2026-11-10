import { useUnsavedChanges, useViewTransition } from "../ui/navigation";
import { Button } from "../ui/primitives/button";
import { Label } from "../ui/primitives/label";
import { NativeSelect, NativeSelectOption } from "../ui/primitives/native-select";
import { Textarea } from "../ui/primitives/textarea";
import { AlertTriangle, RefreshCw, ShieldAlert } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { ApiError, ControlApi, Team } from "../api";
import type { EvidenceConflict, EvidenceConflictDetail } from "../evidence-conflict-api-types";
import { FieldRow, LoadingState, SectionHeading } from "../ui/components";
import { formatDate, readError, shortId } from "./utils";
import "./ConflictQueuePanel.css";

export function ConflictQueuePanel({ api, team }: { api: ControlApi; team: Team }) {
  const [refreshNonce,setRefreshNonce]=useState(0);
  return <section className="team-embedded-panel conflict-queue-panel" aria-label="Evidence conflict workspace">
    <SectionHeading title="Evidence conflicts" subtitle="Review cited Evidence and preserve its history" actions={<Button variant="ghost" size="icon" type="button" aria-label="Refresh evidence conflicts" onClick={()=>setRefreshNonce(value=>value+1)}><RefreshCw size={16} aria-hidden="true"/></Button>}/>
    <EvidenceConflictView api={api} team={team} refreshNonce={refreshNonce}/>
  </section>;
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

function QueueErrorState({ status, message }: { status: number; message: string }) {
  if (status === 401 || status === 403) {
    return <div className="queue-state error-state" role="alert"><ShieldAlert size={22} aria-hidden="true" /><strong>Evidence conflict access is not authorized.</strong><span>Use a control credential with access to this team.</span></div>;
  }
  if (status === 404) {
    return <div className="queue-state error-state" role="alert"><AlertTriangle size={22} aria-hidden="true" /><strong>Team not found.</strong><span>This team may have been removed or is not available to this operator.</span></div>;
  }
  return <div className="queue-state error-state" role="alert"><AlertTriangle size={22} aria-hidden="true" /><strong>Evidence conflicts unavailable.</strong><span>{message || "Try refreshing after the service recovers."}</span></div>;
}
