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
            <SummaryCard label="Status" value={snapshot.status.replaceAll("_", " ")} detail="Document authority" tone={snapshot.status === "converged" ? "neutral" : "warning"} />
            <SummaryCard label="Expected" value={snapshot.expected_documents} detail="Active-contract documents" />
            <SummaryCard label="Current" value={snapshot.current_documents} detail="Validated vectors" />
            <SummaryCard label="Drifted" value={snapshot.drifted_documents} detail={snapshot.affected_team_count + " affected teams"} tone={snapshot.drifted_documents > 0 ? "warning" : "neutral"} />
          </div>
          <p className="form-meta">Drift is derived from canonical search documents. New writes embed before commit; there is no queue or background embedding worker.</p>
          {snapshot.contract && <p className="form-meta">{snapshot.contract.provider} · {snapshot.contract.model} · {snapshot.contract.dimensions} dimensions · index generation {snapshot.contract.index_generation}</p>}
          <section className="overview-panel" aria-label="Search document drift">
            <SectionHeading title="Document drift" meta={"Oldest " + Math.round(snapshot.oldest_drift_age_seconds) + " seconds"} />
            {snapshot.drift_classes.length === 0 ? <p className="form-meta">No unresolved document drift.</p> : (
              <div className="mini-table">
                <div className="mini-table-row" aria-hidden="true"><span>Class</span><span>Documents</span></div>
                {snapshot.drift_classes.map((drift) => (
                  <div className="mini-table-row" key={drift.class}>
                    <span><strong>{drift.class.replaceAll("_", " ")}</strong></span>
                    <span>{drift.count}</span>
                  </div>
                ))}
              </div>
            )}
          </section>
          {snapshot.latest_run && (
            <section className="overview-panel" aria-label="Latest reconciliation run">
              <SectionHeading title="Latest reconciliation run" />
              <div className="mini-table">
                <div className="mini-table-row"><span>Status</span><strong>{snapshot.latest_run.status}</strong></div>
                <div className="mini-table-row"><span>Local run date</span><strong>{snapshot.latest_run.local_run_date}</strong></div>
                <div className="mini-table-row"><span>Selected</span><strong>{snapshot.latest_run.selected_count}</strong></div>
                <div className="mini-table-row"><span>Embedded / updated</span><strong>{snapshot.latest_run.embedded_count} / {snapshot.latest_run.updated_count}</strong></div>
                {snapshot.latest_run.last_error && <div className="mini-table-row"><span>Result</span><strong>{snapshot.latest_run.last_error}</strong></div>}
              </div>
            </section>
          )}
        </>
      )}
    </section>
  );
}
