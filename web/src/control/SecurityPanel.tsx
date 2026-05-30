import { FormEvent, useEffect, useState } from "react";
import { Ban, RefreshCw, Trash2 } from "lucide-react";
import { ControlApi, SecurityBan, SecuritySettings } from "../api";
import { formatDate, readError } from "./utils";

export function SecurityPanel({ api }: { api: ControlApi }) {
  const [settings, setSettings] = useState<SecuritySettings | null>(null);
  const [bans, setBans] = useState<SecurityBan[]>([]);
  const [includeExpired, setIncludeExpired] = useState(false);
  const [ip, setIp] = useState("");
  const [reason, setReason] = useState("");
  const [expiresAt, setExpiresAt] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function loadSecurity(nextIncludeExpired = includeExpired) {
    setBusy(true);
    setError("");
    try {
      const [nextSettings, page] = await Promise.all([
        api.getSecuritySettings(),
        api.listSecurityBans(nextIncludeExpired),
      ]);
      setSettings(nextSettings);
      setBans(page.data);
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void loadSecurity();
  }, []);

  async function createBan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!ip.trim()) {
      setError("IP address is required.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.createSecurityBan({
        ip: ip.trim(),
        reason: reason.trim() || "manual ban",
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : undefined,
      });
      setIp("");
      setReason("");
      setExpiresAt("");
      await loadSecurity();
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  async function deleteBan(ipAddress: string) {
    if (!window.confirm(`Clear IP ban for ${ipAddress} and reset its strikes?`)) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.deleteSecurityBan(ipAddress);
      await loadSecurity();
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="surface security-panel">
      <div className="section-heading">
        <div>
          <h2>IP Bans</h2>
          <p className="section-subtitle">Review failed-auth bans, add manual bans, and clear strikes.</p>
        </div>
        <button className="icon-button" type="button" aria-label="Refresh security" onClick={() => void loadSecurity()} disabled={busy}>
          <RefreshCw size={16} aria-hidden="true" />
        </button>
      </div>
      {error && <div className="banner error" role="alert">{error}</div>}

      <div className="security-summary" aria-label="IP ban rules">
        {settings ? (
          <>
            <div className="summary-item">
              <span>Protection</span>
              <strong>{settings.enabled ? "On" : "Off"}</strong>
            </div>
            <div className="summary-item">
              <span>Threshold</span>
              <strong>{settings.failure_threshold} failures</strong>
            </div>
            <div className="summary-item">
              <span>Window</span>
              <strong>{settings.failure_window_seconds}s</strong>
            </div>
            <div className="summary-item">
              <span>Ban duration</span>
              <strong>{settings.ban_duration_seconds === 0 ? "Permanent" : `${settings.ban_duration_seconds}s`}</strong>
            </div>
          </>
        ) : (
          <div className="table-placeholder compact">Loading rules</div>
        )}
      </div>

      <form className="security-grid security-ban-form" onSubmit={createBan}>
        <label htmlFor="ban-ip">IP address</label>
        <input id="ban-ip" value={ip} onChange={(event) => setIp(event.target.value)} />
        <label htmlFor="ban-reason">Reason</label>
        <input id="ban-reason" value={reason} onChange={(event) => setReason(event.target.value)} />
        <label htmlFor="ban-expires">Expires</label>
        <input id="ban-expires" type="datetime-local" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} />
        <button className="danger-button span" type="submit" disabled={busy}>
          <Ban size={16} aria-hidden="true" />
          Add ban
        </button>
      </form>

      <div className="list-toolbar">
        <div>
          <h3>Ban list</h3>
          <span>{bans.length} IPs</span>
        </div>
        <label className="check-row include-row" htmlFor="include-expired">
          <input
            id="include-expired"
            type="checkbox"
            checked={includeExpired}
            onChange={(event) => {
              setIncludeExpired(event.target.checked);
              void loadSecurity(event.target.checked);
            }}
          />
          Include expired
        </label>
      </div>
      <SecurityBanTable bans={bans} busy={busy} onDelete={(ipAddress) => void deleteBan(ipAddress)} />
    </section>
  );
}

function SecurityBanTable({
  bans,
  busy,
  onDelete,
}: {
  bans: SecurityBan[];
  busy: boolean;
  onDelete: (ip: string) => void;
}) {
  if (bans.length === 0) {
    return <div className="table-placeholder">No IP bans</div>;
  }

  return (
    <div className="table-wrap">
      <table className="data-table security-table">
        <thead>
          <tr>
            <th>IP</th>
            <th>Failed attempts</th>
            <th>Source</th>
            <th>Reason</th>
            <th>Last failed</th>
            <th>Expires</th>
            <th className="actions-cell">Clear</th>
          </tr>
        </thead>
        <tbody>
          {bans.map((ban) => (
            <tr key={ban.ip}>
              <td><code>{ban.ip}</code></td>
              <td>{ban.failure_count}</td>
              <td><span className={ban.source === "auto" ? "status-pill warning" : "status-pill neutral"}>{ban.source}</span></td>
              <td>{ban.reason}</td>
              <td>{ban.last_failed_at ? formatDate(ban.last_failed_at) : "Never"}</td>
              <td>{ban.expires_at ? formatDate(ban.expires_at) : "Never"}</td>
              <td className="actions-cell">
                <button
                  className="icon-button danger"
                  type="button"
                  aria-label={`Clear IP ban and reset strikes for ${ban.ip}`}
                  title="Clear ban and reset strikes"
                  disabled={busy}
                  onClick={() => onDelete(ban.ip)}
                >
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
