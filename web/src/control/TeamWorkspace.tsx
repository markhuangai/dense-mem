import { CSSProperties, ReactNode, useEffect, useRef, useState } from "react";
import { Activity, CheckCircle2, Moon, Network, Users } from "lucide-react";
import { ControlApi, Credential, Team } from "../api";
import { useVisiblePolling } from "../telemetry/useVisiblePolling";
import { SectionHeading, SummaryCard } from "../ui/components";
import { credentialPermissionLabel, credentialRoleLabel, formatDate, shortId } from "./utils";

export type TeamWorkspaceTab = "overview" | "credentials" | "dreams" | "conflicts" | "settings";

export function TeamWorkspaceShell({
  team,
  activeTab,
  onSelectTab,
  children,
}: {
  team: Team;
  activeTab: TeamWorkspaceTab;
  onSelectTab: (tab: TeamWorkspaceTab) => void;
  children: ReactNode;
}) {
  return (
    <section className="surface team-workspace">
      <TeamWorkspaceHeader team={team} activeTab={activeTab} onSelectTab={onSelectTab} />
      <div className="team-workspace-body">
        {children}
      </div>
    </section>
  );
}

function TeamWorkspaceHeader({
  team,
  activeTab,
  onSelectTab,
}: {
  team: Team;
  activeTab: TeamWorkspaceTab;
  onSelectTab: (tab: TeamWorkspaceTab) => void;
}) {
  const tabs: Array<{ id: TeamWorkspaceTab; label: string }> = [
    { id: "overview", label: "Overview" },
    { id: "credentials", label: "Credentials" },
    { id: "dreams", label: "Dreams" },
    { id: "conflicts", label: "Conflicts" },
    { id: "settings", label: "Settings" },
  ];

  return (
    <header className="team-workspace-header">
      <div className="team-detail-header compact">
        <span className="team-mark" aria-hidden="true">
          <Users size={24} />
        </span>
        <div>
          <div className="team-title-row">
            <h2>{team.name}</h2>
            <span className="status-pill neutral">Active</span>
          </div>
          <p>Created {formatDate(team.created_at)} · Team ID {shortId(team.id)}</p>
        </div>
      </div>
      <nav className="team-workspace-tabs" aria-label={`${team.name} sections`}>
        {tabs.map((tab) => (
          <button
            key={tab.id}
            className={tab.id === activeTab ? "team-workspace-tab active" : "team-workspace-tab"}
            type="button"
            aria-label={`Team ${tab.label}`}
            aria-current={tab.id === activeTab ? "page" : undefined}
            onClick={() => onSelectTab(tab.id)}
          >
            {tab.label}
          </button>
        ))}
      </nav>
    </header>
  );
}

export function TeamOverviewPanel({
  api,
  team,
  onOpenMetrics,
}: {
  api: ControlApi;
  team: Team;
  onOpenMetrics: () => void;
}) {
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [metrics, setMetrics] = useState<Awaited<ReturnType<ControlApi["getMetrics"]>> | null>(null);
  const [metricsUnavailable, setMetricsUnavailable] = useState(false);
  const [communityStatus, setCommunityStatus] = useState<Awaited<ReturnType<ControlApi["getTeamCommunityStatus"]>> | null>(null);
  const [loading, setLoading] = useState(false);
  const [metricsLoading, setMetricsLoading] = useState(false);
  const metricsRequestRef = useRef(0);

  useEffect(() => {
    let active = true;
    setLoading(true);
    setMetricsUnavailable(false);
    Promise.all([
      api.listTeamCredentials(team.id).then((page) => page.data).catch(() => [] as Credential[]),
      api.getTeamCommunityStatus(team.id).catch(() => null),
    ])
      .then(([nextCredentials, nextCommunityStatus]) => {
        if (!active) {
          return;
        }
        setCredentials(nextCredentials);
        setCommunityStatus(nextCommunityStatus);
      })
      .finally(() => {
        if (active) {
          setLoading(false);
        }
      });
    return () => {
      active = false;
    };
  }, [api, team.id]);

  const refreshMetrics = useVisiblePolling(async (signal) => {
    const requestID = ++metricsRequestRef.current;
    setMetricsLoading(true);
    setMetricsUnavailable(false);
    try {
      setMetrics(await api.getMetrics({ team_id: team.id, window_minutes: 60 }, signal));
    } catch (error) {
      if (!isAbortError(error)) {
        setMetricsUnavailable(true);
      }
    } finally {
      if (metricsRequestRef.current === requestID) {
        setMetricsLoading(false);
      }
    }
  }, [api, team.id]);

  const metricsReady = metrics !== null && !metricsUnavailable;
  const metricsFailed = metricsUnavailable && !metricsLoading;
  const communityUnavailable = communityStatus === null && !loading;
  const communityEnabled = communityStatus?.effective_config.enabled === true;
  const teamMetrics = metricsReady ? metrics.teams.find((item) => item.team_id === team.id) : undefined;
  const requests = metricsReady ? teamMetrics?.requests ?? metrics.system.requests : 0;
  const errors = metricsReady ? teamMetrics?.errors ?? metrics.system.errors : 0;
  const health = requests === 0 ? 100 : Math.max(0, 100 - (errors / requests) * 100);
  const dependencies = metricsReady ? metrics.dependencies : [];
  const degradedDependencies = dependencies.filter((dependency) => dependency.status !== "ok");
  const healthyDependencies = dependencies.filter((dependency) => dependency.status === "ok");
  const dependencyDetail = degradedDependencies.length > 0
    ? degradedDependencies.map((dependency) => `${dependency.name} · ${dependency.reason_code ?? dependency.status} · ${dependency.latency_ms === null || dependency.latency_ms === undefined ? "latency n/a" : `${Math.round(dependency.latency_ms)} ms`}`).join(", ")
    : metrics?.dependencies_checked_at ? `Observed ${formatDate(metrics.dependencies_checked_at)}` : "Operational";
  const managerCount = credentials.filter((credential) => credential.role === "manager").length;
  const readWriteCount = credentials.filter((credential) => credential.scopes?.includes("write")).length;
  const recentCredentials = [...credentials].sort((left, right) => right.created_at.localeCompare(left.created_at)).slice(0, 5);
  const requestValue = metricsReady ? compactNumber(requests) : loading || metricsLoading ? "..." : "n/a";
  const errorValue = metricsReady ? compactNumber(errors) : loading || metricsLoading ? "..." : "n/a";
  const healthValue = metricsReady ? `${health.toFixed(1)}%` : loading || metricsLoading ? "..." : "n/a";
  const healthDetail = loading || metricsLoading ? <span className="inline-loading">Loading</span> : metricsFailed ? "Metrics unavailable" : "Operational";
  const latencyValue = metricsReady ? `${Math.round(teamMetrics?.avg_latency_ms ?? metrics.system.avg_latency_ms)} ms` : "n/a";
  const maxLatencyValue = metricsReady ? `${Math.round(teamMetrics?.max_latency_ms ?? metrics.system.max_latency_ms)} ms` : "n/a";

  return (
    <div className="team-overview" aria-label="Team overview">
      <div className="summary-strip" aria-label="Summary">
        <SummaryCard label="Credentials" value={credentials.length} detail={`${managerCount} managers`} />
        <SummaryCard label="Communities" value={communityStatus?.current_community_count ?? "n/a"} detail={loading ? "Loading" : communityUnavailable ? "Status unavailable" : communityEnabled ? "Nightly enabled" : "Nightly disabled"} tone={communityUnavailable || !communityEnabled ? "warning" : "neutral"} />
        <SummaryCard label="Requests" value={requestValue} detail="Last hour" tone={metricsFailed ? "warning" : "neutral"} />
        <SummaryCard label="Recall health" value={healthValue} detail={healthDetail} tone={metricsFailed ? "warning" : "neutral"} />
        <div className="health-stack" aria-label="Health summary">
          <HealthLine label="Healthy" value={metricsReady ? Math.max(0, dependencies.length - degradedDependencies.length) : "n/a"} tone={metricsFailed ? "warning" : "healthy"} />
          <HealthLine label="Degraded" value={metricsReady ? degradedDependencies.length : "n/a"} tone={metricsFailed || degradedDependencies.length > 0 ? "warning" : "healthy"} />
          <HealthLine label="Errors" value={metricsReady ? errors : "n/a"} tone={errors > 0 ? "danger" : metricsFailed ? "warning" : "healthy"} />
        </div>
      </div>

      <div className="overview-grid">
        <section className="overview-panel" aria-label="Team activity">
          <SectionHeading title="Team Activity (1h)" meta={loading || metricsLoading ? <span className="inline-loading">Loading</span> : metricsFailed ? "metrics unavailable" : undefined} />
          <MetricRow icon={<Activity size={15} aria-hidden="true" />} label="HTTP requests" value={requestValue} trend={metricsReady ? requests > 0 ? "+ active" : "idle" : "unavailable"} tone={metricsFailed ? "warning" : "neutral"} />
          <MetricRow icon={<Activity size={15} aria-hidden="true" />} label="Errors" value={errorValue} trend={metricsReady ? errors > 0 ? "review" : "clear" : "unavailable"} tone={errors > 0 ? "danger" : metricsFailed ? "warning" : "neutral"} />
          <MetricRow icon={<Users size={15} aria-hidden="true" />} label="Writable credentials" value={readWriteCount} trend={`${credentials.length} total`} />
          <MetricRow icon={<Moon size={15} aria-hidden="true" />} label="Dreaming" value={team.dreaming_effective?.enabled ? "Enabled" : "Inherited"} trend={team.dreaming_effective?.source ?? "global"} />
          <MetricRow icon={<Network size={15} aria-hidden="true" />} label="Community snapshot" value={communityStatus?.current_community_count ?? "n/a"} trend={communityStatus?.latest_run?.status ?? "not run"} tone={communityStatus?.latest_run?.status === "failed" ? "warning" : "neutral"} />
        </section>

        <section className="overview-panel" aria-label="Top signals">
          <SectionHeading title="Top Signals" />
          <MetricRow icon={<CheckCircle2 size={15} aria-hidden="true" />} label="Average latency" value={latencyValue} trend={metricsReady ? "p95 proxy" : "unavailable"} tone={metricsFailed ? "warning" : "neutral"} />
          <MetricRow icon={<CheckCircle2 size={15} aria-hidden="true" />} label="Max latency" value={maxLatencyValue} trend={metricsReady ? "last hour" : "unavailable"} tone={metricsFailed ? "warning" : "neutral"} />
          <MetricRow icon={<CheckCircle2 size={15} aria-hidden="true" />} label="Dependency checks" value={metricsReady ? `${healthyDependencies.length}/${dependencies.length}` : "n/a"} trend={metricsReady ? degradedDependencies.length ? dependencyDetail : "healthy" : "unavailable"} tone={metricsFailed || degradedDependencies.length ? "warning" : "neutral"} />
          <MetricRow icon={<CheckCircle2 size={15} aria-hidden="true" />} label="Credential freshness" value={recentCredentials[0] ? formatDate(recentCredentials[0].created_at) : "No credentials"} trend="latest" />
        </section>
      </div>

      <section className="overview-panel" aria-label="Recent alerts">
        <SectionHeading title="Recent Alerts" actions={<button className="text-button" type="button" onClick={onOpenMetrics}>Open Metrics</button>} />
        <div className="mini-table">
          <MiniTableRow columns={["Time", "Severity", "Alert", "Scope", "Status"]} heading />
          {metricsFailed && <MiniTableRow columns={["Now", "Medium", "Metrics unavailable", "Team", "Open"]} />}
          {!metricsFailed && errors > 0 && <MiniTableRow columns={["Now", "High", "Request errors detected", "Team", "Open"]} />}
          {degradedDependencies.map((dependency) => (
            <MiniTableRow
              key={dependency.name}
              columns={["Now", "Medium", `${dependency.name} ${dependency.status}${dependency.reason_code ? ` · ${dependency.reason_code}` : ""}`, "Dependency", "Open"]}
            />
          ))}
          {!metricsFailed && errors === 0 && degradedDependencies.length === 0 && (
            <MiniTableRow columns={[formatDate(team.updated_at), "Low", "No active alerts", "Team", "Clear"]} />
          )}
        </div>
      </section>

      <section className="overview-panel" aria-label="Recent credential changes">
        <SectionHeading title="Recent Credential Changes" meta={credentials.length} />
        <div className="mini-table">
          <MiniTableRow columns={["Credential", "Permission", "Role", "Created"]} heading />
          {recentCredentials.length > 0 ? recentCredentials.map((credential) => (
            <MiniTableRow
              key={credential.id}
              columns={[
                credential.name,
                credentialPermissionLabel(credential.scopes),
                credentialRoleLabel(credential.role),
                formatDate(credential.created_at),
              ]}
            />
          )) : (
            <MiniTableRow columns={["No credentials", "-", "-", "-"]} />
          )}
        </div>
      </section>
    </div>
  );
}

function HealthLine({
  label,
  value,
  tone,
}: {
  label: string;
  value: ReactNode;
  tone: "healthy" | "warning" | "danger";
}) {
  return (
    <div className={`health-line ${tone}`}>
      <span aria-hidden="true" />
      <small>{label}</small>
      <strong>{value}</strong>
    </div>
  );
}

function MetricRow({
  icon,
  label,
  value,
  trend,
  tone = "neutral",
}: {
  icon: ReactNode;
  label: string;
  value: ReactNode;
  trend: string;
  tone?: "neutral" | "warning" | "danger";
}) {
  return (
    <div className={`metric-row ${tone}`}>
      <span className="metric-icon">{icon}</span>
      <span className="metric-label">{label}</span>
      <strong>{value}</strong>
      <small className="metric-detail">{trend}</small>
    </div>
  );
}

function MiniTableRow({ columns, heading = false }: { columns: ReactNode[]; heading?: boolean }) {
  return (
    <div
      className={heading ? "mini-table-row heading" : "mini-table-row"}
      style={{ "--mini-cols": columns.length } as CSSProperties}
    >
      {columns.map((column, index) => <span key={index}>{column}</span>)}
    </div>
  );
}

function compactNumber(value: number): string {
  return new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}
