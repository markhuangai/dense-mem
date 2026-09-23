import { useEffect, useRef, useState } from "react";
import { ControlApi, Dream, DreamDiagnostic, DreamQuery, DreamRun, DreamSort, DreamStatus, Team } from "../api";
import { InfoTooltip, LoadingState, SectionHeading } from "../ui/components";
import { DreamEvidenceSummary, MetricLabel, runOutcome, runStatusClass } from "../ui/dreams";
import { formatDate, readError } from "./utils";

const DREAM_STATUSES = ["", "proposed", "reinforced", "stale", "rejected", "submitted"];
const DREAM_PAGE_SIZES = [10, 25, 50, 100];
const DREAM_SORTS: Array<{ value: DreamSort; label: string }> = [
  { value: "updated_at", label: "Updated" },
  { value: "created_at", label: "Created" },
];
const DEFAULT_DREAM_QUERY: DreamQuery = { status: "", limit: 25, sort: "updated_at", direction: "desc", cursor: "" };

export function ControlDreamsPanel({ api, team, embedded = false }: { api: ControlApi; team: Team; embedded?: boolean }) {
  const [status, setStatus] = useState<DreamStatus | null>(null);
  const [runs, setRuns] = useState<DreamRun[]>([]);
  const [dreams, setDreams] = useState<Dream[]>([]);
  const [dreamQuery, setDreamQuery] = useState<DreamQuery>(DEFAULT_DREAM_QUERY);
  const [cursorStack, setCursorStack] = useState<string[]>([]);
  const [nextCursor, setNextCursor] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [diagnosticRun, setDiagnosticRun] = useState<DreamRun | null>(null);
  const [diagnostics, setDiagnostics] = useState<DreamDiagnostic[]>([]);
  const [diagnosticCursor, setDiagnosticCursor] = useState("");
  const [diagnosticsLoading, setDiagnosticsLoading] = useState(false);
  const [selectedDiagnostic, setSelectedDiagnostic] = useState<DreamDiagnostic | null>(null);
  const [diagnosticHypothesis, setDiagnosticHypothesis] = useState<string | null>(null);
  const requestSeqRef = useRef(0);
  const diagnosticRequestSeqRef = useRef(0);
  const diagnosticDetailSeqRef = useRef(0);
  const diagnosticSelectionRef = useRef("");
  const activeTeamIdRef = useRef(team.id);

  activeTeamIdRef.current = team.id;

  async function loadData(
    nextQuery = dreamQuery,
    nextCursorStack = cursorStack,
  ) {
    const requestSeq = requestSeqRef.current + 1;
    requestSeqRef.current = requestSeq;
    const requestTeamId = team.id;
    setLoading(true);
    setError("");
    try {
      const [nextStatusResult, nextRuns, nextDreams] = await Promise.all([
        api.getTeamDreamingStatus(requestTeamId),
        api.listTeamDreamingRuns(requestTeamId, 10),
        api.listTeamDreams(requestTeamId, nextQuery),
      ]);
      if (requestSeq !== requestSeqRef.current || requestTeamId !== activeTeamIdRef.current) {
        return;
      }
      setStatus(nextStatusResult);
      setRuns(nextRuns);
      setDreams(nextDreams.items);
      setDreamQuery(nextQuery);
      setCursorStack(nextCursorStack);
      setNextCursor(nextDreams.next_cursor ?? "");
    } catch (err) {
      if (requestSeq !== requestSeqRef.current || requestTeamId !== activeTeamIdRef.current) {
        return;
      }
      setError(readError(err));
    } finally {
      if (requestSeq === requestSeqRef.current && requestTeamId === activeTeamIdRef.current) {
        setLoading(false);
      }
    }
  }

  useEffect(() => {
    diagnosticRequestSeqRef.current += 1;
    diagnosticDetailSeqRef.current += 1;
    diagnosticSelectionRef.current = "";
    setDiagnosticRun(null);
    setDiagnostics([]);
    setDiagnosticCursor("");
    setSelectedDiagnostic(null);
    setDiagnosticHypothesis(null);
    void loadData({ ...dreamQuery, cursor: "" }, []);
  }, [team.id]);

  async function inspectRun(run: DreamRun, cursor = "") {
    const requestSeq = diagnosticRequestSeqRef.current + 1;
    diagnosticRequestSeqRef.current = requestSeq;
    const requestTeamId = team.id;
    const selectionKey = `run:${run.run_id}`;
    if (!cursor) {
      diagnosticSelectionRef.current = selectionKey;
      diagnosticDetailSeqRef.current += 1;
      setDiagnostics([]);
      setDiagnosticCursor("");
      setSelectedDiagnostic(null);
    }
    setDiagnosticRun(run);
    setDiagnosticHypothesis(null);
    setDiagnosticsLoading(true);
    setError("");
    try {
      const page = await api.listTeamDreamDiagnostics(requestTeamId, run.run_id, 25, cursor);
      if (requestSeq !== diagnosticRequestSeqRef.current || requestTeamId !== activeTeamIdRef.current || diagnosticSelectionRef.current !== selectionKey) {
        return;
      }
      setDiagnostics((previous) => cursor ? [...previous, ...page.items] : page.items);
      setDiagnosticCursor(page.next_cursor ?? "");
    } catch (err) {
      if (requestSeq === diagnosticRequestSeqRef.current && requestTeamId === activeTeamIdRef.current && diagnosticSelectionRef.current === selectionKey) {
        setError(readError(err));
      }
    } finally {
      if (requestSeq === diagnosticRequestSeqRef.current && requestTeamId === activeTeamIdRef.current && diagnosticSelectionRef.current === selectionKey) {
        setDiagnosticsLoading(false);
      }
    }
  }

  async function inspectHypothesisByID(hypothesisID: string, cursor = "") {
    const requestSeq = diagnosticRequestSeqRef.current + 1;
    diagnosticRequestSeqRef.current = requestSeq;
    const requestTeamId = team.id;
    const selectionKey = `hypothesis:${hypothesisID}`;
    if (!cursor) {
      diagnosticSelectionRef.current = selectionKey;
      diagnosticDetailSeqRef.current += 1;
      setDiagnostics([]);
      setDiagnosticCursor("");
      setSelectedDiagnostic(null);
    }
    setDiagnosticRun(null);
    setDiagnosticHypothesis(hypothesisID);
    setDiagnosticsLoading(true);
    setError("");
    try {
      const page = await api.listTeamDreamDiagnosticsForHypothesis(requestTeamId, hypothesisID, 25, cursor);
      if (requestSeq !== diagnosticRequestSeqRef.current || requestTeamId !== activeTeamIdRef.current || diagnosticSelectionRef.current !== selectionKey) {
        return;
      }
      setDiagnostics((previous) => cursor ? [...previous, ...page.items] : page.items);
      setDiagnosticCursor(page.next_cursor ?? "");
    } catch (err) {
      if (requestSeq === diagnosticRequestSeqRef.current && requestTeamId === activeTeamIdRef.current && diagnosticSelectionRef.current === selectionKey) {
        setError(readError(err));
      }
    } finally {
      if (requestSeq === diagnosticRequestSeqRef.current && requestTeamId === activeTeamIdRef.current && diagnosticSelectionRef.current === selectionKey) {
        setDiagnosticsLoading(false);
      }
    }
  }

  async function inspectHypothesis(dream: Dream, cursor = "") {
    return inspectHypothesisByID(dream.dream_id, cursor);
  }

  async function loadDiagnostic(diagnostic: DreamDiagnostic) {
    const requestSeq = diagnosticDetailSeqRef.current + 1;
    diagnosticDetailSeqRef.current = requestSeq;
    const requestTeamId = team.id;
    const selectionKey = diagnosticRun ? `run:${diagnosticRun.run_id}` : diagnosticHypothesis ? `hypothesis:${diagnosticHypothesis}` : "";
    if (!selectionKey || !diagnostic.run_id) return;
    setSelectedDiagnostic(null);
    try {
      const detail = await api.getTeamDreamDiagnostic(requestTeamId, diagnostic.run_id, diagnostic.capture_id);
      if (requestSeq !== diagnosticDetailSeqRef.current || requestTeamId !== activeTeamIdRef.current || diagnosticSelectionRef.current !== selectionKey) {
        return;
      }
      setSelectedDiagnostic(detail);
    } catch (err) {
      if (requestSeq === diagnosticDetailSeqRef.current && requestTeamId === activeTeamIdRef.current && diagnosticSelectionRef.current === selectionKey) {
        setError(readError(err));
      }
    }
  }

  const pageNumber = cursorStack.length + 1;
  const dreamSort = dreamQuery.sort ?? "updated_at";
  const dreamDirection = dreamQuery.direction ?? "desc";
  const dreamLimit = dreamQuery.limit ?? DEFAULT_DREAM_QUERY.limit ?? 25;
  const panelClassName = embedded ? "overview-panel" : "surface";

  return (
    <>
      <section className={panelClassName}>
        <SectionHeading title="Dreaming" meta={team.name} />
        {error && <div className="banner error" role="alert">{error}</div>}
        {status && (
          <div className="dream-status-bar" aria-label="Dreaming status">
            <StatusItem label="Source" value={sourceLabel(status.effective_config.source)} />
            <StatusItem label="Scheduled" value={status.effective_config.enabled ? "Enabled" : "Disabled"} />
            <StatusItem label="Pending" value={status.pending_count} />
            <StatusItem label="Latest run" value={status.latest_run ? runLabel(status.latest_run) : "None"} />
          </div>
        )}
      </section>

      <section className={panelClassName}>
        <SectionHeading title="Dream Outputs" meta={`Page ${pageNumber}`} />
        <div className="metrics-toolbar dream-list-toolbar">
          <label>
            Status
            <select
              value={dreamQuery.status ?? ""}
              onChange={(event) => {
                void loadData({ ...dreamQuery, status: event.target.value, cursor: "" }, []);
              }}
            >
              {DREAM_STATUSES.map((statusOption) => (
                <option value={statusOption} key={statusOption}>{statusOption || "All"}</option>
              ))}
            </select>
          </label>
          <label>
            Sort
            <select
              value={dreamSort}
              onChange={(event) => {
                void loadData({ ...dreamQuery, sort: event.target.value as DreamSort, cursor: "" }, []);
              }}
            >
              {DREAM_SORTS.map((sortOption) => (
                <option value={sortOption.value} key={sortOption.value}>{sortOption.label}</option>
              ))}
            </select>
          </label>
          <label>
            Direction
            <select
              value={dreamDirection}
              onChange={(event) => {
                void loadData({ ...dreamQuery, direction: event.target.value as DreamQuery["direction"], cursor: "" }, []);
              }}
            >
              <option value="desc">Desc</option>
              <option value="asc">Asc</option>
            </select>
          </label>
        </div>
        {loading && dreams.length === 0 ? (
          <LoadingState label="Loading dreams" />
        ) : dreams.length === 0 ? (
          <div className="table-placeholder">No dreams</div>
        ) : (
          <DreamTable dreams={dreams} sort={dreamSort} onInspectHypothesis={inspectHypothesis} />
        )}
        <div className="table-actions">
          <span className="form-meta">Page {pageNumber} · {dreams.length} rows</span>
          <label className="table-page-size">
            Rows
            <select
              value={dreamLimit}
              disabled={loading}
              onChange={(event) => {
                void loadData({ ...dreamQuery, limit: Number(event.target.value), cursor: "" }, []);
              }}
            >
              {DREAM_PAGE_SIZES.map((pageSize) => (
                <option value={pageSize} key={pageSize}>{pageSize}</option>
              ))}
            </select>
          </label>
          <button
            className="ghost-button"
            type="button"
            disabled={loading || cursorStack.length === 0}
            onClick={() => {
              const previousStack = cursorStack.slice(0, -1);
              void loadData({ ...dreamQuery, cursor: cursorStack[cursorStack.length - 1] ?? "" }, previousStack);
            }}
          >
            Previous
          </button>
          <button
            className="ghost-button"
            type="button"
            disabled={loading || !nextCursor}
            onClick={() => {
              void loadData({ ...dreamQuery, cursor: nextCursor }, [...cursorStack, dreamQuery.cursor ?? ""]);
            }}
          >
            Next
          </button>
        </div>
      </section>

      <section className={panelClassName}>
        <SectionHeading title="Cycle Runs" meta={runs.length} />
        {loading && runs.length === 0 ? (
          <LoadingState label="Loading runs" />
        ) : runs.length === 0 ? (
          <div className="table-placeholder">No runs</div>
        ) : (
          <RunTable runs={runs} onInspect={inspectRun} />
        )}
      </section>

      {(diagnosticRun || diagnosticHypothesis) && (
        <section className={panelClassName} aria-label="Dream diagnostics">
          <SectionHeading title={diagnosticRun ? "Run investigation" : "Hypothesis investigation"} meta={(diagnosticRun?.run_id ?? diagnosticHypothesis ?? "").slice(0, 8)} />
          {diagnosticsLoading && diagnostics.length === 0 ? (
            <LoadingState label="Loading Dream diagnostics" />
          ) : diagnostics.length === 0 ? (
            <div className="table-placeholder">No diagnostic capture was retained for this selection.</div>
          ) : (
            <div className="table-wrap">
              <table className="data-table dream-diagnostics-table">
                <thead><tr><th>Phase</th><th>Hypothesis</th><th>Outcome</th><th>Cause</th><th>Capture</th><th>Details</th><th>Payload</th></tr></thead>
                <tbody>
                  {diagnostics.map((diagnostic) => (
                    <tr key={diagnostic.capture_id}>
                      <td>{diagnostic.phase}</td>
                      <td><code>{diagnostic.hypothesis_id?.slice(0, 8) || "-"}</code></td>
                      <td>{diagnostic.outcome}</td>
                      <td>{diagnostic.cause || "-"}</td>
                      <td>{diagnostic.capture_state}{diagnostic.capture_reason ? ` · ${diagnostic.capture_reason}` : ""}</td>
                      <td><code>{diagnostic.details ? JSON.stringify(diagnostic.details) : "Unavailable"}</code></td>
                      <td><button className="ghost-button" type="button" onClick={() => void loadDiagnostic(diagnostic)}>View capture</button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {selectedDiagnostic && (
            <div className="dream-diagnostic-detail" role="status">
              <strong>Selected capture</strong>
              <span>Hypothesis: {selectedDiagnostic.hypothesis_id || "-"}</span>
              <span>{selectedDiagnostic.capture_state}{selectedDiagnostic.capture_reason ? ` · ${selectedDiagnostic.capture_reason}` : ""}</span>
              <code>{selectedDiagnostic.payload ? JSON.stringify(selectedDiagnostic.payload) : "Payload unavailable or expired"}</code>
            </div>
          )}
          {diagnosticCursor && (
            <div className="table-actions">
              <button className="ghost-button" type="button" disabled={diagnosticsLoading} onClick={() => {
                if (diagnosticRun) {
                  void inspectRun(diagnosticRun, diagnosticCursor);
                } else if (diagnosticHypothesis) {
                  void inspectHypothesisByID(diagnosticHypothesis, diagnosticCursor);
                }
              }}>Load more</button>
            </div>
          )}
        </section>
      )}
    </>
  );
}

function StatusItem({ label, value }: { label: string; value: string | number }) {
  return (
    <span>
      <span>{label}</span>
      <strong>{value}</strong>
    </span>
  );
}

function DreamTable({ dreams, sort, onInspectHypothesis }: { dreams: Dream[]; sort: DreamSort; onInspectHypothesis: (dream: Dream) => void }) {
  return (
    <div className="table-wrap dream-table-wrap">
      <table className="data-table dreams-table">
        <thead>
          <tr>
            <th>{dreamDateHeader(sort)}</th>
            <th>Status</th>
            <th>Hypothesis</th>
            <th>Evidence</th>
            <th>Confidence</th>
            <th>Run</th>
            <th>Investigation</th>
          </tr>
        </thead>
        <tbody>
          {dreams.map((dream) => (
            <tr key={dream.dream_id}>
              <td>{formatDreamDate(dream, sort)}</td>
              <td><span className={dreamStatusClass(dream.status)}>{dream.status}</span></td>
              <td>
                <div className="dream-hypothesis-cell">
                  <strong>{dream.hypothesis}</strong>
                  {dream.rationale && (
                    <InfoTooltip label={`Why this hypothesis: ${dream.hypothesis}`}>
                      {dream.rationale}
                    </InfoTooltip>
                  )}
                </div>
              </td>
              <td><DreamEvidenceSummary dream={dream} /></td>
              <td>{Math.round(dream.confidence * 100)}%</td>
              <td><code>{dream.cycle_run_id?.slice(0, 8) || "-"}</code></td>
              <td><button className="ghost-button" type="button" onClick={() => onInspectHypothesis(dream)}>Inspect</button></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function dreamDateHeader(sort: DreamSort): string {
  return DREAM_SORTS.find((option) => option.value === sort)?.label ?? "Updated";
}

function formatDreamDate(dream: Dream, sort: DreamSort): string {
  const value = sort === "created_at" ? dream.created_at : dream.updated_at;
  return value ? formatDate(value) : "-";
}

function RunTable({ runs, onInspect }: { runs: DreamRun[]; onInspect: (run: DreamRun) => void }) {
  return (
    <div className="table-wrap">
      <table className="data-table dream-runs-table">
        <thead>
          <tr>
            <th>Started</th>
            <th>Lane</th>
            <th>Status</th>
            <th><MetricLabel label="Eligible" detail="Graph relationships or evidence targets selected for this Dream lane." /></th>
            <th><MetricLabel label="Paths" detail="Direct A → B → C paths for graph dreaming; evidence target passes for discovery." /></th>
            <th><MetricLabel label="AI" detail="Valid possible-relationship proposals returned by the provider." /></th>
            <th>Created</th>
            <th><MetricLabel label="Rejected" detail="Provider proposals rejected by current target or source policy after validation." /></th>
            <th>Outcome</th>
            <th>Investigation</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <tr key={run.run_id}>
              <td>{formatDate(run.started_at)}</td>
              <td>{run.lane === "evidence_discovery" ? "Evidence discovery" : "Graph"}</td>
              <td><span className={runStatusClass(run.status)}>{run.status}</span></td>
              <td>{run.lane === "evidence_discovery" ? run.evidence_targets ?? 0 : run.input_relationships}</td>
              <td>{run.lane === "evidence_discovery" ? run.evaluated_evidence_targets ?? 0 : run.attempted_paths ?? 0}</td>
              <td>{run.provider_proposals ?? 0}</td>
              <td>{run.created_dreams}</td>
              <td>{run.rejected_dreams}</td>
              <td>{runOutcome(run)}</td>
              <td><button className="ghost-button" type="button" onClick={() => onInspect(run)}>Inspect</button></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function runLabel(run: DreamRun): string {
  return `${run.status} ${formatDate(run.started_at)}`;
}

function sourceLabel(source: string): string {
  if (source === "global_force") {
    return "Global force";
  }
  if (source === "team") {
    return "Team";
  }
  return "Global";
}

function dreamStatusClass(status: string): string {
  switch (status) {
    case "rejected":
    case "stale":
      return "status-pill warning";
    case "submitted":
    case "reinforced":
      return "status-pill";
    default:
      return "status-pill neutral";
  }
}
