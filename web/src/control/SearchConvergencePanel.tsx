import { useCallback, useEffect, useState } from "react";
import { RefreshCw, Search } from "lucide-react";
import { ControlApi, SearchConvergence } from "../api";
import { LoadingState, SectionHeading, SummaryCard } from "../ui/components";
import { formatDate, readError } from "./utils";

export function SearchConvergencePanel({ api }: { api: ControlApi }) {
  const [snapshot, setSnapshot] = useState<SearchConvergence | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setSnapshot(await api.getSearchConvergence());
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }, [api]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 60_000);
    return () => window.clearInterval(timer);
  }, [load]);

  if (!snapshot && loading) {
    return <LoadingState label="Loading search convergence" />;
  }

  return (
    <section className="surface" aria-label="Search convergence">
      <SectionHeading
        title={<span><Search size={18} aria-hidden="true" /> Search convergence</span>}
        actions={(
          <div className="button-row">
            {snapshot?.observed_at && <span className="form-meta">Observed {formatDate(snapshot.observed_at)}</span>}
            <button className="icon-button" type="button" aria-label="Refresh search convergence" onClick={() => void load()} disabled={loading}>
              <RefreshCw size={16} aria-hidden="true" />
            </button>
          </div>
        )}
      />
      {error && <div className="banner error" role="alert">{error}</div>}
      {snapshot && (
        <>
          <div className="summary-strip" aria-label="Search convergence summary">
            <SummaryCard label="Status" value={snapshot.status.replaceAll("_", " ")} detail="Automatic recovery" tone={snapshot.status === "converged" ? "neutral" : "warning"} />
            <SummaryCard label="Queued" value={snapshot.queue.queued} detail="Awaiting embedding" />
            <SummaryCard label="Failed" value={snapshot.queue.failed} detail={`${snapshot.queue.affected_team_count} affected teams`} tone={snapshot.queue.failed > 0 ? "warning" : "neutral"} />
            <SummaryCard label="Incidents" value={snapshot.incidents.length} detail="Operator visibility" tone={snapshot.incidents.length > 0 ? "warning" : "neutral"} />
          </div>
          <p className="form-meta">Failed vectors stay out of vector search. Valid text remains eligible for lexical recall while the daily canary attempts recovery.</p>
          {snapshot.contract && <p className="form-meta">{snapshot.contract.provider} · {snapshot.contract.model} · {snapshot.contract.dimensions} dimensions · index generation {snapshot.contract.index_generation}</p>}
          {snapshot.latest_run && (
            <section className="overview-panel" aria-label="Latest reconciliation run">
              <SectionHeading title="Latest reconciliation run" />
              <div className="mini-table">
                <div className="mini-table-row"><span>Status</span><strong>{snapshot.latest_run.status}</strong></div>
                <div className="mini-table-row"><span>Local run date</span><strong>{snapshot.latest_run.local_run_date}</strong></div>
                <div className="mini-table-row"><span>Canary</span><strong>{snapshot.latest_run.canary_outcome || "not attempted"}</strong></div>
                <div className="mini-table-row"><span>Recovered</span><strong>{snapshot.latest_run.recovered_count}</strong></div>
              </div>
            </section>
          )}
          <section className="overview-panel" aria-label="Embedding incidents">
            <SectionHeading title="Embedding incidents" meta="Read-only guidance" />
            {snapshot.incidents.length === 0 ? <p className="form-meta">No open incidents.</p> : (
              <div className="mini-table">
                <div className="mini-table-row" aria-hidden="true"><span>Team / reason</span><span>Jobs</span><span>Guidance</span></div>
                {snapshot.incidents.map((incident) => (
                  <div className="mini-table-row" key={incident.incident_id}>
                    <span><strong>{incident.team_name || incident.team_id}</strong><br />{incident.failure_code}</span>
                    <span>{incident.affected_job_count}</span>
                    <span>{incident.guidance}</span>
                  </div>
                ))}
              </div>
            )}
          </section>
        </>
      )}
    </section>
  );
}
