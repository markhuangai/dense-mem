import { useEffect, useRef, useState } from "react";
import { ControlApi, Dream, DreamQuery, DreamRun, DreamSort, DreamStatus, Team } from "../api";
import { InfoTooltip, LoadingState, SectionHeading } from "../ui/components";
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
  const requestSeqRef = useRef(0);
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
    void loadData({ ...dreamQuery, cursor: "" }, []);
  }, [team.id]);

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
          <DreamTable dreams={dreams} sort={dreamSort} />
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
          <RunTable runs={runs} />
        )}
      </section>
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

function DreamTable({ dreams, sort }: { dreams: Dream[]; sort: DreamSort }) {
  return (
    <div className="table-wrap">
      <table className="data-table dreams-table">
        <thead>
          <tr>
            <th>{dreamDateHeader(sort)}</th>
            <th>Status</th>
            <th>Hypothesis</th>
            <th>Evidence</th>
            <th>Confidence</th>
            <th>Run</th>
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
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function DreamEvidenceSummary({ dream }: { dream: Dream }) {
  const derivations = dream.derivations ?? [];
  if (derivations.length === 0) {
    return <span className="form-meta">No cited excerpts</span>;
  }
  return (
    <span>
      {derivations.length} cited excerpt{derivations.length === 1 ? "" : "s"}
      <InfoTooltip label={`Evidence used for ${dream.hypothesis}`}>
        {derivations.map((derivation, index) => (
          <span key={`${derivation.relationship_id}-${derivation.premise_position}-${index}`}>
            Premise {derivation.premise_position} · {derivation.authority} · “{derivation.quote}”
          </span>
        ))}
      </InfoTooltip>
    </span>
  );
}

function dreamDateHeader(sort: DreamSort): string {
  return DREAM_SORTS.find((option) => option.value === sort)?.label ?? "Updated";
}

function formatDreamDate(dream: Dream, sort: DreamSort): string {
  const value = sort === "created_at" ? dream.created_at : dream.updated_at;
  return value ? formatDate(value) : "-";
}

function RunTable({ runs }: { runs: DreamRun[] }) {
  return (
    <div className="table-wrap">
      <table className="data-table dream-runs-table">
        <thead>
          <tr>
            <th>Started</th>
            <th>Status</th>
            <th><MetricLabel label="Eligible" detail="Relationships with current, valid evidence. This is not the number of AI calls." /></th>
            <th><MetricLabel label="Paths" detail="Direct A → B → C paths actually sent to the AI provider." /></th>
            <th><MetricLabel label="AI" detail="Valid possible-relationship proposals returned by the provider." /></th>
            <th>Created</th>
            <th><MetricLabel label="Rejected" detail="Provider proposals rejected by current target or source policy after validation." /></th>
            <th>Outcome</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <tr key={run.run_id}>
              <td>{formatDate(run.started_at)}</td>
              <td><span className={runStatusClass(run.status)}>{run.status}</span></td>
              <td>{run.input_relationships}</td>
              <td>{run.attempted_paths ?? 0}</td>
              <td>{run.provider_proposals ?? 0}</td>
              <td>{run.created_dreams}</td>
              <td>{run.rejected_dreams}</td>
              <td>{runOutcome(run)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function MetricLabel({ label, detail }: { label: string; detail: string }) {
  return (
    <span>
      {label} <InfoTooltip label={`${label} meaning`}>{detail}</InfoTooltip>
    </span>
  );
}

function runOutcome(run: DreamRun): string {
  const outcomes = run.outcome_summary;
  if (!outcomes) {
    return "-";
  }
  if ((outcomes.provider_failed ?? 0) > 0) {
    return "Provider call failed";
  }
  if ((outcomes.attempted_paths ?? 0) === 0) {
    if ((outcomes.blocked_targets ?? 0) > 0) {
      return `${outcomes.blocked_targets} target${outcomes.blocked_targets === 1 ? "" : "s"} already exist`;
    }
    if ((outcomes.previously_assessed_paths ?? 0) > 0) {
      return "All paths already assessed";
    }
    return "No eligible two-hop path";
  }
  if ((outcomes.policy_rejections ?? 0) > 0) {
    return `${outcomes.policy_rejections} blocked during persistence`;
  }
  if ((outcomes.provider_proposals ?? 0) === 0) {
    return "Provider returned no supported relationship";
  }
  return "Provider result stored";
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

function runStatusClass(status: string): string {
  if (status === "error" || status === "failed") {
    return "status-pill error";
  }
  if (status === "skipped") {
    return "status-pill warning";
  }
  return "status-pill";
}
