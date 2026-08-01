import { FormEvent, ReactNode, useCallback, useEffect, useState } from "react";
import { Check, Clock, ListFilter, MessageSquare, Moon, Network, RefreshCw, Settings, X } from "lucide-react";
import { CommunityDetectionConfig, CommunityDetectionConfigItem, ControlApi, DreamingConfig, DreamingConfigItem, EvaluationConfig, EvaluationConfigItem, GeneralConfig, GeneralConfigItem, OperationLogConfig, OperationLogConfigItem, RecallFeedbackConfig, RecallFeedbackConfigItem, SSOConfig, SSOConfigItem, TelemetryPricingConfig, TelemetryPricingConfigItem } from "../api";
import { LoadingState, SectionHeading } from "../ui/components";
import { formatDate, readError } from "./utils";

type ConfigTab = "general" | "sso" | "dreaming" | "community" | "operation-logs" | "recall-feedback" | "evaluation" | "telemetry-pricing";

const CONFIG_LABELS: Record<string, string> = {
  APP_TIMEZONE: "Timezone",
  SSO_PUBLIC_BASE_URL: "Public base URL",
  SSO_ENTITLEMENT_CACHE_TTL_SECONDS: "Entitlement cache TTL",
  SSO_SESSION_TTL_SECONDS: "Session TTL",
  SSO_STATE_TTL_SECONDS: "OAuth state TTL",
  SSO_HTTP_TIMEOUT_SECONDS: "OIDC HTTP timeout",
  SSO_COOKIE_SECURE: "Secure cookies",
  DREAMING_ENABLED: "Enable scheduled cycle",
  DREAMING_FORCE_ENABLED: "Force all teams",
  DREAMING_START_TIME_LOCAL: "Cycle start time",
  DREAMING_MAX_OUTPUTS: "Max dream outputs",
  COMMUNITY_DETECTION_ENABLED: "Enable scheduled detection",
  COMMUNITY_DETECTION_START_TIME_LOCAL: "Detection start time",
  COMMUNITY_DETECTION_MAX_CONCURRENCY: "Max concurrency",
  COMMUNITY_DETECTION_JITTER_SECONDS: "Jitter seconds",
  OPERATION_LOG_RETENTION_DAYS: "Retention days",
  RECALL_FEEDBACK_ENABLED: "Enable recall feedback",
  RECALL_FEEDBACK_RETENTION_DAYS: "Investigation retention days",
  EVALUATION_MODE_ENABLED: "Evaluation mode",
  EVALUATION_EXPORT_MAX_PAGE_SIZE: "Max export page size",
  TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS: "Verifier input USD / million tokens",
  TELEMETRY_COST_VERIFIER_OUTPUT_USD_PER_MILLION_TOKENS: "Verifier output USD / million tokens",
  TELEMETRY_COST_EMBEDDING_INPUT_USD_PER_MILLION_TOKENS: "Embedding input USD / million tokens",
};

const CONFIG_PLACEHOLDERS: Record<string, string> = {
  APP_TIMEZONE: "Local",
  SSO_ENTITLEMENT_CACHE_TTL_SECONDS: "300",
  SSO_SESSION_TTL_SECONDS: "28800",
  SSO_STATE_TTL_SECONDS: "600",
  SSO_HTTP_TIMEOUT_SECONDS: "10",
  DREAMING_START_TIME_LOCAL: "03:00",
  DREAMING_MAX_OUTPUTS: "5",
  COMMUNITY_DETECTION_START_TIME_LOCAL: "03:30",
  COMMUNITY_DETECTION_MAX_CONCURRENCY: "1",
  COMMUNITY_DETECTION_JITTER_SECONDS: "600",
  OPERATION_LOG_RETENTION_DAYS: "30",
  RECALL_FEEDBACK_RETENTION_DAYS: "30",
  EVALUATION_EXPORT_MAX_PAGE_SIZE: "100",
  TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS: "Leave blank to mark as unpriced",
  TELEMETRY_COST_VERIFIER_OUTPUT_USD_PER_MILLION_TOKENS: "Leave blank to mark as unpriced",
  TELEMETRY_COST_EMBEDDING_INPUT_USD_PER_MILLION_TOKENS: "Leave blank to mark as unpriced",
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

export function ConfigPanel({ api }: { api: ControlApi }) {
  const [activeTab, setActiveTab] = useState<ConfigTab>("general");

  return (
    <>
      <div className="config-tabs" role="tablist" aria-label="Config sections">
        <button
          className={activeTab === "general" ? "tab-button active" : "tab-button"}
          type="button"
          role="tab"
          aria-selected={activeTab === "general"}
          onClick={() => setActiveTab("general")}
        >
          <Clock size={16} aria-hidden="true" />
          <span>General</span>
        </button>
        <button
          className={activeTab === "sso" ? "tab-button active" : "tab-button"}
          type="button"
          role="tab"
          aria-selected={activeTab === "sso"}
          onClick={() => setActiveTab("sso")}
        >
          <Settings size={16} aria-hidden="true" />
          <span>SSO</span>
        </button>
        <button
          className={activeTab === "dreaming" ? "tab-button active" : "tab-button"}
          type="button"
          role="tab"
          aria-selected={activeTab === "dreaming"}
          onClick={() => setActiveTab("dreaming")}
        >
          <Moon size={16} aria-hidden="true" />
          <span>Dreaming</span>
        </button>
        <button
          className={activeTab === "community" ? "tab-button active" : "tab-button"}
          type="button"
          role="tab"
          aria-selected={activeTab === "community"}
          onClick={() => setActiveTab("community")}
        >
          <Network size={16} aria-hidden="true" />
          <span>Community</span>
        </button>
        <button
          className={activeTab === "recall-feedback" ? "tab-button active" : "tab-button"}
          type="button"
          role="tab"
          aria-selected={activeTab === "recall-feedback"}
          onClick={() => setActiveTab("recall-feedback")}
        >
          <MessageSquare size={16} aria-hidden="true" />
          <span>Recall</span>
        </button>
        <button
          className={activeTab === "evaluation" ? "tab-button active" : "tab-button"}
          type="button"
          role="tab"
          aria-selected={activeTab === "evaluation"}
          onClick={() => setActiveTab("evaluation")}
        >
          <Settings size={16} aria-hidden="true" />
          <span>Evaluation</span>
        </button>
        <button
          className={activeTab === "telemetry-pricing" ? "tab-button active" : "tab-button"}
          type="button"
          role="tab"
          aria-selected={activeTab === "telemetry-pricing"}
          onClick={() => setActiveTab("telemetry-pricing")}
        >
          <Settings size={16} aria-hidden="true" />
          <span>Telemetry</span>
        </button>
        <button
          className={activeTab === "operation-logs" ? "tab-button active" : "tab-button"}
          type="button"
          role="tab"
          aria-selected={activeTab === "operation-logs"}
          onClick={() => setActiveTab("operation-logs")}
        >
          <ListFilter size={16} aria-hidden="true" />
          <span>Logs</span>
        </button>
      </div>
      {activeTab === "general" && <GeneralConfigPanel api={api} />}
      {activeTab === "sso" && <SSOConfigPanel api={api} />}
      {activeTab === "dreaming" && <DreamingConfigPanel api={api} />}
      {activeTab === "community" && <CommunityDetectionConfigPanel api={api} />}
      {activeTab === "recall-feedback" && <RecallFeedbackConfigPanel api={api} />}
      {activeTab === "evaluation" && <EvaluationConfigPanel api={api} />}
      {activeTab === "telemetry-pricing" && <TelemetryPricingConfigPanel api={api} />}
      {activeTab === "operation-logs" && <OperationLogConfigPanel api={api} />}
    </>
  );
}

type ConfigItem = GeneralConfigItem | SSOConfigItem | DreamingConfigItem | CommunityDetectionConfigItem | OperationLogConfigItem | RecallFeedbackConfigItem | EvaluationConfigItem | TelemetryPricingConfigItem;
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

function GeneralConfigPanel({ api }: { api: ControlApi }) {
  return (
    <RuntimeConfigPanel
      title="General"
      refreshLabel="Refresh general config"
      load={() => api.getGeneralConfig()}
      save={(input) => api.updateGeneralConfig(input)}
    />
  );
}

function SSOConfigPanel({ api }: { api: ControlApi }) {
  return (
    <RuntimeConfigPanel
      title="SSO"
      refreshLabel="Refresh SSO config"
      load={() => api.getSSOConfig()}
      save={(input) => api.updateSSOConfig(input)}
    />
  );
}

function DreamingConfigPanel({ api }: { api: ControlApi }) {
  return (
    <RuntimeConfigPanel
      title="Dreaming"
      refreshLabel="Refresh dreaming config"
      load={() => api.getDreamingConfig()}
      save={(input) => api.updateDreamingConfig(input)}
    />
  );
}

function CommunityDetectionConfigPanel({ api }: { api: ControlApi }) {
  return (
    <RuntimeConfigPanel
      title="Community Detection"
      refreshLabel="Refresh community detection config"
      load={() => api.getCommunityDetectionConfig()}
      save={(input) => api.updateCommunityDetectionConfig(input)}
    />
  );
}

function OperationLogConfigPanel({ api }: { api: ControlApi }) {
  return (
    <RuntimeConfigPanel
      title="Operation Logs"
      refreshLabel="Refresh operation log config"
      load={() => api.getOperationLogConfig()}
      save={(input) => api.updateOperationLogConfig(input)}
    />
  );
}

function RecallFeedbackConfigPanel({ api }: { api: ControlApi }) {
  return (
    <RuntimeConfigPanel
      title="Recall Feedback"
      refreshLabel="Refresh recall feedback config"
      load={() => api.getRecallFeedbackConfig()}
      save={(input) => api.updateRecallFeedbackConfig(input)}
    />
  );
}

function EvaluationConfigPanel({ api }: { api: ControlApi }) {
  return (
    <RuntimeConfigPanel
      title="Evaluation"
      refreshLabel="Refresh evaluation config"
      load={() => api.getEvaluationConfig()}
      save={(input) => api.updateEvaluationConfig(input)}
    />
  );
}

function TelemetryPricingConfigPanel({ api }: { api: ControlApi }) {
  return (
    <RuntimeConfigPanel
      title="Telemetry pricing"
      refreshLabel="Refresh telemetry pricing config"
      load={() => api.getTelemetryPricingConfig()}
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
}: {
  title: string;
  refreshLabel: string;
  load: () => Promise<T>;
  save: (input: RuntimeConfigInput) => Promise<T>;
  detail?: (config: T) => ReactNode;
}) {
  const [config, setConfig] = useState<T | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [loading, setLoading] = useState(false);

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
      setMessage("Saved");
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <section className="surface">
      <SectionHeading
        title={title}
        actions={(
          <div className="button-row">
            {config?.update_time && <span className="form-meta">{formatDate(config.update_time)}</span>}
            <button className="icon-button" type="button" aria-label={refreshLabel} onClick={() => void loadConfig()}>
              <RefreshCw size={16} aria-hidden="true" />
            </button>
          </div>
        )}
      />
      {config && detail?.(config)}
      {error && <div className="banner error" role="alert">{error}</div>}
      {message && <div className="banner neutral">{message}</div>}
      {loading && !config ? (
        <LoadingState label="Loading config" compact />
      ) : (
        <form className="edit-grid" onSubmit={saveConfig}>
          {(config?.items ?? []).map((item) => (
            <ConfigField
              key={item.key}
              item={item}
              value={draft[item.key] ?? ""}
              onChange={(value) => setDraft((current) => ({ ...current, [item.key]: value }))}
            />
          ))}
          <div className="button-row span">
            <button className="primary-button" type="submit" disabled={loading || !config}>
              <Check size={16} aria-hidden="true" />
              Save config
            </button>
          </div>
        </form>
      )}
    </section>
  );
}

function ConfigField({
  item,
  value,
  onChange,
}: {
  item: GeneralConfigItem | SSOConfigItem | DreamingConfigItem | CommunityDetectionConfigItem | OperationLogConfigItem | RecallFeedbackConfigItem | EvaluationConfigItem | TelemetryPricingConfigItem;
  value: string;
  onChange: (value: string) => void;
}) {
  const label = CONFIG_LABELS[item.key] ?? item.key;

  if (item.key === "SSO_COOKIE_SECURE") {
    return (
      <>
        <label htmlFor={item.key}>{label}</label>
        <select id={item.key} value={value} onChange={(event) => onChange(event.target.value)}>
          <option value="">Auto from public URL</option>
          <option value="true">Enabled</option>
          <option value="false">Disabled</option>
        </select>
      </>
    );
  }

  if ((item.key.startsWith("DREAMING_") || item.key.startsWith("COMMUNITY_DETECTION_") || item.key.startsWith("RECALL_FEEDBACK_") || item.key.startsWith("EVALUATION_")) && item.key.endsWith("_ENABLED")) {
    const checked = (value || item.effective_value) === "true";
    return (
      <>
        <label htmlFor={item.key}>{label}</label>
        <div className="button-row">
          <label className="toggle-row config-toggle" htmlFor={item.key}>
            <span>{checked ? "Enabled" : "Disabled"}</span>
            <input
              id={item.key}
              type="checkbox"
              checked={checked}
              onChange={(event) => onChange(event.target.checked ? "true" : "false")}
            />
          </label>
        </div>
      </>
    );
  }

  if (item.key === "APP_TIMEZONE") {
    const timezone = value || item.effective_value || "Local";
    return (
      <>
        <label htmlFor={item.key}>{label}</label>
        <div className="button-row">
          <select id={item.key} value={timezone} onChange={(event) => onChange(event.target.value)}>
            {timezoneOptions(timezone).map((option) => (
              <option value={option} key={option}>{option}</option>
            ))}
          </select>
          {value !== "" && (
            <button className="icon-button" type="button" aria-label={`Clear ${label} override`} title={`Clear ${label} override`} onClick={() => onChange("")}>
              <X size={16} aria-hidden="true" />
            </button>
          )}
        </div>
      </>
    );
  }

  const pricing = item.key.startsWith("TELEMETRY_COST_");
  const numeric = pricing || item.key.endsWith("_SECONDS") || item.key === "DREAMING_MAX_OUTPUTS" || item.key === "COMMUNITY_DETECTION_MAX_CONCURRENCY" || item.key === "OPERATION_LOG_RETENTION_DAYS" || item.key === "RECALL_FEEDBACK_RETENTION_DAYS" || item.key === "EVALUATION_EXPORT_MAX_PAGE_SIZE";
  const time = item.key === "DREAMING_START_TIME_LOCAL" || item.key === "COMMUNITY_DETECTION_START_TIME_LOCAL";
  const min = item.key === "COMMUNITY_DETECTION_JITTER_SECONDS" ? 0 : numeric && !pricing ? 1 : undefined;
  return (
    <>
      <label htmlFor={item.key}>{label}</label>
      <input
        id={item.key}
        type={time ? "time" : pricing ? "text" : numeric ? "number" : "text"}
        inputMode={pricing ? "decimal" : undefined}
        min={min}
        max={item.key === "DREAMING_MAX_OUTPUTS" ? 50 : item.key === "COMMUNITY_DETECTION_MAX_CONCURRENCY" ? 8 : item.key === "COMMUNITY_DETECTION_JITTER_SECONDS" ? 3600 : item.key === "OPERATION_LOG_RETENTION_DAYS" || item.key === "RECALL_FEEDBACK_RETENTION_DAYS" ? 365 : item.key === "EVALUATION_EXPORT_MAX_PAGE_SIZE" ? 500 : undefined}
        placeholder={CONFIG_PLACEHOLDERS[item.key] ?? ""}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        aria-invalid={item.validation_error ? true : undefined}
      />
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
