import { Fragment, useEffect, useRef, useState } from "react";
import { Braces, RefreshCw } from "lucide-react";
import { ControlApi, RecallFeedbackEvent, RecallFeedbackEventQuery, RecallFeedbackResolvedResult, RecallFeedbackResultRef, Team } from "../api";
import { SectionHeading } from "../ui/components";
import { formatDate, readError, shortId } from "./utils";

const PAGE_SIZES = [25, 50, 100, 250, 500];
const QUALITIES = ["", "high", "medium", "low"];

export function RecallFeedbackPanel({ api, teams }: { api: ControlApi; teams: Team[] }) {
  const [events, setEvents] = useState<RecallFeedbackEvent[]>([]);
  const [total, setTotal] = useState(0);
  const [query, setQuery] = useState<RecallFeedbackEventQuery>({ limit: 100, offset: 0 });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [expandedRecallId, setExpandedRecallId] = useState("");
  const [details, setDetails] = useState<Record<string, RecallFeedbackEvent>>({});
  const [detailLoading, setDetailLoading] = useState("");
  const requestSeqRef = useRef(0);

  async function loadEvents(nextQuery = query) {
    const requestSeq = requestSeqRef.current + 1;
    requestSeqRef.current = requestSeq;
    setLoading(true);
    setError("");
    try {
      const page = await api.listRecallFeedbackEvents(nextQuery);
      if (requestSeq !== requestSeqRef.current) {
        return;
      }
      setEvents(page.data);
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

  async function toggleDetail(recallId: string) {
    if (expandedRecallId === recallId) {
      setExpandedRecallId("");
      return;
    }
    setExpandedRecallId(recallId);
    if (details[recallId]) {
      return;
    }
    setDetailLoading(recallId);
    setError("");
    try {
      const detail = await api.getRecallFeedbackEvent(recallId);
      setDetails((current) => ({ ...current, [recallId]: detail }));
    } catch (err) {
      setError(readError(err));
    } finally {
      setDetailLoading("");
    }
  }

  useEffect(() => {
    void loadEvents();
  }, []);

  const teamNames = new Map(teams.map((team) => [team.id, team.name]));
  const offset = query.offset ?? 0;
  const limit = query.limit ?? 100;
  const rangeStart = total === 0 ? 0 : offset + 1;
  const rangeEnd = Math.min(offset + events.length, total);

  return (
    <section className="surface">
      <SectionHeading
        title="Recall Feedback"
        meta={total}
        actions={(
          <button className="icon-button" type="button" aria-label="Refresh recall feedback" onClick={() => void loadEvents()}>
            <RefreshCw size={16} aria-hidden="true" />
          </button>
        )}
      />
      {error && <div className="banner error" role="alert">{error}</div>}
      <div className="metrics-toolbar">
        <label>
          Team
          <select
            value={query.team_id ?? ""}
            onChange={(event) => {
              const next = { ...query, team_id: event.target.value, offset: 0 };
              setQuery(next);
              void loadEvents(next);
            }}
          >
            <option value="">All</option>
            {teams.map((team) => <option value={team.id} key={team.id}>{team.name}</option>)}
          </select>
        </label>
        <label>
          Quality
          <select
            value={query.quality ?? ""}
            onChange={(event) => {
              const next = { ...query, quality: event.target.value as RecallFeedbackEventQuery["quality"], offset: 0 };
              setQuery(next);
              void loadEvents(next);
            }}
          >
            {QUALITIES.map((quality) => <option value={quality} key={quality}>{quality || "All"}</option>)}
          </select>
        </label>
        <label>
          Missing context
          <select
            value={query.missing_context === "" || query.missing_context === undefined ? "" : String(query.missing_context)}
            onChange={(event) => {
              const next = { ...query, missing_context: boolFilter(event.target.value), offset: 0 };
              setQuery(next);
              void loadEvents(next);
            }}
          >
            <option value="">All</option>
            <option value="true">Yes</option>
            <option value="false">No</option>
          </select>
        </label>
        <label>
          Irrelevant
          <select
            value={query.irrelevant === "" || query.irrelevant === undefined ? "" : String(query.irrelevant)}
            onChange={(event) => {
              const next = { ...query, irrelevant: boolFilter(event.target.value), offset: 0 };
              setQuery(next);
              void loadEvents(next);
            }}
          >
            <option value="">All</option>
            <option value="true">Yes</option>
            <option value="false">No</option>
          </select>
        </label>
        <label>
          From
          <input
            type="datetime-local"
            value={rfc3339ToLocalInput(query.from)}
            onChange={(event) => {
              const next = { ...query, from: localInputToRFC3339(event.target.value), offset: 0 };
              setQuery(next);
              void loadEvents(next);
            }}
          />
        </label>
        <label>
          To
          <input
            type="datetime-local"
            value={rfc3339ToLocalInput(query.to)}
            onChange={(event) => {
              const next = { ...query, to: localInputToRFC3339(event.target.value), offset: 0 };
              setQuery(next);
              void loadEvents(next);
            }}
          />
        </label>
      </div>
      {loading && events.length === 0 ? (
        <div className="table-placeholder">Loading</div>
      ) : events.length === 0 ? (
        <div className="table-placeholder">No recall feedback</div>
      ) : (
        <div className="table-wrap">
          <table className="data-table logs-table">
            <thead>
              <tr>
                <th>Time</th>
                <th>Quality</th>
                <th>Flags</th>
                <th>Query</th>
                <th>Results</th>
                <th>Team</th>
                <th>Details</th>
              </tr>
            </thead>
            <tbody>
              {events.map((event) => {
                const expanded = expandedRecallId === event.recall_id;
                const detail = details[event.recall_id];
                return (
                  <Fragment key={event.recall_id}>
                    <tr>
                      <td>{formatDate(event.created_at)}</td>
                      <td><span className={qualityClass(event.quality)}>{event.quality || "pending"}</span></td>
                      <td>
                        <div className="log-detail-list" aria-label="Feedback flags">
                          {feedbackFlagChips(event)}
                        </div>
                      </td>
                      <td>
                        <div className="log-message-cell">
                          <strong>{event.query || "No query snapshot"}</strong>
                          <div className="log-detail-list" aria-label="Recall metadata">
                            <span className="log-detail-chip">{event.snapshot_state}</span>
                            <span className="log-detail-chip">{shortId(event.recall_id)}</span>
                          </div>
                        </div>
                      </td>
                      <td>{event.result_count}</td>
                      <td>{event.team_id ? (teamNames.get(event.team_id) ?? shortId(event.team_id)) : "Unknown"}</td>
                      <td className="actions-cell">
                        <button
                          className="icon-button"
                          type="button"
                          aria-label={`${expanded ? "Hide" : "View"} recall feedback ${event.recall_id}`}
                          title="Recall feedback details"
                          onClick={() => void toggleDetail(event.recall_id)}
                        >
                          <Braces size={16} aria-hidden="true" />
                        </button>
                      </td>
                    </tr>
                    {expanded && (
                      <tr className="log-raw-row">
                        <td colSpan={7}>
                          {detailLoading === event.recall_id && !detail ? (
                            <div className="table-placeholder compact">Loading</div>
                          ) : (
                            <RecallFeedbackDetail event={detail ?? event} />
                          )}
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
              void loadEvents(next);
            }}
          >
            {PAGE_SIZES.map((pageSize) => <option value={pageSize} key={pageSize}>{pageSize}</option>)}
          </select>
        </label>
        <button
          className="ghost-button"
          type="button"
          disabled={loading || offset === 0}
          onClick={() => {
            const next = { ...query, offset: Math.max(0, offset - limit) };
            setQuery(next);
            void loadEvents(next);
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
            void loadEvents(next);
          }}
        >
          Next
        </button>
      </div>
    </section>
  );
}

function RecallFeedbackDetail({ event }: { event: RecallFeedbackEvent }) {
  const resolved = event.resolved_results ?? [];
  const refs = event.result_refs ?? [];
  return (
    <div>
      <div className="log-detail-list" aria-label="Recall feedback details">
        <span className="log-detail-chip">recall_id={event.recall_id}</span>
        <span className="log-detail-chip">auth={event.auth_method || "unknown"}</span>
        <span className="log-detail-chip">profile={event.profile_id ? shortId(event.profile_id) : "unknown"}</span>
        <span className="log-detail-chip">key={event.key_id ? shortId(event.key_id) : "unknown"}</span>
      </div>
      <ResultRefTable refs={refs} resolved={resolved} />
      <pre aria-label={`Raw recall feedback ${event.recall_id}`}>{JSON.stringify(rawEvent(event), null, 2)}</pre>
    </div>
  );
}

function ResultRefTable({ refs, resolved }: { refs: RecallFeedbackResultRef[]; resolved: RecallFeedbackResolvedResult[] }) {
  const resolvedByKey = new Map(resolved.map((item) => [`${item.type}:${item.id}:${item.rank}`, item]));
  if (refs.length === 0) {
    return <div className="table-placeholder compact">No result refs</div>;
  }
  return (
    <table className="data-table" aria-label="Recall feedback result refs">
      <thead>
        <tr>
          <th>Rank</th>
          <th>Type</th>
          <th>ID</th>
          <th>Status</th>
          <th>Current</th>
        </tr>
      </thead>
      <tbody>
        {refs.map((ref) => {
          const current = resolvedByKey.get(`${ref.type}:${ref.id}:${ref.rank}`);
          return (
            <tr key={`${ref.type}:${ref.id}:${ref.rank}`}>
              <td>{ref.rank}</td>
              <td>{ref.type}</td>
              <td><code>{ref.id}</code></td>
              <td>{current ? currentStatus(current) : (ref.status_at_recall || "unknown")}</td>
              <td>{current ? currentSummary(current) : "Not resolved"}</td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function feedbackFlagChips(event: RecallFeedbackEvent) {
  const chips = [
    event.used === true ? "used" : event.used === false ? "unused" : "",
    event.answer_supported === true ? "supported" : event.answer_supported === false ? "unsupported" : "",
    event.missing_context ? "missing context" : "",
    event.irrelevant ? "irrelevant" : "",
  ].filter(Boolean);
  if (chips.length === 0) {
    return <span className="log-detail-chip">pending</span>;
  }
  return chips.map((chip) => <span className="log-detail-chip" key={chip}>{chip}</span>);
}

function qualityClass(quality?: string): string {
  if (quality === "low") {
    return "status-pill error";
  }
  if (quality === "medium") {
    return "status-pill warning";
  }
  return "status-pill neutral";
}

function currentStatus(result: RecallFeedbackResolvedResult): string {
  if (result.resolution_status === "missing") {
    return "missing";
  }
  return result.current_status || result.ref.status_at_recall || "found";
}

function currentSummary(result: RecallFeedbackResolvedResult): string {
  if (result.resolution_status === "missing") {
    return "Missing from graph";
  }
  const current = result.current ?? {};
  if (result.type === "fragment") {
    return compact(String(current.content ?? ""));
  }
  const triple = [current.subject, current.predicate, current.object].map((value) => String(value ?? "").trim()).filter(Boolean);
  if (triple.length > 0) {
    return compact(triple.join(" "));
  }
  return "Resolved";
}

function rawEvent(event: RecallFeedbackEvent): Record<string, unknown> {
  return {
    recall_id: event.recall_id,
    created_at: event.created_at,
    feedback_at: event.feedback_at,
    team_id: event.team_id,
    profile_id: event.profile_id,
    key_id: event.key_id,
    auth_method: event.auth_method,
    tool_name: event.tool_name,
    query: event.query,
    tool_args: event.tool_args ?? {},
    feedback: {
      used: event.used,
      answer_supported: event.answer_supported,
      quality: event.quality,
      missing_context: event.missing_context,
      irrelevant: event.irrelevant,
    },
    result_refs: event.result_refs ?? [],
    resolved_results: event.resolved_results ?? [],
    snapshot_state: event.snapshot_state,
  };
}

function boolFilter(value: string): boolean | "" {
  if (value === "true") {
    return true;
  }
  if (value === "false") {
    return false;
  }
  return "";
}

function localInputToRFC3339(value: string): string {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  return date.toISOString();
}

function rfc3339ToLocalInput(value?: string): string {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  const pad = (part: number) => String(part).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function compact(value: string): string {
  const trimmed = value.trim().replace(/\s+/g, " ");
  return trimmed.length > 160 ? `${trimmed.slice(0, 157)}...` : trimmed;
}
