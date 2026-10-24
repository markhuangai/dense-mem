import { Button } from "../ui/primitives/button";
import { Label } from "../ui/primitives/label";
import { NativeSelect, NativeSelectOption } from "../ui/primitives/native-select";
import { Switch } from "../ui/primitives/switch";
import { Textarea } from "../ui/primitives/textarea";
import { Input } from "../ui/primitives/input";
import { FormEvent, Fragment, ReactNode, useCallback, useEffect, useState } from "react";
import { Check, Clock, ListFilter, MessageSquare, Network, RefreshCw, Settings, X } from "lucide-react";
import { ControlApi, GeneralConfig, GeneralConfigItem, OperationLogConfig, OperationLogConfigItem, RecallFeedbackConfig, RecallFeedbackConfigItem, SSOConfig, SSOConfigItem, TelemetryPricingConfig, TelemetryPricingConfigItem } from "../api";
import { LoadingState, SectionHeading } from "../ui/components";
import { formatDate, readError } from "./utils";
import { OntologyMaintenancePanel } from "./OntologyMaintenancePanel";
import { OntologyTeamConcurrencyField } from "./OntologyTeamConcurrencyField";
import type { OntologyMaintenanceStatus } from "../api";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/primitives/tabs";
import { useUnsavedChanges, useViewTransition } from "../ui/navigation";

export type ConfigTab = "general" | "sso" | "operation-logs" | "recall-feedback" | "telemetry-pricing";

const CONFIG_LABELS: Record<string, string> = {
  APP_TIMEZONE: "Timezone",
  ONTOLOGY_MAINTENANCE_ENABLED: "Enable ontology maintenance",
  ONTOLOGY_MAINTENANCE_CADENCE_HOURS: "Cadence (hours)",
  ONTOLOGY_MAINTENANCE_START_TIME_LOCAL: "Window start time",
  ONTOLOGY_MAINTENANCE_MODEL: "Maintenance model",
  ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS: "Model context window (tokens)",
  ONTOLOGY_MAINTENANCE_MAX_CONCURRENCY: "Concurrent assessments",
  ONTOLOGY_MAINTENANCE_TEAM_MAX_CONCURRENCY: "Default concurrency per team",
	SSO_PUBLIC_BASE_URL: "Public base URL",
	MCP_PUBLIC_BASE_URL: "MCP public base URL",
  SCIM_PUBLIC_BASE_URL: "SCIM ingress URL",
  CONTROL_PUBLIC_BASE_URL: "Control portal ingress URL",
  SSO_ENTITLEMENT_CACHE_TTL_SECONDS: "Entitlement cache TTL",
  SSO_SESSION_TTL_SECONDS: "Session TTL",
  SSO_STATE_TTL_SECONDS: "OAuth state TTL",
  SSO_HTTP_TIMEOUT_SECONDS: "OIDC HTTP timeout",
  SSO_COOKIE_SECURE: "Secure browser cookies",
  OPERATION_LOG_RETENTION_DAYS: "Retention days",
  RECALL_FEEDBACK_ENABLED: "Enable recall feedback",
  RECALL_FEEDBACK_RETENTION_DAYS: "Investigation retention days",
  TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS: "Verifier input USD / million tokens",
  TELEMETRY_COST_VERIFIER_OUTPUT_USD_PER_MILLION_TOKENS: "Verifier output USD / million tokens",
  TELEMETRY_COST_EMBEDDING_INPUT_USD_PER_MILLION_TOKENS: "Embedding input USD / million tokens",
  TELEMETRY_COST_MODEL_PRICES_JSON: "Additional model prices (JSON)",
};

const CONFIG_DESCRIPTIONS: Record<string, string> = {
  ONTOLOGY_MAINTENANCE_ENABLED: "Admit new work on the maintenance schedule.",
  ONTOLOGY_MAINTENANCE_CADENCE_HOURS: "Hours between maintenance windows.",
  ONTOLOGY_MAINTENANCE_START_TIME_LOCAL: "Start time in the service timezone.",
  ONTOLOGY_MAINTENANCE_MODEL: "Leave blank to use the semantic assessor model.",
  ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS: "Capacity for one complete request and response. Set the selected model’s supported context size before processing.",
  ONTOLOGY_MAINTENANCE_MAX_CONCURRENCY: "Assessments that can run at the same time.",
  ONTOLOGY_MAINTENANCE_TEAM_MAX_CONCURRENCY: "Work slots per team (1–8), bounded by global capacity. Applies at the next window.",
  APP_TIMEZONE: "Timezone used to display service activity.",
  OPERATION_LOG_RETENTION_DAYS: "Days to retain operational records.",
  RECALL_FEEDBACK_RETENTION_DAYS: "Days to retain recall investigations.",
};

const CONFIG_PLACEHOLDERS: Record<string, string> = {
  APP_TIMEZONE: "Local",
  ONTOLOGY_MAINTENANCE_CADENCE_HOURS: "12",
  ONTOLOGY_MAINTENANCE_START_TIME_LOCAL: "03:00",
  ONTOLOGY_MAINTENANCE_MODEL: "Use the semantic assessor model",
  ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS: "Required before model processing",
  ONTOLOGY_MAINTENANCE_MAX_CONCURRENCY: "1",
  ONTOLOGY_MAINTENANCE_TEAM_MAX_CONCURRENCY: "1",
	MCP_PUBLIC_BASE_URL: "https://memory.example.com",
  SCIM_PUBLIC_BASE_URL: "https://memory.example.com",
  CONTROL_PUBLIC_BASE_URL: "https://control.example.com",
  SSO_ENTITLEMENT_CACHE_TTL_SECONDS: "300",
  SSO_SESSION_TTL_SECONDS: "28800",
  SSO_STATE_TTL_SECONDS: "600",
  SSO_HTTP_TIMEOUT_SECONDS: "10",
  OPERATION_LOG_RETENTION_DAYS: "30",
  RECALL_FEEDBACK_RETENTION_DAYS: "30",
  TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS: "Leave blank to mark as unpriced",
  TELEMETRY_COST_VERIFIER_OUTPUT_USD_PER_MILLION_TOKENS: "Leave blank to mark as unpriced",
  TELEMETRY_COST_EMBEDDING_INPUT_USD_PER_MILLION_TOKENS: "Leave blank to mark as unpriced",
  TELEMETRY_COST_MODEL_PRICES_JSON: '[{"component":"verifier","model":"model-name","input_usd_per_million_tokens":1,"output_usd_per_million_tokens":2}]',
};

const FALLBACK_TIMEZONES = [
  "UTC",
  "America/New_York",
  "America/Chicago",
  "America/Denver",
  "America/Los_Angeles",
  "America/Phoenix",
  "America/Anchorage",
  "America/Honolulu",
  "Europe/London",
  "Europe/Berlin",
  "Europe/Paris",
  "Europe/Madrid",
  "Asia/Tokyo",
  "Asia/Shanghai",
  "Asia/Singapore",
  "Asia/Kolkata",
  "Australia/Sydney",
];

const SUPPORTED_TIMEZONES = getSupportedTimezones();

export function ConfigPanel({ api, tab, onTabChange }: { api: ControlApi; tab?: string; onTabChange?: (tab: ConfigTab) => void }) {
  const transition = useViewTransition();
  const [localTab, setLocalTab] = useState<ConfigTab>("general");
  const tabs = [
    { id: "general", label: "General", icon: Clock },
    { id: "sso", label: "SSO runtime", icon: Settings },
    { id: "operation-logs", label: "Operation logs", icon: ListFilter },
    { id: "recall-feedback", label: "Recall feedback", icon: MessageSquare },
    { id: "telemetry-pricing", label: "Pricing", icon: Settings },
  ] as const;
  const activeTab = tab && tabs.some((item) => item.id === tab) ? tab : localTab;
  return <div className="stack"><SectionHeading title="Settings" subtitle="Configure service behavior, retention, and model pricing." />
    <Tabs className="panel-tabs" value={activeTab} onValueChange={(next) => { if (onTabChange) onTabChange(next as ConfigTab); else void transition(() => setLocalTab(next as ConfigTab)); }}>
      <TabsList aria-label="Settings sections">{tabs.map((item) => <TabsTrigger value={item.id} key={item.id}><item.icon size={16} aria-hidden="true" />{item.label}</TabsTrigger>)}</TabsList>
      <TabsContent value="general"><GeneralConfigPanel api={api} /></TabsContent>
      <TabsContent value="sso"><SSOConfigPanel api={api} /></TabsContent>
      <TabsContent value="operation-logs"><OperationLogConfigPanel api={api} /></TabsContent>
      <TabsContent value="recall-feedback"><RecallFeedbackConfigPanel api={api} /></TabsContent>
      <TabsContent value="telemetry-pricing"><TelemetryPricingConfigPanel api={api} /></TabsContent>
    </Tabs>
  </div>;
}

type ConfigItem = GeneralConfigItem | SSOConfigItem | OperationLogConfigItem | RecallFeedbackConfigItem | TelemetryPricingConfigItem;
type RuntimeConfig = {
  update_time: string;
  items: ConfigItem[];
};
type RuntimeConfigInput = {
  items: Array<{
    key: string;
    value: string;
  }>;
};

export function OntologyConfigPanel({ api }: { api: ControlApi }) {
  const load = useCallback(() => api.getOntologyMaintenanceConfig(), [api]);
  const [status, setStatus] = useState<OntologyMaintenanceStatus | null>(null);
  return <><div className="page-intro"><h1>Ontology maintenance</h1><p>Organize shared knowledge within the model’s context capacity.</p></div><div className="ontology-maintenance-layout"><div className="ontology-policy">
    <RuntimeConfigPanel title="Maintenance policy" refreshLabel="Refresh ontology config" load={load} save={(input) => api.updateOntologyMaintenanceConfig(input)} detail={() => <p>Policy changes apply at the next window. Disabling stops new work immediately.</p>}
      renderItem={(item, value, onChange) => item.key === "ONTOLOGY_MAINTENANCE_TEAM_OVERRIDES_JSON" ? <OntologyTeamConcurrencyField api={api} value={value} onChange={onChange} status={status} /> : undefined} />
    </div><OntologyMaintenancePanel api={api} onStatus={setStatus} /></div>
  </>;
}

function GeneralConfigPanel({ api }: { api: ControlApi }) {
  const load = useCallback(() => api.getGeneralConfig(), [api]);
  return (
    <RuntimeConfigPanel
      title="General"
      refreshLabel="Refresh general config"
      load={load}
      save={(input) => api.updateGeneralConfig(input)}
    />
  );
}

function SSOConfigPanel({ api }: { api: ControlApi }) {
  const load = useCallback(() => api.getSSOConfig(), [api]);
  return (
    <RuntimeConfigPanel
      title="SSO"
      refreshLabel="Refresh SSO config"
      load={load}
      save={(input) => api.updateSSOConfig(input)}
    />
  );
}

function OperationLogConfigPanel({ api }: { api: ControlApi }) {
  const load = useCallback(() => api.getOperationLogConfig(), [api]);
  return (
    <RuntimeConfigPanel
      title="Operation Logs"
      refreshLabel="Refresh operation log config"
      load={load}
      save={(input) => api.updateOperationLogConfig(input)}
    />
  );
}

function RecallFeedbackConfigPanel({ api }: { api: ControlApi }) {
  const load = useCallback(() => api.getRecallFeedbackConfig(), [api]);
  return (
    <RuntimeConfigPanel
      title="Recall Feedback"
      refreshLabel="Refresh recall feedback config"
      load={load}
      save={(input) => api.updateRecallFeedbackConfig(input)}
    />
  );
}

function TelemetryPricingConfigPanel({ api }: { api: ControlApi }) {
  const load = useCallback(() => api.getTelemetryPricingConfig(), [api]);
  return (
    <RuntimeConfigPanel
      title="Telemetry pricing"
      refreshLabel="Refresh telemetry pricing config"
      load={load}
      save={(input) => api.updateTelemetryPricingConfig(input)}
      detail={(config: TelemetryPricingConfig) => (
        <p className="form-meta">
          Verifier model: {config.effective.verifier_model || "Not configured"} · Embedding model: {config.effective.embedding_model || "Not configured"}
        </p>
      )}
    />
  );
}

function RuntimeConfigPanel<T extends RuntimeConfig>({
  title,
  refreshLabel,
  load,
  save,
  detail,
  renderItem,
}: {
  title: string;
  refreshLabel: string;
  load: () => Promise<T>;
  save: (input: RuntimeConfigInput) => Promise<T>;
  detail?: (config: T) => ReactNode;
  renderItem?: (item: ConfigItem, value: string, onChange: (value: string) => void) => ReactNode;
}) {
  const transition = useViewTransition();
  const [config, setConfig] = useState<T | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [loading, setLoading] = useState(false);
  const clearDirty = useUnsavedChanges(Boolean(config && config.items.some((item) => (draft[item.key] ?? "") !== item.value)));

  const loadConfig = useCallback(async () => {
    setLoading(true);
    setError("");
    setMessage("");
    try {
      const next = await load();
      setConfig(next);
      setDraft(Object.fromEntries(next.items.map((item) => [item.key, item.value])));
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }, [load]);

  useEffect(() => {
    void loadConfig();
  }, [loadConfig]);

  async function saveConfig(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError("");
    setMessage("");
    try {
      const next = await save({
        items: Object.entries(draft).map(([key, value]) => ({ key, value })),
      });
      setConfig(next);
      setDraft(Object.fromEntries(next.items.map((item) => [item.key, item.value])));
      clearDirty();
      setMessage("Changes saved");
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  const groups = new Map<string, ConfigItem[]>();
  for (const item of config?.items ?? []) {
    const group = configGroup(item.key);
    groups.set(group, [...(groups.get(group) ?? []), item]);
  }
  return (
    <section className="surface">
      <SectionHeading
        title={title}
        actions={(
          <div className="button-row">
            {config?.update_time && <span className="form-meta">{formatDate(config.update_time)}</span>}
            <Button variant="ghost" size="icon" className="icon-button" type="button" aria-label={refreshLabel} onClick={() => void transition(loadConfig)}>
              <RefreshCw size={16} aria-hidden="true" />
            </Button>
          </div>
        )}
      />
      {config && detail?.(config)}
      {error && <div className="banner error" role="alert">{error}</div>}
      {message && <div className="banner neutral" role="status">{message}</div>}
      {loading && !config ? (
        <LoadingState label="Loading config" compact />
      ) : (
        <form className="edit-grid" onSubmit={saveConfig}>
          {[...groups].map(([group, items]) => <fieldset className="setting-group" key={group}>
            <legend>{group}</legend>
            {items.map((item) => {
              const onChange = (value: string) => setDraft((current) => ({ ...current, [item.key]: value }));
              return <Fragment key={item.key}>{renderItem?.(item, draft[item.key] ?? "", onChange) ?? <ConfigField item={item} value={draft[item.key] ?? ""} onChange={onChange} />}</Fragment>;
            })}
          </fieldset>)}
          <div className="button-row span">
            <Button variant="default" className="primary-button" type="submit" disabled={loading || !config}>
              <Check size={16} aria-hidden="true" />
              Save config
            </Button>
          </div>
        </form>
      )}
    </section>
  );
}

function configGroup(key: string): string {
  if (key.startsWith("ONTOLOGY_MAINTENANCE_")) {
    if (key === "ONTOLOGY_MAINTENANCE_TEAM_OVERRIDES_JSON") return "Team overrides";
    if (key.endsWith("_TOKENS")) return "Model capacity";
    return key.endsWith("_MODEL") || key.endsWith("_CONCURRENCY") ? "Processing" : "Schedule";
  }
  if (key.endsWith("BASE_URL")) return "Public endpoints";
  if (key.startsWith("SSO_")) return "Sessions and security";
  if (key.startsWith("TELEMETRY_COST_")) return key.endsWith("_JSON") ? "Additional models" : "Model pricing";
  if (key.endsWith("RETENTION_DAYS")) return "Retention";
  if (key.endsWith("_ENABLED")) return "Availability";
  return "General";
}

function ConfigField({
  item,
  value,
  onChange,
}: {
  item: GeneralConfigItem | SSOConfigItem | OperationLogConfigItem | RecallFeedbackConfigItem | TelemetryPricingConfigItem;
  value: string;
  onChange: (value: string) => void;
}) {
  const label = CONFIG_LABELS[item.key] ?? item.key;
  const description = CONFIG_DESCRIPTIONS[item.key];
  const fieldLabel = <div className="setting-label"><Label htmlFor={item.key}>{label}</Label>{description && <p id={`${item.key}-help`}>{description}</p>}</div>;

  if (item.key === "SSO_COOKIE_SECURE") {
    return (
      <>
        <div className="setting-row">
          {fieldLabel}
        <NativeSelect id={item.key} aria-describedby={description ? `${item.key}-help` : undefined} value={value} onChange={(event) => onChange(event.target.value)}>
          <NativeSelectOption value="">Auto from SSO public URL</NativeSelectOption>
          <NativeSelectOption value="true">Enabled</NativeSelectOption>
          <NativeSelectOption value="false">Disabled</NativeSelectOption>
        </NativeSelect>
        </div>
      </>
    );
  }

  if ((item.key.startsWith("ONTOLOGY_MAINTENANCE_") || item.key.startsWith("DREAMING_") || item.key.startsWith("COMMUNITY_DETECTION_") || item.key.startsWith("RECALL_FEEDBACK_") || item.key.startsWith("EVALUATION_")) && item.key.endsWith("_ENABLED")) {
    const checked = (value || item.effective_value) === "true";
    return (
      <>
        <div className="setting-row">
          {fieldLabel}
        <div className="button-row">
          <Label className="toggle-row config-toggle" htmlFor={item.key}>
            <span>{checked ? "Enabled" : "Disabled"}</span>
            <Switch
              id={item.key} aria-describedby={description ? `${item.key}-help` : undefined}
              checked={checked}
              onCheckedChange={(event) => onChange((event === true) ? "true" : "false")}
            />
          </Label>
        </div>
        </div>
      </>
    );
  }

  if (item.key === "APP_TIMEZONE") {
    const timezone = value || item.effective_value || "Local";
    return (
      <>
        <div className="setting-row">
          {fieldLabel}
        <div className="button-row">
          <NativeSelect id={item.key} aria-describedby={description ? `${item.key}-help` : undefined} value={timezone} onChange={(event) => onChange(event.target.value)}>
            {timezoneOptions(timezone).map((option) => (
              <NativeSelectOption value={option} key={option}>{option}</NativeSelectOption>
            ))}
          </NativeSelect>
          {value !== "" && (
            <Button variant="ghost" size="icon" className="icon-button" type="button" aria-label={`Clear ${label} override`} title={`Clear ${label} override`} onClick={() => onChange("")}>
              <X size={16} aria-hidden="true" />
            </Button>
          )}
        </div>
        </div>
      </>
    );
  }

  if (item.key === "TELEMETRY_COST_MODEL_PRICES_JSON") {
    return (
      <>
        <div className="setting-row">
          {fieldLabel}
        <Textarea
          id={item.key} aria-describedby={description ? `${item.key}-help` : undefined}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          placeholder={CONFIG_PLACEHOLDERS[item.key]}
          aria-invalid={item.validation_error ? true : undefined}
          rows={5}
        />
        </div>
        {item.validation_error && <p className="field-error span" role="alert">{item.validation_error}</p>}
      </>
    );
  }

  const pricing = item.key.startsWith("TELEMETRY_COST_");
  const ontologyNumber = ["ONTOLOGY_MAINTENANCE_CADENCE_HOURS", "ONTOLOGY_MAINTENANCE_MAX_CONCURRENCY", "ONTOLOGY_MAINTENANCE_TEAM_MAX_CONCURRENCY", "ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS"].includes(item.key);
  const numeric = pricing || item.key.endsWith("_SECONDS") || item.key === "DREAMING_MAX_OUTPUTS" || item.key === "COMMUNITY_DETECTION_MAX_CONCURRENCY" || item.key === "OPERATION_LOG_RETENTION_DAYS" || item.key === "RECALL_FEEDBACK_RETENTION_DAYS";
  const time = item.key === "DREAMING_START_TIME_LOCAL" || item.key === "COMMUNITY_DETECTION_START_TIME_LOCAL";
  const min = item.key === "COMMUNITY_DETECTION_JITTER_SECONDS" ? 0 : numeric && !pricing ? 1 : undefined;
  return (
    <>
      <div className="setting-row">
        {fieldLabel}
      <Input
        id={item.key} aria-describedby={description ? `${item.key}-help` : undefined}
        type={time ? "time" : pricing ? "text" : numeric ? "number" : "text"}
        inputMode={pricing ? "decimal" : ontologyNumber ? "numeric" : undefined}
        className={item.key === "ONTOLOGY_MAINTENANCE_START_TIME_LOCAL" ? "input-time" : undefined}
        min={min}
        max={item.key === "DREAMING_MAX_OUTPUTS" ? 50 : item.key === "COMMUNITY_DETECTION_MAX_CONCURRENCY" ? 8 : item.key === "COMMUNITY_DETECTION_JITTER_SECONDS" ? 3600 : item.key === "OPERATION_LOG_RETENTION_DAYS" || item.key === "RECALL_FEEDBACK_RETENTION_DAYS" ? 365 : undefined}
        placeholder={CONFIG_PLACEHOLDERS[item.key] ?? ""}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        aria-invalid={item.validation_error ? true : undefined}
      />
      </div>
      {item.validation_error && <p className="field-error span" role="alert">{item.validation_error}</p>}
    </>
  );
}

function getSupportedTimezones(): string[] {
  const intl = Intl as typeof Intl & {
    supportedValuesOf?: (key: "timeZone") => string[];
  };
  try {
    return intl.supportedValuesOf?.("timeZone") ?? FALLBACK_TIMEZONES;
  } catch {
    return FALLBACK_TIMEZONES;
  }
}

function timezoneOptions(current: string): string[] {
  const values = new Set<string>(["Local", "UTC", ...SUPPORTED_TIMEZONES, ...FALLBACK_TIMEZONES]);
  if (current.trim()) {
    values.add(current.trim());
  }
  return [...values].sort((left, right) => {
    if (left === "Local") {
      return -1;
    }
    if (right === "Local") {
      return 1;
    }
    if (left === "UTC") {
      return -1;
    }
    if (right === "UTC") {
      return 1;
    }
    return left.localeCompare(right);
  });
}
