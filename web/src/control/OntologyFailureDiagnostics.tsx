import { useEffect, useRef, useState } from "react";
import type { ControlApi, OperationLog } from "../api";
import { Button } from "../ui/primitives/button";
import { LoadingState, SectionHeading } from "../ui/components";
import { formatDate, readError } from "./utils";

type Body = { body: string; state: string; reason: string };
type Exchange = { index: number; component: string; model: string; status: number; request: Body; response: Body };
type Chunk = { index: number; count: number; body: string; sha256: string; state: string; reason?: number };

export async function reconstructOntologyExchanges(manifest: OperationLog, rows: OperationLog[]): Promise<Exchange[]> {
  const expected = Number(manifest.attrs?.record_count);
  const exchangeCount = Number(manifest.attrs?.exchange_count);
  if (!Number.isInteger(expected) || expected < 0 || rows.length !== expected || !Number.isInteger(exchangeCount) || exchangeCount < 0 || exchangeCount > 3) throw new Error("The capture is incomplete or expired; complete bodies are unavailable.");
  const result: Exchange[] = [];
  for (let exchange = 0; exchange < exchangeCount; exchange += 1) {
    const parts = rows.filter((row) => row.attrs?.exchange === exchange);
    const first = parts[0]?.attrs;
    if (!first) throw new Error("An expected provider exchange is missing.");
    const bodies: Body[] = [];
    for (const direction of ["request", "response"]) {
      const chunks = parts.filter((row) => row.attrs?.direction === direction).map((row) => row.attrs?.payload_chunk as Chunk).sort((a, b) => a.index - b.index);
      const head = chunks[0];
      if (!head || chunks.length !== head.count || chunks.some((chunk, index) => chunk.index !== index || chunk.count !== head.count || chunk.sha256 !== head.sha256 || chunk.state !== head.state)) throw new Error("The capture has missing or inconsistent chunks.");
      const reason = head.reason ? `credential_protection_${head.reason}` : String(first.capture_reason || "");
      const captureState = head.state === "captured" ? String(first.capture_state || "captured") : head.state;
      const state = reason === "credential_protection_2" ? "over_budget" : captureState;
      if (head.state !== "captured") { bodies.push({ body: "", state, reason }); continue; }
      const body = chunks.map((chunk) => chunk.body).join("");
      if (!globalThis.crypto?.subtle) throw new Error("Body integrity verification requires a secure browser context.");
      const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(body));
      const hash = Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
      if (hash !== head.sha256) throw new Error("Body integrity verification failed; the capture will not be displayed.");
      bodies.push({ body, state, reason });
    }
    result.push({ index: exchange, component: String(first.component || "provider"), model: String(first.model || ""), status: Number(first.status_code || 0), request: bodies[0], response: bodies[1] });
  }
  if (rows.some((row) => row.team_id !== manifest.team_id || row.attrs?.attempt_id !== manifest.attrs?.attempt_id || row.attrs?.reference_id !== manifest.attrs?.reference_id || !Number.isInteger(row.attrs?.exchange) || Number(row.attrs?.exchange) < 0 || Number(row.attrs?.exchange) >= exchangeCount)) throw new Error("The capture identity does not match the selected failure.");
  return result;
}

export function OntologyFailureDiagnostics({ api, runId }: { api: ControlApi; runId: string }) {
  const [failures, setFailures] = useState<OperationLog[]>([]);
  const [total, setTotal] = useState(0);
  const [selected, setSelected] = useState<OperationLog | null>(null);
  const [exchanges, setExchanges] = useState<Exchange[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const sequence = useRef(0);

  useEffect(() => {
    let active = true;
    setLoading(true);
    void api.listOperationLogs({ event: "ontology_failure_capture", reference_type: "ontology_run", reference_id: runId, limit: 20 }).then((page) => {
      if (active) { setFailures(page.data); setTotal(page.pagination.total); }
    }).catch((err) => { if (active) setError(readError(err)); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; sequence.current += 1; };
  }, [api, runId]);

  async function open(failure: OperationLog) {
    const current = ++sequence.current;
    setSelected(failure); setExchanges([]); setError(""); setLoading(true);
    try {
      const count = Number(failure.attrs?.record_count);
      const captureID = String(failure.attrs?.attempt_id || "");
      if (!failure.team_id || !captureID || !Number.isInteger(count) || count < 0 || count > 40000) throw new Error("This capture manifest is unavailable.");
      const rows: OperationLog[] = [];
      while (rows.length < count) {
        const page = await api.listOperationLogs({ event: "ontology_failure_payload", team_id: failure.team_id, reference_type: "ontology_run", reference_id: runId, attempt_id: captureID, limit: 100, offset: rows.length, direction: "asc" });
        if (current !== sequence.current) return;
        if (page.pagination.total !== count || page.data.length === 0) throw new Error("The capture is incomplete or expired; complete bodies are unavailable.");
        rows.push(...page.data);
      }
      const value = await reconstructOntologyExchanges(failure, rows);
      if (current === sequence.current) setExchanges(value);
    } catch (err) { if (current === sequence.current) setError(readError(err)); }
    finally { if (current === sequence.current) setLoading(false); }
  }

  async function more() {
    setLoading(true); setError("");
    try {
      const page = await api.listOperationLogs({ event: "ontology_failure_capture", reference_type: "ontology_run", reference_id: runId, limit: 20, offset: failures.length });
      setFailures((items) => [...items, ...page.data]); setTotal(page.pagination.total);
    } catch (err) { setError(readError(err)); }
    finally { setLoading(false); }
  }

  return <section className="surface" aria-label="Ontology failure diagnostics">
    <SectionHeading title="Ontology failure diagnostics" />
    {error && <div className="banner error" role="alert">{error}</div>}
    {loading && <LoadingState label="Loading ontology captures" compact />}
    {!loading && failures.length === 0 && <p>No retained failure captures are available for this run. Earlier failures may predate capture or have expired.</p>}
    {failures.map((failure) => <div className="button-row" key={failure.id}><span>{formatDate(failure.timestamp)} · {String(failure.attrs?.failure_code || "failure")} · {failure.team_id}</span><Button variant="outline" type="button" disabled={loading} onClick={() => void open(failure)}>View failed assessment {String(failure.attrs?.assessment_id || failure.attrs?.batch_id).slice(0, 8)}</Button></div>)}
    {failures.length < total && <Button variant="outline" disabled={loading} onClick={() => void more()}>Load older failures</Button>}
    {selected && !loading && <>
      <p>Assessment {String(selected.attrs?.assessment_id || "unavailable")} · Batch {String(selected.attrs?.batch_id || "unavailable")}</p>
      <details><summary>Validation and provider attempts</summary><pre>{JSON.stringify(selected.attrs?.attempts ?? [], null, 2)}</pre></details>
      {!error && exchanges.length === 0 && <p>No provider exchanges were captured for this failed operation.</p>}
      {exchanges.map((exchange) => <article className="remember-diagnostic-exchange" key={exchange.index}>
        <h3>Exchange {exchange.index + 1} · {exchange.component} · {exchange.model} {exchange.status ? `· HTTP ${exchange.status}` : ""}</h3>
        <FailureBody label="AI request" value={exchange.request} />
        <FailureBody label="AI response" value={exchange.response} />
      </article>)}
    </>}
  </section>;
}

function FailureBody({ label, value }: { label: string; value: Body }) {
  return <div className="remember-diagnostic-body"><strong>{label}</strong>
    {value.state !== "captured" && <p className="banner warning">{value.state === "over_budget" ? "The body exceeded the diagnostic capture budget." : `Capture state: ${value.state}. The body may be missing or incomplete.`}{value.reason && ` Reason: ${value.reason}.`}</p>}
    {value.body && <pre>{value.body}</pre>}
  </div>;
}
