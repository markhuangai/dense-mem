import { useConfirmation } from "../ui/confirmation";
import { useUnsavedChanges } from "../ui/navigation";
import { Button } from "../ui/primitives/button";
import { Label } from "../ui/primitives/label";
import { Input } from "../ui/primitives/input";
import { Checkbox } from "../ui/primitives/checkbox";
import { Table, TableHeader, TableRow, TableHead, TableBody, TableCell } from "../ui/primitives/table";
import { FormEvent, useEffect, useState } from "react";
import { Ban, RefreshCw, Trash2 } from "lucide-react";
import { ControlApi, SecurityBan, SecuritySettings } from "../api";
import { FieldRow, LoadingState, SectionHeading, SummaryCard } from "../ui/components";
import { formatDate, readError } from "./utils";

export function SecurityPanel({ api }: { api: ControlApi }) {
  const confirm = useConfirmation();
  const [settings, setSettings] = useState<SecuritySettings | null>(null);
  const [bans, setBans] = useState<SecurityBan[]>([]);
  const [includeExpired, setIncludeExpired] = useState(false);
  const [ip, setIp] = useState("");
  const [reason, setReason] = useState("");
  const [expiresAt, setExpiresAt] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useUnsavedChanges(Boolean(ip || reason || expiresAt));

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
    if (!await confirm({ title: "Clear IP ban?", description: `Clear IP ban for ${ipAddress} and reset its strikes?`, confirmLabel: "Clear IP ban", destructive: true })) {
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
      <SectionHeading
        title="IP Bans"
        subtitle="Review failed-auth bans, add manual bans, and clear strikes."
        actions={(
          <Button variant="ghost" size="icon" className="icon-button" type="button" aria-label="Refresh security" onClick={() => void loadSecurity()} disabled={busy}>
            <RefreshCw size={16} aria-hidden="true" />
          </Button>
        )}
      />
      {error && <div className="banner error" role="alert">{error}</div>}

      <div className="security-summary" aria-label="IP ban rules">
        {settings ? (
          <>
            <SummaryCard label="Protection" value={settings.enabled ? "On" : "Off"} />
            <SummaryCard label="Threshold" value={`${settings.failure_threshold} failures`} />
            <SummaryCard label="Window" value={`${settings.failure_window_seconds}s`} />
            <SummaryCard label="Ban duration" value={settings.ban_duration_seconds === 0 ? "Permanent" : `${settings.ban_duration_seconds}s`} />
          </>
        ) : (
          <LoadingState label="Loading rules" compact />
        )}
      </div>

      <form className="security-grid security-ban-form" onSubmit={createBan}>
        <FieldRow label="IP address" htmlFor="ban-ip">
          <Input id="ban-ip" value={ip} onChange={(event) => setIp(event.target.value)} />
        </FieldRow>
        <FieldRow label="Reason" htmlFor="ban-reason">
          <Input id="ban-reason" value={reason} onChange={(event) => setReason(event.target.value)} />
        </FieldRow>
        <FieldRow label="Expires" htmlFor="ban-expires">
          <Input id="ban-expires" type="datetime-local" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} />
        </FieldRow>
        <Button variant="destructive" className="danger-button span" type="submit" disabled={busy}>
          <Ban size={16} aria-hidden="true" />
          Add ban
        </Button>
      </form>

      <div className="list-toolbar">
        <div>
          <h3>Ban list</h3>
          <span>{bans.length} IPs</span>
        </div>
        <Label className="check-row include-row" htmlFor="include-expired">
          <Checkbox
            id="include-expired"
            checked={includeExpired}
            onCheckedChange={(event) => {
              setIncludeExpired((event === true));
              void loadSecurity((event === true));
            }}
          />
          Include expired
        </Label>
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
      <Table className="data-table security-table">
        <TableHeader>
          <TableRow>
            <TableHead>IP</TableHead>
            <TableHead>Failed attempts</TableHead>
            <TableHead>Source</TableHead>
            <TableHead>Reason</TableHead>
            <TableHead>Last failed</TableHead>
            <TableHead>Expires</TableHead>
            <TableHead className="actions-cell">Clear</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {bans.map((ban) => (
            <TableRow key={ban.ip}>
              <TableCell><code>{ban.ip}</code></TableCell>
              <TableCell>{ban.failure_count}</TableCell>
              <TableCell><span className={ban.source === "auto" ? "status-pill warning" : "status-pill neutral"}>{ban.source}</span></TableCell>
              <TableCell>{ban.reason}</TableCell>
              <TableCell>{ban.last_failed_at ? formatDate(ban.last_failed_at) : "Never"}</TableCell>
              <TableCell>{ban.expires_at ? formatDate(ban.expires_at) : "Never"}</TableCell>
              <TableCell className="actions-cell">
                <Button variant="ghost" size="icon"
                  className="icon-button danger"
                  type="button"
                  aria-label={`Clear IP ban and reset strikes for ${ban.ip}`}
                  title="Clear ban and reset strikes"
                  disabled={busy}
                  onClick={() => onDelete(ban.ip)}
                >
                  <Trash2 size={16} aria-hidden="true" />
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
