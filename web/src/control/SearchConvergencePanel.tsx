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
        title={
          <span>
            <Search size={18} aria-hidden="true" /> Search convergence
          </span>
        }
        actions={
          <div className="button-row">
            {snapshot?.observed_at && (
              <span className="form-meta">
                Observed {formatDate(snapshot.observed_at)}
              </span>
            )}
            <button
              className="icon-button"
              type="button"
              aria-label="Refresh search convergence"
              onClick={() => void load()}
              disabled={loading}
            >
              <RefreshCw size={16} aria-hidden="true" />
            </button>
          </div>
        }
      />
      {error && (
        <div className="banner error" role="alert">
          {error}
        </div>
      )}
      {snapshot && (
        <>
          <div
            className="summary-strip"
            aria-label="Search convergence summary"
          >
            <SummaryCard
              label="Status"
              value={snapshot.status.replaceAll("_", " ")}
              detail="Canonical documents"
              tone={snapshot.status === "converged" ? "neutral" : "warning"}
            />
            <SummaryCard
              label="Expected"
              value={snapshot.expected_documents}
              detail="Canonical sources"
            />
            <SummaryCard
              label="Current"
              value={snapshot.current_documents}
              detail="Active-contract vectors"
            />
            <SummaryCard
              label="Drifted"
              value={snapshot.drifted_documents}
              detail={`${snapshot.affected_team_count} affected teams`}
              tone={snapshot.drifted_documents > 0 ? "warning" : "neutral"}
            />
          </div>
          <p className="form-meta">
            Convergence compares canonical documents with active-contract
            vectors. Drift repair is bounded and document-fenced.
          </p>
          {snapshot.contract && (
            <p className="form-meta">
              {snapshot.contract.provider} · {snapshot.contract.model} ·{" "}
              {snapshot.contract.dimensions} dimensions · index generation{" "}
              {snapshot.contract.index_generation}
            </p>
          )}
          <section
            className="overview-panel"
            aria-label="Document drift classes"
          >
            <SectionHeading
              title="Document drift"
              meta={
                snapshot.oldest_drift_age_seconds > 0
                  ? `Oldest ${Math.floor(snapshot.oldest_drift_age_seconds)} seconds`
                  : "No outstanding drift"
              }
            />
            <div className="mini-table">
              {snapshot.drift_classes.map((drift) => (
                <div className="mini-table-row" key={drift.class}>
                  <span>{drift.class.replaceAll("_", " ")}</span>
                  <strong>{drift.count}</strong>
                </div>
              ))}
            </div>
          </section>
          {snapshot.latest_run && (
            <section
              className="overview-panel"
              aria-label="Latest reconciliation run"
            >
              <SectionHeading title="Latest reconciliation run" />
              <div className="mini-table">
                <div className="mini-table-row">
                  <span>Status</span>
                  <strong>{snapshot.latest_run.status}</strong>
                </div>
                <div className="mini-table-row">
                  <span>Local run date</span>
                  <strong>{snapshot.latest_run.local_run_date}</strong>
                </div>
                <div className="mini-table-row">
                  <span>Selected / embedded</span>
                  <strong>
                    {snapshot.latest_run.selected_count} /{" "}
                    {snapshot.latest_run.embedded_count}
                  </strong>
                </div>
                <div className="mini-table-row">
                  <span>Updated / remaining drift</span>
                  <strong>
                    {snapshot.latest_run.updated_count} /{" "}
                    {snapshot.latest_run.drifted_count}
                  </strong>
                </div>
                {snapshot.latest_run.last_error && (
                  <div className="mini-table-row">
                    <span>Last error</span>
                    <strong>{snapshot.latest_run.last_error}</strong>
                  </div>
                )}
              </div>
            </section>
          )}
          <section
            className="overview-panel"
            aria-label="Legacy embedding job diagnostics"
          >
            <SectionHeading
              title="Legacy embedding jobs — temporary until T09"
              meta="Compatibility diagnostics; not convergence authority"
            />
            <div
              className="summary-strip"
              aria-label="Legacy embedding job summary"
            >
              <SummaryCard
                label="Queued"
                value={snapshot.queue.queued}
                detail="Awaiting legacy worker"
              />
              <SummaryCard
                label="Failed"
                value={snapshot.queue.failed}
                detail={`${snapshot.queue.affected_team_count} affected teams`}
                tone={snapshot.queue.failed > 0 ? "warning" : "neutral"}
              />
              <SummaryCard
                label="Failure groups"
                value={snapshot.failure_group_count}
                detail={
                  snapshot.failure_groups_truncated
                    ? `Showing first ${snapshot.failure_groups.length}`
                    : "Operator visibility"
                }
                tone={snapshot.failure_group_count > 0 ? "warning" : "neutral"}
              />
            </div>
            {snapshot.failure_groups.length === 0 ? (
              <p className="form-meta">No unresolved failure groups.</p>
            ) : (
              <div className="mini-table">
                <div className="mini-table-row" aria-hidden="true">
                  <span>Team / reason</span>
                  <span>Failed / recovering</span>
                  <span>Guidance</span>
                </div>
                {snapshot.failure_groups.map((group) => (
                  <div
                    className="mini-table-row"
                    key={[
                      group.team_id,
                      group.source_kind,
                      group.failure_class,
                      group.failure_code,
                    ].join(":")}
                  >
                    <span>
                      <strong>{group.team_name || group.team_id}</strong>
                      <br />
                      {group.failure_code}
                    </span>
                    <span>
                      {group.failed_job_count} /{" "}
                      {group.queued_job_count + group.processing_job_count}
                    </span>
                    <span>{group.guidance}</span>
                  </div>
                ))}
              </div>
            )}
            {snapshot.failure_groups_truncated && (
              <p className="form-meta">
                Showing the most recent {snapshot.failure_groups.length} of{" "}
                {snapshot.failure_group_count} failure groups.
              </p>
            )}
          </section>
        </>
      )}
    </section>
  );
}
