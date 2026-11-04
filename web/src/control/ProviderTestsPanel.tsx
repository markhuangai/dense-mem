import { useCallback, useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { ControlApi, type ProviderTestResult, type ProviderTestTarget } from "../api";
import { Button } from "../ui/primitives/button";
import { LoadingState, SectionHeading } from "../ui/components";
import { formatDate, readError } from "./utils";
import "./provider-tests.css";

type TestState = { result?: ProviderTestResult; error?: string; cancelled?: boolean };

export function ProviderTestsPanel({ api }: { api: ControlApi }) {
  const [targets, setTargets] = useState<ProviderTestTarget[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [running, setRunning] = useState<Set<string>>(new Set());
  const [results, setResults] = useState<Record<string, TestState>>({});
  const requests = useRef(new Map<string, AbortController>());
  const active = useRef(true);
  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    setError("");
    try {
      const values = await api.listProviderTestTargets(signal);
      if (active.current) setTargets(values);
    } catch (err) {
      if (active.current && !signal?.aborted) setError(readError(err));
    } finally {
      if (active.current) setLoading(false);
    }
  }, [api]);

  useEffect(() => {
    active.current = true;
    const controller = new AbortController();
    const pending = requests.current;
    void load(controller.signal);
    return () => {
      active.current = false;
      controller.abort();
      for (const request of pending.values()) request.abort();
      pending.clear();
    };
  }, [load]);

  async function run(target: ProviderTestTarget) {
    const id = target.target_id;
    if (requests.current.has(id)) return;
    const controller = new AbortController();
    requests.current.set(id, controller);
    setRunning((current) => new Set(current).add(id));
    setResults((current) => ({ ...current, [id]: {} }));
    try {
      const result = await api.runProviderTest(id, controller.signal);
      if (active.current) setResults((current) => ({ ...current, [id]: { result } }));
    } catch (err) {
      if (active.current) setResults((current) => ({ ...current, [id]: controller.signal.aborted ? { cancelled: true } : { error: readError(err) } }));
    } finally {
      requests.current.delete(id);
      if (active.current) setRunning((current) => { const next = new Set(current); next.delete(id); return next; });
    }
  }

  const endpoints = new Map<string, ProviderTestTarget[]>();
  for (const target of targets ?? []) {
    const group = endpoints.get(target.endpoint) ?? [];
    group.push(target);
    endpoints.set(target.endpoint, group);
  }
  return <section className="surface provider-tests" aria-label="AI provider tests">
    <SectionHeading title="AI providers" actions={<Button variant="ghost" type="button" aria-label="Refresh AI providers" disabled={loading || running.size > 0} onClick={() => void load()}><RefreshCw size={16} aria-hidden="true" />Refresh</Button>} />
    <p className="panel-intro">Send a built-in sample from the hosting server through the application’s existing provider logic. Each test checks the listed endpoint and model and may incur provider charges.</p>
    {error && <div className="banner error" role="alert">{error}</div>}
    {loading && <LoadingState label="Loading AI providers" />}
    {!loading && targets?.length === 0 && <p>No AI provider configurations are available.</p>}
    {Array.from(endpoints, ([endpoint, entries]) => <section className="provider-endpoint" key={endpoint} aria-label={`Endpoint ${endpoint || "not configured"}`}>
      <h3><code>{endpoint || "Endpoint not configured"}</code></h3>
      {entries.map((target) => <article className="provider-test-target" key={target.target_id} aria-label={`${target.model || "Unconfigured model"} (${target.operation})`}>
        <div className="provider-test-heading">
          <div><h4>{target.model || "Model not configured"}</h4><p className="form-meta">{target.roles.join(" · ")}</p><p className="form-meta">{operationName(target.operation)} sample · {target.credential_configuration} · Timeout {target.timeout_ms / 1000}s</p></div>
          <div className="button-row">
            <Button type="button" disabled={!target.available || running.has(target.target_id)} aria-label={`Send test request for ${target.model || "unconfigured model"} (${target.operation})`} onClick={() => void run(target)}>{running.has(target.target_id) ? "Testing…" : "Send test request"}</Button>
            {running.has(target.target_id) && <Button variant="ghost" type="button" onClick={() => requests.current.get(target.target_id)?.abort()}>Cancel request</Button>}
          </div>
        </div>
        {!target.available && <p className="form-meta">{target.unavailable_reason}</p>}
        {running.has(target.target_id) && <p role="status">Waiting for the server’s test result…</p>}
        {results[target.target_id]?.cancelled && <p role="status">Request cancelled. Provider completion is unknown; check server logs for the final outcome.</p>}
        {results[target.target_id]?.error && <div className="banner error" role="alert">{results[target.target_id].error}</div>}
        {results[target.target_id]?.result && <ProviderResult result={results[target.target_id].result!} />}
      </article>)}
    </section>)}
  </section>;
}

function operationName(operation: ProviderTestTarget["operation"]) {
  return { assess: "Remember assessment", embed: "Embedding", decide: "Typed decision" }[operation];
}

function duration(value: number | null) { return value === null ? "Not observed" : `${value} ms`; }

function ProviderResult({ result }: { result: ProviderTestResult }) {
  return <div className="provider-test-result">
    <p role="status"><strong>{result.outcome === "succeeded" ? "Sample succeeded" : result.outcome === "cancelled" ? "Test cancelled" : "Sample failed"}</strong> · {result.reason.replaceAll("_", " ")}</p>
    <p>{result.guidance}</p>
    <p className="form-meta">{result.network_state === "http_response_received" ? "HTTP response received" : result.network_state === "not_dispatched" ? "No request dispatched" : "No HTTP response observed"}. This sample does not certify every consuming workflow.</p>
    <dl className="key-detail-grid">
      <div><dt>Server</dt><dd>{result.server_instance}</dd></div>
      <div><dt>Started</dt><dd>{formatDate(result.started_at)}</dd></div>
      <div><dt>Duration</dt><dd>{duration(result.duration_ms)}</dd></div>
      <div><dt>Last call queue</dt><dd>{duration(result.diagnostics?.queue_ms ?? null)}</dd></div>
      <div><dt>HTTP status</dt><dd>{result.http_status ?? "Not observed"}</dd></div>
      <div><dt>Failure phase</dt><dd>{result.failure_phase?.replaceAll("_", " ") || "None"}</dd></div>
      <div><dt>Provider exchanges</dt><dd>{result.exchanges.length}</dd></div>
      <div><dt>Correlation ID</dt><dd><code>{result.correlation_id}</code></dd></div>
      <div><dt>Test ID</dt><dd><code>{result.test_id}</code></dd></div>
    </dl>
    {result.error && <p className="provider-test-error">{result.error}</p>}
    {result.exchanges.map((exchange, index) => <details key={exchange.exchange_id}>
      <summary>Exchange {index + 1}: {exchange.status_code ? `HTTP ${exchange.status_code}` : "No HTTP response"}</summary>
      <dl className="key-detail-grid">
        <div><dt>DNS</dt><dd>{duration(exchange.dns_ms)}</dd></div>
        <div><dt>Connection</dt><dd>{duration(exchange.connect_ms)}</dd></div>
        <div><dt>TLS</dt><dd>{duration(exchange.tls_ms)}</dd></div>
        <div><dt>First byte</dt><dd>{duration(exchange.first_byte_ms)}</dd></div>
        <div><dt>Connection reused</dt><dd>{exchange.connection_reused === null ? "Not observed" : exchange.connection_reused ? "Yes" : "No"}</dd></div>
        <div><dt>Capture</dt><dd>{exchange.request_capture_state} / {exchange.response_capture_state}{exchange.capture_reason ? ` · ${exchange.capture_reason}` : ""}</dd></div>
      </dl>
      {exchange.error && <p className="provider-test-error">{exchange.error}</p>}
    </details>)}
    {result.capture_state !== "captured" && <p className="form-meta">Diagnostic capture is incomplete. See the exchange capture reasons.</p>}
    {!result.detailed_logs_available && <p className="form-meta">Full exchange logs are unavailable at the current log level. They require DEBUG or TRACE.</p>}
  </div>;
}
