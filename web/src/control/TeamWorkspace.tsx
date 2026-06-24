import { CSSProperties, ReactNode, useEffect, useState } from "react";
import { Activity, CheckCircle2, Moon, Users } from "lucide-react";
import { ControlApi, Team, TeamProfile } from "../api";
import { SectionHeading, SummaryCard } from "../ui/components";
import { formatDate, profilePermissionLabel, profileRoleLabel, shortId } from "./utils";

export type TeamWorkspaceTab = "overview" | "profiles" | "dreams" | "settings";

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
      {children}
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
    { id: "profiles", label: "Profiles" },
    { id: "dreams", label: "Dreams" },
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
  onOpenSettings,
}: {
  api: ControlApi;
  team: Team;
  onOpenSettings: () => void;
}) {
  const [profiles, setProfiles] = useState<TeamProfile[]>([]);
  const [metrics, setMetrics] = useState<Awaited<ReturnType<ControlApi["getMetrics"]>> | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    let active = true;
    setLoading(true);
    Promise.all([
      api.listTeamProfiles(team.id).then((page) => page.data).catch(() => [] as TeamProfile[]),
      api.getMetrics({ team_id: team.id, window_minutes: 60 }).catch(() => null),
    ])
      .then(([nextProfiles, nextMetrics]) => {
        if (!active) {
          return;
        }
        setProfiles(nextProfiles);
        setMetrics(nextMetrics);
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

  const teamMetrics = metrics?.teams.find((item) => item.team_id === team.id);
  const requests = teamMetrics?.requests ?? metrics?.system.requests ?? 0;
  const errors = teamMetrics?.errors ?? metrics?.system.errors ?? 0;
  const health = requests === 0 ? 100 : Math.max(0, 100 - (errors / requests) * 100);
  const dependencies = metrics?.dependencies ?? [];
  const degradedDependencies = dependencies.filter((dependency) => dependency.status !== "ok");
  const managerCount = profiles.filter((profile) => profile.role === "manager").length;
  const readWriteCount = profiles.filter((profile) => profile.scopes?.includes("write")).length;
  const recentProfiles = [...profiles].sort((left, right) => right.created_at.localeCompare(left.created_at)).slice(0, 5);

  return (
    <div className="team-overview" aria-label="Team overview">
      <div className="summary-strip" aria-label="Summary">
        <SummaryCard label="Profiles" value={profiles.length} detail={`${managerCount} managers`} />
        <SummaryCard label="Requests" value={compactNumber(requests)} detail="Last hour" />
        <SummaryCard label="Recall health" value={`${health.toFixed(1)}%`} detail={loading ? "Loading" : "Operational"} />
        <div className="health-stack" aria-label="Health summary">
          <HealthLine label="Healthy" value={Math.max(0, dependencies.length - degradedDependencies.length)} tone="healthy" />
          <HealthLine label="Degraded" value={degradedDependencies.length} tone={degradedDependencies.length > 0 ? "warning" : "healthy"} />
          <HealthLine label="Errors" value={errors} tone={errors > 0 ? "danger" : "healthy"} />
        </div>
      </div>

      <div className="overview-grid">
        <section className="overview-panel" aria-label="Team activity">
          <SectionHeading title="Team Activity (1h)" meta={loading ? "loading" : undefined} />
          <MetricRow icon={<Activity size={15} aria-hidden="true" />} label="HTTP requests" value={compactNumber(requests)} trend={requests > 0 ? "+ active" : "idle"} />
          <MetricRow icon={<Activity size={15} aria-hidden="true" />} label="Errors" value={compactNumber(errors)} trend={errors > 0 ? "review" : "clear"} tone={errors > 0 ? "danger" : "neutral"} />
          <MetricRow icon={<Users size={15} aria-hidden="true" />} label="Writable profiles" value={readWriteCount} trend={`${profiles.length} total`} />
          <MetricRow icon={<Moon size={15} aria-hidden="true" />} label="Dreaming" value={team.dreaming_effective?.enabled ? "Enabled" : "Inherited"} trend={team.dreaming_effective?.source ?? "global"} />
        </section>

        <section className="overview-panel" aria-label="Top signals">
          <SectionHeading title="Top Signals" />
          <MetricRow icon={<CheckCircle2 size={15} aria-hidden="true" />} label="Average latency" value={`${Math.round(teamMetrics?.avg_latency_ms ?? metrics?.system.avg_latency_ms ?? 0)} ms`} trend="p95 proxy" />
          <MetricRow icon={<CheckCircle2 size={15} aria-hidden="true" />} label="Max latency" value={`${Math.round(teamMetrics?.max_latency_ms ?? metrics?.system.max_latency_ms ?? 0)} ms`} trend="last hour" />
          <MetricRow icon={<CheckCircle2 size={15} aria-hidden="true" />} label="Dependency checks" value={dependencies.length || "n/a"} trend={degradedDependencies.length ? "attention" : "healthy"} tone={degradedDependencies.length ? "warning" : "neutral"} />
          <MetricRow icon={<CheckCircle2 size={15} aria-hidden="true" />} label="Profile freshness" value={recentProfiles[0] ? formatDate(recentProfiles[0].created_at) : "No profiles"} trend="latest" />
        </section>
      </div>

      <section className="overview-panel" aria-label="Recent alerts">
        <SectionHeading title="Recent Alerts" actions={<button className="text-button" type="button" onClick={onOpenSettings}>Open settings</button>} />
        <div className="mini-table">
          <MiniTableRow columns={["Time", "Severity", "Alert", "Scope", "Status"]} heading />
          {errors > 0 && <MiniTableRow columns={["Now", "High", "Request errors detected", "Team", "Open"]} />}
          {degradedDependencies.map((dependency) => (
            <MiniTableRow
              key={dependency.name}
              columns={["Now", "Medium", `${dependency.name} ${dependency.status}`, "Dependency", "Open"]}
            />
          ))}
          {errors === 0 && degradedDependencies.length === 0 && (
            <MiniTableRow columns={[formatDate(team.updated_at), "Low", "No active alerts", "Team", "Clear"]} />
          )}
        </div>
      </section>

      <section className="overview-panel" aria-label="Recent profile changes">
        <SectionHeading title="Recent Profile Changes" meta={profiles.length} />
        <div className="mini-table">
          <MiniTableRow columns={["Profile", "Permission", "Role", "Created"]} heading />
          {recentProfiles.length > 0 ? recentProfiles.map((profile) => (
            <MiniTableRow
              key={profile.id}
              columns={[
                profile.name,
                profilePermissionLabel(profile.scopes),
                profileRoleLabel(profile.role),
                formatDate(profile.created_at),
              ]}
            />
          )) : (
            <MiniTableRow columns={["No profiles", "-", "-", "-"]} />
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
  value: number;
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
      <span>{label}</span>
      <strong>{value}</strong>
      <small>{trend}</small>
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
