import { useConfirmation } from "../ui/confirmation";
import { useUnsavedChanges } from "../ui/navigation";
import { Button } from "../ui/primitives/button";
import { Label } from "../ui/primitives/label";
import { Input } from "../ui/primitives/input";
import { Checkbox } from "../ui/primitives/checkbox";
import { Table, TableHeader, TableRow, TableHead, TableBody, TableCell } from "../ui/primitives/table";
import { FormEvent, Fragment, useEffect, useState } from "react";
import { Ban, RefreshCw, Trash2 } from "lucide-react";
import { ControlApi, SecurityBan, SecuritySettings, SecurityRequestSnapshot } from "../api";
import { ResponsiveInspector, useCompactWorkspace } from "../ui/inspector";
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
  const { ref, compact } = useCompactWorkspace(760);
  const [expandedIP, setExpandedIP] = useState("");
  if (bans.length === 0) {
    return <section ref={ref} aria-label="IP ban requests"><div className="table-placeholder">No IP bans</div></section>;
  }

  return (
    <section ref={ref} aria-label="IP ban requests">
    <div className="table-wrap">
      <Table className="data-table security-table">
        <TableHeader>
          <TableRow>
            <TableHead>IP</TableHead>
            <TableHead>Failed attempts</TableHead>
            <TableHead>Source</TableHead>
            <TableHead>Reason</TableHead>
            <TableHead>Request</TableHead>
            <TableHead>Last failed</TableHead>
            <TableHead>Expires</TableHead>
            <TableHead className="actions-cell">Clear</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {bans.map((ban) => (
            <Fragment key={ban.ip}>
            <TableRow>
              <TableCell><code>{ban.ip}</code></TableCell>
              <TableCell>{ban.failure_count}</TableCell>
              <TableCell><span className={ban.source === "auto" ? "status-pill warning" : "status-pill neutral"}>{ban.source}</span></TableCell>
              <TableCell>{ban.reason}</TableCell>
              <TableCell>{ban.source === "manual" ? "Not applicable" : ban.metadata?.last_request ? (
                <Button variant="ghost" type="button" aria-label={`View request for ${ban.ip}`} aria-expanded={expandedIP === ban.ip} onClick={() => setExpandedIP(expandedIP === ban.ip ? "" : ban.ip)}>View request</Button>
              ) : "Request details unavailable"}</TableCell>
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
            {expandedIP === ban.ip && !compact && ban.metadata?.last_request && <TableRow className="log-raw-row"><TableCell colSpan={8}><SecurityRequestDetails request={ban.metadata.last_request} /></TableCell></TableRow>}
            </Fragment>
          ))}
        </TableBody>
      </Table>
    </div>
    <ResponsiveInspector compact open={compact && bans.some((ban) => ban.ip === expandedIP)} onOpenChange={(open) => { if (!open) setExpandedIP(""); }} title="Latest failed request">
      {bans.filter((ban) => ban.ip === expandedIP && ban.metadata?.last_request).map((ban) => <SecurityRequestDetails key={ban.ip} request={ban.metadata!.last_request!} />)}
    </ResponsiveInspector>
    </section>
  );
}

function SecurityRequestDetails({ request }: { request: SecurityRequestSnapshot }) {
  return <div className="security-request-details">
    <h3>Latest failed request</h3>
    <dl>
      <dt>Request</dt><dd><code>{request.method} {request.url || "URL unavailable"}</code></dd>
      <dt>Route</dt><dd><code>{request.route || "Unavailable"}</code></dd>
      <dt>Time</dt><dd>{formatDate(request.captured_at)}</dd>
      <dt>Reason</dt><dd>{request.reason} {request.message}</dd>
      <dt>Authentication</dt><dd>{request.auth_scheme || "Unavailable"}</dd>
      {request.credential_prefix && <><dt>Key prefix</dt><dd><code>{request.credential_prefix}…</code></dd></>}
      {request.credential_fingerprint && <><dt>Credential fingerprint (SHA-256)</dt><dd><code>{request.credential_fingerprint}</code></dd></>}
      <dt>Snapshot ID</dt><dd><code>{request.id}</code></dd>
    </dl>
    {request.url_capture.state === "unavailable" && <p>URL unavailable: {request.url_capture.reason}</p>}
    <div className="remember-diagnostic-body"><h4>Headers</h4>{request.header_capture.state === "captured" ? <pre aria-label="Request headers">{Object.entries(request.headers ?? {}).sort(([a], [b]) => a.localeCompare(b)).flatMap(([name, values]) => values.map((value) => `${name}: ${value}`)).join("\n") || "(empty headers)"}</pre> : <p>Headers unavailable: {request.header_capture.reason}</p>}</div>
    <div className="remember-diagnostic-body"><h4>Body</h4>{request.body_capture.state === "captured" ? <pre aria-label="Request body">{request.body || "(empty body)"}</pre> : <p>Body unavailable: {request.body_capture.reason} (limit {request.body_capture.limit.toLocaleString()} bytes)</p>}</div>
  </div>;
}
