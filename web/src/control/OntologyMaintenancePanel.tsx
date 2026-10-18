import { Button } from "../ui/primitives/button";
import { Table, TableHeader, TableRow, TableHead, TableBody, TableCell } from "../ui/primitives/table";
import { Label } from "../ui/primitives/label";
import { Input } from "../ui/primitives/input";
import { useCallback, useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import type { ControlApi, OntologyMaintenanceRunPage, OntologyMaintenanceStatus } from "../api";
import { LoadingState, SectionHeading } from "../ui/components";
import { formatDate, readError } from "./utils";

export function OntologyMaintenancePanel({ api }: { api: ControlApi }) {
  const [status, setStatus] = useState<OntologyMaintenanceStatus | null>(null);
  const [runs, setRuns] = useState<OntologyMaintenanceRunPage>({ runs: [] });
  const [batches, setBatches] = useState(1);
  const [additionalInput, setAdditionalInput] = useState(0);
  const [additionalOutput, setAdditionalOutput] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [refreshError, setRefreshError] = useState("");
  const [message, setMessage] = useState("");
  const [retryKey, setRetryKey] = useState<{ intent: string; key: string } | null>(null);

  const [historyLoading, setHistoryLoading] = useState(false);
  const historyLoad = useRef<Promise<void> | null>(null);
  const loadedPages = useRef(1);
  const currentRuns = useRef<OntologyMaintenanceRunPage>({ runs: [] });

  const loadHistory = useCallback((more = false): Promise<void> => {
    if (historyLoad.current) return more ? historyLoad.current.then(() => loadHistory(true)) : historyLoad.current;
    if (more && !currentRuns.current.next_cursor) return Promise.resolve();
    setHistoryLoading(true);
    const request = Promise.resolve().then(async () => {
      try {
        let nextRuns: OntologyMaintenanceRunPage;
        if (more) {
          const page = await api.listOntologyMaintenanceRuns(currentRuns.current.next_cursor);
          nextRuns = { runs: [...currentRuns.current.runs, ...page.runs], next_cursor: page.next_cursor };
          loadedPages.current += 1;
        } else {
          const [nextStatus, firstPage] = await Promise.all([api.getOntologyMaintenanceStatus(), api.listOntologyMaintenanceRuns()]);
          nextRuns = firstPage;
          for (let page = 1; page < loadedPages.current && nextRuns.next_cursor; page += 1) {
            const older = await api.listOntologyMaintenanceRuns(nextRuns.next_cursor);
            nextRuns = { runs: [...nextRuns.runs, ...older.runs], next_cursor: older.next_cursor };
          }
          setStatus(nextStatus);
        }
        nextRuns = { ...nextRuns, runs: [...new Map(nextRuns.runs.map((run) => [run.id, run])).values()] };
        currentRuns.current = nextRuns;
        setRuns(nextRuns);
        if (!more) setRefreshError("");
      } catch (err) {
        setRefreshError(readError(err));
      }
    }).finally(() => {
      historyLoad.current = null;
      setHistoryLoading(false);
    });
    historyLoad.current = request;
    return request;
  }, [api]);
  const refresh = useCallback(() => loadHistory(), [loadHistory]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 10000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  async function command(action: "run" | "pause" | "resume" | "allowance", runId?: string) {
    const windowId = status?.window?.id;
    if (action === "allowance" && !windowId) return;
    const intent = action === "allowance" ? `${action}:${windowId}:${additionalInput}:${additionalOutput}` : `${action}:${runId ?? ""}:${batches}`;
    const key = retryKey?.intent === intent ? retryKey.key
      : globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
    setRetryKey({ intent, key });
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const run = action === "allowance"
        ? await api.addOntologyMaintenanceAllowance(key, windowId!, additionalInput, additionalOutput)
        : action === "run"
        ? await api.runOntologyMaintenance(key, batches, runId)
        : await api.pauseOntologyMaintenance(key, action === "pause");
      setMessage(action === "allowance" ? "Allowance added to this window. Automatic maintenance continues on its next minute tick." : action === "run" ? `Run ${run.id.slice(0, 8)} accepted · ${run.status}` : action === "pause" ? "Maintenance paused; dispatched work will finish." : "Maintenance resumed.");
      if (action === "allowance") {
        setAdditionalInput(0);
        setAdditionalOutput(0);
      }
      setRetryKey(null);
      await historyLoad.current;
      await refresh();
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  async function loadMore() {
    await loadHistory(true);
  }

  const windowState = status?.window;
  const allowanceInvalid = ![additionalInput, additionalOutput].every((amount) => Number.isInteger(amount) && amount >= 0 && amount <= 1000000000)
    || additionalInput + additionalOutput === 0
    || additionalInput + (windowState?.additional_input_tokens ?? 0) > 1000000000
    || additionalOutput + (windowState?.additional_output_tokens ?? 0) > 1000000000;
  const oldestAge = status?.oldest_pending_at ? Math.max(0, Math.floor((Date.now() - Date.parse(status.oldest_pending_at)) / 60000)) : null;

  return (
    <section className="surface">
      <SectionHeading title="Ontology coverage" actions={(
        <Button variant="ghost" size="icon" className="icon-button" type="button" aria-label="Refresh ontology coverage" disabled={historyLoading} onClick={() => void refresh()}><RefreshCw size={16} aria-hidden="true" /></Button>
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
              <Table className="data-table"><TableHeader><TableRow><TableHead>Tokens</TableHead><TableHead>Charged / allowance</TableHead><TableHead>Reported</TableHead><TableHead>Unreported reservation</TableHead></TableRow></TableHeader><TableBody>
                <TableRow><TableHead>Input</TableHead><TableCell>{windowState.charged_input_tokens.toLocaleString()} / {(windowState.policy.input_tokens + (windowState.additional_input_tokens ?? 0)).toLocaleString()}</TableCell><TableCell>{windowState.reported_input_tokens.toLocaleString()}</TableCell><TableCell>{windowState.reserved_input_tokens.toLocaleString()}</TableCell></TableRow>
                <TableRow><TableHead>Output</TableHead><TableCell>{windowState.charged_output_tokens.toLocaleString()} / {(windowState.policy.output_tokens + (windowState.additional_output_tokens ?? 0)).toLocaleString()}</TableCell><TableCell>{windowState.reported_output_tokens.toLocaleString()}</TableCell><TableCell>{windowState.reserved_output_tokens.toLocaleString()}</TableCell></TableRow>
              </TableBody></Table>
              {windowState.overrun && <div className="banner error" role="alert">Provider usage exceeded a reservation. New admission is stopped for this window.</div>}
              <p>Added allowance: {(windowState.additional_input_tokens ?? 0).toLocaleString()} input / {(windowState.additional_output_tokens ?? 0).toLocaleString()} output tokens. Original policy, usage, and reservations are retained.</p>
              <div className="button-row">
                <Label htmlFor="ontology-additional-input">Additional input tokens</Label><Input id="ontology-additional-input" type="number" min="0" max="1000000000" value={additionalInput} onChange={(event) => setAdditionalInput(Number(event.target.value))} />
                <Label htmlFor="ontology-additional-output">Additional output tokens</Label><Input id="ontology-additional-output" type="number" min="0" max="1000000000" value={additionalOutput} onChange={(event) => setAdditionalOutput(Number(event.target.value))} />
                <Button variant="outline" className="secondary-button" type="button" disabled={busy || historyLoading || Boolean(refreshError) || !status.enabled || status.paused || windowState.overrun || Date.parse(windowState.ends_at) <= Date.parse(status.observed_at) || allowanceInvalid} onClick={() => void command("allowance")}>Add allowance now</Button>
              </div>
              <p>This authorizes additional model usage for the current window only. All teams share it; queued work keeps its existing order.</p>
            </>
          ) : <p>No maintenance window has opened.</p>}
          <p>Pending policy: {status.pending_policy.cadence_hours} hours from {status.pending_policy.start_time_local} ({status.pending_policy.timezone}), {status.pending_policy.model}, {status.pending_policy.max_concurrency} concurrent assessment{status.pending_policy.max_concurrency === 1 ? "" : "s"}, {status.pending_policy.input_tokens.toLocaleString()} input / {status.pending_policy.output_tokens.toLocaleString()} output tokens per window.</p>
          <div className="button-row">
            <Label htmlFor="ontology-batches">Maximum batches</Label><Input id="ontology-batches" type="number" min="1" max="100" value={batches} onChange={(event) => setBatches(Number(event.target.value))} />
            <Button variant="default" className="primary-button" type="button" disabled={busy || !status.enabled || status.paused || !Number.isInteger(batches) || batches < 1 || batches > 100} onClick={() => void command("run")}>Run bounded maintenance</Button>
            <Button variant="outline" className="secondary-button" type="button" disabled={busy} onClick={() => void command(status.paused ? "resume" : "pause")}>{status.paused ? "Resume maintenance" : "Pause maintenance"}</Button>
          </div>
          <p>Manual runs and retries share the current window allowance.</p>
          <Table className="data-table"><TableHeader><TableRow><TableHead>Run</TableHead><TableHead>Kind</TableHead><TableHead>Status</TableHead><TableHead>Batches / added tokens</TableHead><TableHead>Reason</TableHead><TableHead>Action</TableHead></TableRow></TableHeader><TableBody>
            {runs.runs.map((run) => <TableRow key={run.id}>
              <TableCell title={run.id}>{run.id.slice(0, 8)}</TableCell><TableCell>{run.kind}</TableCell><TableCell>{run.status}</TableCell><TableCell>{run.kind === "allowance" ? `${(run.input_tokens ?? 0).toLocaleString()} input / ${(run.output_tokens ?? 0).toLocaleString()} output` : `${run.completed_batches}${run.max_batches ? ` / ${run.max_batches}` : ""}`}</TableCell><TableCell>{run.failure_code ?? "—"}</TableCell>
              <TableCell>{run.retryable && <Button variant="outline" type="button" disabled={busy || historyLoading || Boolean(refreshError) || !status.enabled || status.paused} onClick={() => void command("run", run.id)}>Retry failed work</Button>}</TableCell>
            </TableRow>)}
          </TableBody></Table>
          {runs.next_cursor && <Button variant="outline" type="button" disabled={historyLoading || busy} onClick={() => void loadMore()}>Load older runs</Button>}
        </>
      )}
    </section>
  );
}
