import { useCallback, useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import type { ControlApi, OntologyMaintenanceRunPage, OntologyMaintenanceStatus } from "../api";
import { LoadingState, SectionHeading } from "../ui/components";
import { formatDate, readError } from "./utils";

export function OntologyMaintenancePanel({ api }: { api: ControlApi }) {
  const [status, setStatus] = useState<OntologyMaintenanceStatus | null>(null);
  const [runs, setRuns] = useState<OntologyMaintenanceRunPage>({ runs: [] });
  const [batches, setBatches] = useState(1);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [refreshError, setRefreshError] = useState("");
  const [message, setMessage] = useState("");
  const [retryKey, setRetryKey] = useState<{ intent: string; key: string } | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [nextStatus, nextRuns] = await Promise.all([api.getOntologyMaintenanceStatus(), api.listOntologyMaintenanceRuns()]);
      setStatus(nextStatus);
      setRuns((previous) => {
        if (previous.runs.length <= nextRuns.runs.length) return nextRuns;
        const current = new Map(nextRuns.runs.map((run) => [run.id, run]));
        return { runs: [...nextRuns.runs, ...previous.runs.filter((run) => !current.has(run.id))], next_cursor: previous.next_cursor };
      });
      setRefreshError("");
    } catch (err) {
      setRefreshError(readError(err));
    }
  }, [api]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 10000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  async function command(action: "run" | "pause" | "resume", runId?: string) {
    const intent = `${action}:${runId ?? ""}:${batches}`;
    const key = retryKey?.intent === intent ? retryKey.key
      : globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
    setRetryKey({ intent, key });
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const run = action === "run"
        ? await api.runOntologyMaintenance(key, batches, runId)
        : await api.pauseOntologyMaintenance(key, action === "pause");
      setMessage(action === "run" ? `Run ${run.id.slice(0, 8)} accepted · ${run.status}` : action === "pause" ? "Maintenance paused; dispatched work will finish." : "Maintenance resumed.");
      setRetryKey(null);
      await refresh();
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  async function loadMore() {
    if (!runs.next_cursor) return;
    try {
      const page = await api.listOntologyMaintenanceRuns(runs.next_cursor);
      setRuns((previous) => ({ runs: [...previous.runs, ...page.runs], next_cursor: page.next_cursor }));
    } catch (err) {
      setError(readError(err));
    }
  }

  const windowState = status?.window;
  const oldestAge = status?.oldest_pending_at ? Math.max(0, Math.floor((Date.now() - Date.parse(status.oldest_pending_at)) / 60000)) : null;

  return (
    <section className="surface">
      <SectionHeading title="Ontology coverage" actions={(
        <button className="icon-button" type="button" aria-label="Refresh ontology coverage" onClick={() => void refresh()}><RefreshCw size={16} aria-hidden="true" /></button>
      )} />
      {error && <div className="banner error" role="alert">{error}</div>}
      {refreshError && <div className="banner error" role="alert">{refreshError}</div>}
      {message && <div className="banner neutral" role="status">{message}</div>}
      {!status ? <LoadingState label="Loading ontology coverage" compact /> : (
        <>
          <p>{status.paused ? "Paused" : status.enabled ? "Enabled" : "Disabled"} · {status.coverage_complete ? "Coverage complete" : "Coverage incomplete"} · {status.discovery_complete ? "Discovery complete" : "Discovery in progress"}</p>
          <dl className="summary-grid">
            {Object.entries(status.counts).map(([key, value]) => (
              <div key={key}><dt>{key === "budget_deferred" ? "Budget deferred" : key[0].toUpperCase() + key.slice(1)}</dt><dd>{value.toLocaleString()}</dd></div>
            ))}
          </dl>
          <p className="form-meta">Snapshot {formatDate(status.observed_at)} · Oldest pending {oldestAge === null ? "none" : `${oldestAge} min`} · Last successful progress {status.last_successful_progress ? formatDate(status.last_successful_progress) : "none"}</p>
          {!status.discovery_complete && <p>Eligible counts cover discovered records while the remaining corpus is scanned.</p>}
          {windowState ? (
            <>
              <p>Active window {formatDate(windowState.starts_at)} – {formatDate(windowState.ends_at)} · {windowState.policy.model}</p>
              <p>Active policy: {windowState.policy.cadence_hours} hours · {windowState.policy.max_concurrency} concurrent assessment{windowState.policy.max_concurrency === 1 ? "" : "s"}.</p>
              <table className="data-table"><thead><tr><th>Tokens</th><th>Charged / allowance</th><th>Reported</th><th>Unreported reservation</th></tr></thead><tbody>
                <tr><th>Input</th><td>{windowState.charged_input_tokens.toLocaleString()} / {windowState.policy.input_tokens.toLocaleString()}</td><td>{windowState.reported_input_tokens.toLocaleString()}</td><td>{windowState.reserved_input_tokens.toLocaleString()}</td></tr>
                <tr><th>Output</th><td>{windowState.charged_output_tokens.toLocaleString()} / {windowState.policy.output_tokens.toLocaleString()}</td><td>{windowState.reported_output_tokens.toLocaleString()}</td><td>{windowState.reserved_output_tokens.toLocaleString()}</td></tr>
              </tbody></table>
              {windowState.overrun && <div className="banner error" role="alert">Provider usage exceeded a reservation. New admission is stopped for this window.</div>}
            </>
          ) : <p>No maintenance window has opened.</p>}
          <p>Pending policy: {status.pending_policy.cadence_hours} hours from {status.pending_policy.start_time_local} ({status.pending_policy.timezone}), {status.pending_policy.model}, {status.pending_policy.max_concurrency} concurrent assessment{status.pending_policy.max_concurrency === 1 ? "" : "s"}, {status.pending_policy.input_tokens.toLocaleString()} input / {status.pending_policy.output_tokens.toLocaleString()} output tokens per window.</p>
          <div className="button-row">
            <label htmlFor="ontology-batches">Maximum batches</label><input id="ontology-batches" type="number" min="1" max="100" value={batches} onChange={(event) => setBatches(Number(event.target.value))} />
            <button className="primary-button" type="button" disabled={busy || !status.enabled || status.paused || !Number.isInteger(batches) || batches < 1 || batches > 100} onClick={() => void command("run")}>Run bounded maintenance</button>
            <button className="secondary-button" type="button" disabled={busy} onClick={() => void command(status.paused ? "resume" : "pause")}>{status.paused ? "Resume maintenance" : "Pause maintenance"}</button>
          </div>
          <p>Manual runs and retries share the current window allowance.</p>
          <table className="data-table"><thead><tr><th>Run</th><th>Kind</th><th>Status</th><th>Batches</th><th>Reason</th><th>Action</th></tr></thead><tbody>
            {runs.runs.map((run) => <tr key={run.id}>
              <td title={run.id}>{run.id.slice(0, 8)}</td><td>{run.kind}</td><td>{run.status}</td><td>{run.completed_batches}{run.max_batches ? ` / ${run.max_batches}` : ""}</td><td>{run.failure_code ?? "—"}</td>
              <td>{run.retryable && <button type="button" disabled={busy || !status.enabled || status.paused} onClick={() => void command("run", run.id)}>Retry failed work</button>}</td>
            </tr>)}
          </tbody></table>
          {runs.next_cursor && <button type="button" onClick={() => void loadMore()}>Load older runs</button>}
        </>
      )}
    </section>
  );
}
