import { useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { SectionHeading } from "../ui/components";
import { Dream, DreamRun, DreamStatus, UserApi } from "./api";

const DREAM_STATUSES = ["", "proposed", "reinforced", "stale", "rejected", "promoted"];

export function UserDreamsPanel({ api }: { api: UserApi }) {
  const [status, setStatus] = useState<DreamStatus | null>(null);
  const [runs, setRuns] = useState<DreamRun[]>([]);
  const [dreams, setDreams] = useState<Dream[]>([]);
  const [dreamStatus, setDreamStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const requestSeqRef = useRef(0);

  async function loadData(nextStatus = dreamStatus) {
    const requestSeq = requestSeqRef.current + 1;
    requestSeqRef.current = requestSeq;
    setLoading(true);
    setError("");
    try {
      const [nextStatusResult, nextRuns, nextDreams] = await Promise.all([
        api.dreamingStatus(),
        api.listDreamingRuns(10),
        api.listDreams(nextStatus, 50),
      ]);
      if (requestSeq !== requestSeqRef.current) {
        return;
      }
      setStatus(nextStatusResult);
      setRuns(nextRuns);
      setDreams(nextDreams.items);
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

  useEffect(() => {
    void loadData();
  }, [api]);

  return (
    <>
      <section className="surface">
        <SectionHeading
          title="Dreaming"
          actions={(
            <button className="icon-button" type="button" aria-label="Refresh dreams" onClick={() => void loadData()}>
              <RefreshCw size={16} aria-hidden="true" />
            </button>
          )}
        />
        {error && <div className="banner error" role="alert">{error}</div>}
        {status && (
          <div className="metrics-summary">
            <SummaryMetric label="Source" value={sourceLabel(status.effective_config.source)} />
            <SummaryMetric label="Scheduled" value={status.effective_config.enabled ? "Enabled" : "Disabled"} />
            <SummaryMetric label="Pending" value={status.pending_count} />
            <SummaryMetric label="Latest run" value={status.latest_run ? `${status.latest_run.status} ${formatDate(status.latest_run.started_at)}` : "None"} />
          </div>
        )}
      </section>

      <section className="surface">
        <SectionHeading title="Dream Outputs" meta={dreams.length} />
        <div className="metrics-toolbar">
          <label>
            Status
            <select
              value={dreamStatus}
              onChange={(event) => {
                setDreamStatus(event.target.value);
                void loadData(event.target.value);
              }}
            >
              {DREAM_STATUSES.map((statusOption) => (
                <option value={statusOption} key={statusOption}>{statusOption || "All"}</option>
              ))}
            </select>
          </label>
        </div>
        {loading && dreams.length === 0 ? (
          <div className="table-placeholder">Loading</div>
        ) : dreams.length === 0 ? (
          <div className="table-placeholder">No dreams</div>
        ) : (
          <DreamTable dreams={dreams} />
        )}
      </section>

      <section className="surface">
        <SectionHeading title="Cycle Runs" meta={runs.length} />
        {loading && runs.length === 0 ? (
          <div className="table-placeholder">Loading</div>
        ) : runs.length === 0 ? (
          <div className="table-placeholder">No runs</div>
        ) : (
          <RunTable runs={runs} />
        )}
      </section>
    </>
  );
}

function SummaryMetric({ label, value }: { label: string; value: string | number }) {
  return (
    <div>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function DreamTable({ dreams }: { dreams: Dream[] }) {
  return (
    <div className="table-wrap">
      <table className="data-table dreams-table">
        <thead>
          <tr>
            <th>Updated</th>
            <th>Status</th>
            <th>Hypothesis</th>
            <th>Confidence</th>
            <th>Run</th>
          </tr>
        </thead>
        <tbody>
          {dreams.map((dream) => (
            <tr key={dream.dream_id}>
              <td>{formatDate(dream.updated_at)}</td>
              <td><span className={dreamStatusClass(dream.status)}>{dream.status}</span></td>
              <td>
                <strong>{dream.hypothesis}</strong>
                <small>{dream.rationale}</small>
              </td>
              <td>{Math.round(dream.confidence * 100)}%</td>
              <td><code>{dream.cycle_run_id?.slice(0, 8) || "-"}</code></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function RunTable({ runs }: { runs: DreamRun[] }) {
  return (
    <div className="table-wrap">
      <table className="data-table dream-runs-table">
        <thead>
          <tr>
            <th>Started</th>
            <th>Status</th>
            <th>Phases</th>
            <th>Created</th>
            <th>Re-evaluated</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <tr key={run.run_id}>
              <td>{formatDate(run.started_at)}</td>
              <td><span className={runStatusClass(run.status)}>{run.status}</span></td>
              <td>{[run.reflect_ran && "reflect", run.reevaluate_ran && "re-evaluate", run.dream_ran && "dream"].filter(Boolean).join(", ") || "-"}</td>
              <td>{run.created_dreams}</td>
              <td>{run.reevaluated_dreams}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
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
    case "promoted":
    case "reinforced":
      return "status-pill";
    default:
      return "status-pill neutral";
  }
}

function runStatusClass(status: string): string {
  if (status === "error") {
    return "status-pill error";
  }
  if (status === "skipped") {
    return "status-pill warning";
  }
  return "status-pill";
}

function readError(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed.";
}

function formatDate(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}
