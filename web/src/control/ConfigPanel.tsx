import { FormEvent, useEffect, useState } from "react";
import { Check, Moon, RefreshCw, Settings } from "lucide-react";
import { ControlApi, DreamingConfig, DreamingConfigItem, SSOConfig, SSOConfigItem } from "../api";
import { SectionHeading } from "../ui/components";
import { formatDate, readError } from "./utils";

type ConfigTab = "sso" | "dreaming";

const CONFIG_LABELS: Record<string, string> = {
  SSO_PUBLIC_BASE_URL: "Public base URL",
  SSO_ENTITLEMENT_CACHE_TTL_SECONDS: "Entitlement cache TTL",
  SSO_SESSION_TTL_SECONDS: "Session TTL",
  SSO_STATE_TTL_SECONDS: "OAuth state TTL",
  SSO_HTTP_TIMEOUT_SECONDS: "OIDC HTTP timeout",
  SSO_COOKIE_SECURE: "Secure cookies",
  DREAMING_ENABLED: "Enable scheduled cycle",
  DREAMING_FORCE_ENABLED: "Force all teams",
  DREAMING_START_TIME_LOCAL: "Cycle start time",
  DREAMING_TIMEZONE: "Timezone",
  DREAMING_REFLECT_ENABLED: "Reflect phase",
  DREAMING_REEVALUATE_ENABLED: "Re-evaluate phase",
  DREAMING_DREAM_ENABLED: "Dream phase",
  DREAMING_MODEL: "Dream model",
  DREAMING_MAX_OUTPUTS: "Max dream outputs",
};

const CONFIG_PLACEHOLDERS: Record<string, string> = {
  SSO_ENTITLEMENT_CACHE_TTL_SECONDS: "300",
  SSO_SESSION_TTL_SECONDS: "28800",
  SSO_STATE_TTL_SECONDS: "600",
  SSO_HTTP_TIMEOUT_SECONDS: "10",
  DREAMING_START_TIME_LOCAL: "03:00",
  DREAMING_TIMEZONE: "UTC",
  DREAMING_MODEL: "dense-mem.heuristic-dream-v1",
  DREAMING_MAX_OUTPUTS: "5",
};

export function ConfigPanel({ api }: { api: ControlApi }) {
  const [activeTab, setActiveTab] = useState<ConfigTab>("sso");

  return (
    <>
      <section className="surface">
        <SectionHeading
          title="Config"
          actions={(
            <div className="config-tabs" role="tablist" aria-label="Config sections">
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
            </div>
          )}
        />
      </section>
      {activeTab === "sso" && <SSOConfigPanel api={api} />}
      {activeTab === "dreaming" && <DreamingConfigPanel api={api} />}
    </>
  );
}

function SSOConfigPanel({ api }: { api: ControlApi }) {
  const [config, setConfig] = useState<SSOConfig | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [loading, setLoading] = useState(false);

  async function loadConfig() {
    setLoading(true);
    setError("");
    setMessage("");
    try {
      const next = await api.getSSOConfig();
      setConfig(next);
      setDraft(Object.fromEntries(next.items.map((item) => [item.key, item.value])));
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void loadConfig();
  }, []);

  async function saveConfig(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError("");
    setMessage("");
    try {
      const next = await api.updateSSOConfig({
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
        title="SSO"
        actions={(
          <div className="button-row">
            {config?.update_time && <span className="form-meta">{formatDate(config.update_time)}</span>}
            <button className="icon-button" type="button" aria-label="Refresh SSO config" onClick={() => void loadConfig()}>
              <RefreshCw size={16} aria-hidden="true" />
            </button>
          </div>
        )}
      />
      {error && <div className="banner error" role="alert">{error}</div>}
      {message && <div className="banner neutral">{message}</div>}
      {loading && !config ? (
        <div className="table-placeholder compact">Loading</div>
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

function DreamingConfigPanel({ api }: { api: ControlApi }) {
  const [config, setConfig] = useState<DreamingConfig | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [loading, setLoading] = useState(false);

  async function loadConfig() {
    setLoading(true);
    setError("");
    setMessage("");
    try {
      const next = await api.getDreamingConfig();
      setConfig(next);
      setDraft(Object.fromEntries(next.items.map((item) => [item.key, item.value])));
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void loadConfig();
  }, []);

  async function saveConfig(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError("");
    setMessage("");
    try {
      const next = await api.updateDreamingConfig({
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
        title="Dreaming"
        actions={(
          <div className="button-row">
            {config?.update_time && <span className="form-meta">{formatDate(config.update_time)}</span>}
            <button className="icon-button" type="button" aria-label="Refresh dreaming config" onClick={() => void loadConfig()}>
              <RefreshCw size={16} aria-hidden="true" />
            </button>
          </div>
        )}
      />
      {error && <div className="banner error" role="alert">{error}</div>}
      {message && <div className="banner neutral">{message}</div>}
      {loading && !config ? (
        <div className="table-placeholder compact">Loading</div>
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
  item: SSOConfigItem | DreamingConfigItem;
  value: string;
  onChange: (value: string) => void;
}) {
  const label = CONFIG_LABELS[item.key] ?? item.key;
  const effective = item.effective_value ? `effective ${item.effective_value}` : "";

  if (item.key === "SSO_COOKIE_SECURE") {
    return (
      <>
        <label htmlFor={item.key}>{label}</label>
        <select id={item.key} value={value} onChange={(event) => onChange(event.target.value)}>
          <option value="">Auto from public URL</option>
          <option value="true">true</option>
          <option value="false">false</option>
        </select>
        <span className="form-meta span">{effective}</span>
      </>
    );
  }

  if (item.key.startsWith("DREAMING_") && item.key.endsWith("_ENABLED")) {
    return (
      <>
        <label htmlFor={item.key}>{label}</label>
        <select id={item.key} value={value} onChange={(event) => onChange(event.target.value)}>
          <option value="">Default</option>
          <option value="true">true</option>
          <option value="false">false</option>
        </select>
        <span className="form-meta span">{effective}</span>
      </>
    );
  }

  const numeric = item.key.endsWith("_SECONDS") || item.key === "DREAMING_MAX_OUTPUTS";
  const time = item.key === "DREAMING_START_TIME_LOCAL";
  return (
    <>
      <label htmlFor={item.key}>{label}</label>
      <input
        id={item.key}
        type={time ? "time" : numeric ? "number" : "text"}
        min={numeric ? 1 : undefined}
        max={item.key === "DREAMING_MAX_OUTPUTS" ? 50 : undefined}
        placeholder={CONFIG_PLACEHOLDERS[item.key] ?? ""}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
      <span className="form-meta span">{effective}</span>
    </>
  );
}
