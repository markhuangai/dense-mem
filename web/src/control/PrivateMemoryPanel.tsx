import { useEffect, useRef, useState } from "react";
import { Lock, Play, RefreshCw, Trash2, Unlock } from "lucide-react";
import {
  ControlApi,
  PrivateMemoryConfig,
  PrivateMemoryOperation,
  PrivateMemoryRetentionRun,
  PrivateMemorySpace,
} from "../api";
import { LoadingState, SectionHeading, SummaryCard } from "../ui/components";
import { formatDate, readError, shortId } from "./utils";

export function PrivateMemoryPanel({ api }: { api: ControlApi }) {
  const [config, setConfig] = useState<PrivateMemoryConfig | null>(null);
  const [spaces, setSpaces] = useState<PrivateMemorySpace[]>([]);
  const [operations, setOperations] = useState<PrivateMemoryOperation[]>([]);
  const [runs, setRuns] = useState<PrivateMemoryRetentionRun[]>([]);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const idempotencyKeys = useRef(new Map<string, string>());

  async function load() {
    setBusy(true);
    setError("");
    try {
      const [nextConfig, spacePage, operationPage, runPage] = await Promise.all([
        api.getPrivateMemoryConfig(),
        api.listPrivateMemorySpaces(),
        api.listPrivateMemoryErasures(),
        api.listPrivateMemoryRetentionRuns(),
      ]);
      setConfig(nextConfig);
      setSpaces(spacePage.data);
      setOperations(operationPage.data);
      setRuns(runPage.data);
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  async function placeHold(space: PrivateMemorySpace) {
    const reason = window.prompt("Legal hold reason code", "legal_hold")?.trim();
    if (!reason) {
      return;
    }
    await mutate(async () => {
      await api.placePrivateMemoryLegalHold(space.id, reason);
      setMessage(`Legal hold placed on ${shortId(space.id)}.`);
    });
  }

  async function releaseHold(space: PrivateMemorySpace) {
    if (!window.confirm(`Release the legal hold on ${shortId(space.id)}? Erasure requests can proceed immediately after release.`)) {
      return;
    }
    await mutate(async () => {
      await api.releasePrivateMemoryLegalHold(space.id);
      setMessage(`Legal hold released on ${shortId(space.id)}.`);
    });
  }

  async function eraseSpace(space: PrivateMemorySpace) {
    if (!window.confirm(`Permanently erase ${space.kind} memory in ${shortId(space.id)}? This cannot be undone without an authorized backup restore.`)) {
      return;
    }
    await mutate(async () => {
      const intent = privateMemoryIntent("control-erasure", space.id);
      const operation = await api.requestPrivateMemoryErasure(space.id, privateMemoryIdempotencyKey(idempotencyKeys.current, intent));
      idempotencyKeys.current.delete(intent);
      setMessage(`Erasure ${shortId(operation.operation_id)} queued.`);
    });
  }

  async function runRetention() {
    const days = config?.effective.retention_days ?? 0;
    if (days <= 0 || !window.confirm(`Queue permanent erasure for eligible private memory older than ${days} days? Legal holds remain protected.`)) {
      return;
    }
    await mutate(async () => {
      const intent = privateMemoryIntent("retention", String(days));
      const run = await api.runPrivateMemoryRetention(privateMemoryIdempotencyKey(idempotencyKeys.current, intent));
      idempotencyKeys.current.delete(intent);
      setMessage(`Retention run ${shortId(run.id)} queued ${run.queued_count} space${run.queued_count === 1 ? "" : "s"}.`);
    });
  }

  async function mutate(action: () => Promise<void>) {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await action();
      await load();
    } catch (err) {
      setError(readError(err));
      setBusy(false);
    }
  }

  const retentionDays = config?.effective.retention_days ?? 0;
  const heldSpaces = spaces.filter((space) => space.active_hold).length;
  const activeOperations = operations.filter((operation) => operation.status === "queued" || operation.status === "processing").length;

  return (
    <section className="surface private-memory-panel">
      <SectionHeading
        title="Private memory governance"
        subtitle="Control retention, legal holds, and verified physical erasure. Team-shared memory is never listed here."
        actions={(
          <button className="icon-button" type="button" aria-label="Refresh private memory" onClick={() => void load()} disabled={busy}>
            <RefreshCw size={16} aria-hidden="true" />
          </button>
        )}
      />
      {error && <div className="banner error" role="alert">{error}</div>}
      {message && <div className="banner neutral" role="status">{message}</div>}

      <div className="private-memory-summary" aria-label="Private memory status">
        {config ? (
          <>
            <SummaryCard label="Automatic retention" value={retentionDays === 0 ? "Off" : `${retentionDays} days`} />
            <SummaryCard label="Private spaces" value={spaces.length} />
            <SummaryCard label="Legal holds" value={heldSpaces} />
            <SummaryCard label="Active erasures" value={activeOperations} />
          </>
        ) : (
          <LoadingState label="Loading governance status" compact />
        )}
      </div>

      <div className="private-memory-runbar">
        <div>
          <strong>Retention sweep</strong>
          <span>{retentionDays === 0 ? "Enable retention under Config → Privacy before running a sweep." : `Queues private spaces with content older than ${retentionDays} days.`}</span>
        </div>
        <button className="danger-button" type="button" disabled={busy || retentionDays === 0} onClick={() => void runRetention()}>
          <Play size={16} aria-hidden="true" />
          Run retention
        </button>
      </div>

      <div className="list-toolbar">
        <div>
          <h3>Erasure ledger</h3>
          <span>{spaces.length} private spaces</span>
        </div>
      </div>
      <PrivateMemorySpaceTable
        spaces={spaces}
        busy={busy}
        onPlaceHold={(space) => void placeHold(space)}
        onReleaseHold={(space) => void releaseHold(space)}
        onErase={(space) => void eraseSpace(space)}
      />

      <div className="private-memory-history-grid">
        <PrivateMemoryOperationList operations={operations} />
        <PrivateMemoryRetentionList runs={runs} />
      </div>
    </section>
  );
}

function PrivateMemorySpaceTable({
  spaces,
  busy,
  onPlaceHold,
  onReleaseHold,
  onErase,
}: {
  spaces: PrivateMemorySpace[];
  busy: boolean;
  onPlaceHold: (space: PrivateMemorySpace) => void;
  onReleaseHold: (space: PrivateMemorySpace) => void;
  onErase: (space: PrivateMemorySpace) => void;
}) {
  if (spaces.length === 0) {
    return <div className="table-placeholder">No private memory spaces</div>;
  }
  return (
    <div className="table-wrap private-memory-ledger">
      <table className="data-table private-memory-table">
        <thead>
          <tr>
            <th>Space</th>
            <th>Team</th>
            <th>Owner</th>
            <th>Generation</th>
            <th>State</th>
            <th>Legal hold</th>
            <th>Updated</th>
            <th className="actions-cell">Actions</th>
          </tr>
        </thead>
        <tbody>
          {spaces.map((space) => (
            <tr key={space.id} className={space.active_hold ? "private-memory-held" : ""}>
              <td><strong>{space.kind}</strong><code title={space.id}>{shortId(space.id)}</code></td>
              <td><code title={space.team_id}>{shortId(space.team_id)}</code></td>
              <td><code title={space.owner_profile_id ?? space.owner_credential_id}>{shortId(space.owner_profile_id ?? space.owner_credential_id ?? "")}</code></td>
              <td>{space.generation}</td>
              <td><span className={`status-pill ${space.lifecycle_state === "active" ? "neutral" : "warning"}`}>{space.lifecycle_state}</span></td>
              <td>{space.active_hold ? <span className="status-pill warning" title={space.active_hold.reason_code}>Held</span> : <span className="status-pill neutral">Clear</span>}</td>
              <td>{formatDate(space.updated_at)}</td>
              <td className="actions-cell private-memory-actions">
                {space.active_hold ? (
                  <button className="icon-button" type="button" aria-label={`Release legal hold for ${space.id}`} title="Release legal hold" disabled={busy} onClick={() => onReleaseHold(space)}>
                    <Unlock size={16} aria-hidden="true" />
                  </button>
                ) : (
                  <button className="icon-button" type="button" aria-label={`Place legal hold for ${space.id}`} title="Place legal hold" disabled={busy} onClick={() => onPlaceHold(space)}>
                    <Lock size={16} aria-hidden="true" />
                  </button>
                )}
                <button className="icon-button danger" type="button" aria-label={`Erase private memory for ${space.id}`} title="Erase private memory" disabled={busy || Boolean(space.active_hold) || space.lifecycle_state !== "active"} onClick={() => onErase(space)}>
                  <Trash2 size={16} aria-hidden="true" />
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function PrivateMemoryOperationList({ operations }: { operations: PrivateMemoryOperation[] }) {
  return (
    <div className="private-memory-history">
      <h3>Recent erasures</h3>
      {operations.length === 0 ? <p className="form-meta">No erasure operations.</p> : operations.slice(0, 8).map((operation) => (
        <div className="private-memory-history-row" key={operation.operation_id}>
          <div><strong>{operation.action.replaceAll("_", " ")}</strong><code title={operation.operation_id}>{shortId(operation.operation_id)}</code></div>
          <span className={`status-pill ${operation.status === "completed" ? "neutral" : "warning"}`}>{operation.status}</span>
        </div>
      ))}
    </div>
  );
}

function PrivateMemoryRetentionList({ runs }: { runs: PrivateMemoryRetentionRun[] }) {
  return (
    <div className="private-memory-history">
      <h3>Retention runs</h3>
      {runs.length === 0 ? <p className="form-meta">No retention runs.</p> : runs.slice(0, 8).map((run) => (
        <div className="private-memory-history-row" key={run.id}>
          <div><strong>{run.queued_count} queued</strong><code title={run.id}>{shortId(run.id)}</code></div>
          <span>{formatDate(run.started_at)}</span>
        </div>
      ))}
    </div>
  );
}

function privateMemoryIntent(action: string, target: string): string {
  return `${action}:${target}`;
}

function privateMemoryIdempotencyKey(keys: Map<string, string>, intent: string): string {
  const existing = keys.get(intent);
  if (existing) {
    return existing;
  }
  const key = `${intent}:${crypto.randomUUID()}`;
  keys.set(intent, key);
  return key;
}
