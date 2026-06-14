import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { X } from "lucide-react";
import type { DreamingEffectiveConfig } from "./api";
import { SectionHeading } from "./ui/components";

type DreamingBooleanKey = "enabled" | "reflect_enabled" | "reevaluate_enabled" | "dream_enabled";

type TeamDreamingDraft = {
  enabled?: boolean;
  reflect_enabled?: boolean;
  reevaluate_enabled?: boolean;
  dream_enabled?: boolean;
};

type TeamDreamingConfigFormProps = {
  config: Record<string, unknown> | null | undefined;
  effective?: DreamingEffectiveConfig | null;
  disabled?: boolean;
  onSave: (config: Record<string, unknown>) => Promise<void>;
};

const BOOLEAN_FIELDS: Array<{ key: DreamingBooleanKey; label: string }> = [
  { key: "enabled", label: "Scheduled cycle" },
  { key: "reflect_enabled", label: "Reflect phase" },
  { key: "reevaluate_enabled", label: "Re-evaluate phase" },
  { key: "dream_enabled", label: "Dream phase" },
];

export function TeamDreamingConfigForm({ config, effective, disabled = false, onSave }: TeamDreamingConfigFormProps) {
  const [draft, setDraft] = useState<TeamDreamingDraft>(() => draftFromConfig(config));
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setDraft(draftFromConfig(config));
    setError("");
  }, [config]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const validationError = validateDraft(draft);
    if (validationError) {
      setError(validationError);
      setStatus("");
      return;
    }
    setBusy(true);
    setError("");
    setStatus("");
    try {
      await onSave(buildTeamConfigWithDreaming(config, draft));
      setStatus("Saved");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Request failed.");
    } finally {
      setBusy(false);
    }
  }

  function setBoolean(key: DreamingBooleanKey, value: boolean | undefined) {
    setDraft((current) => ({ ...current, [key]: value }));
  }

  return (
    <div className="team-dreaming-panel">
      <SectionHeading title="Dreaming" />
      {error && <div className="banner error" role="alert">{error}</div>}
      {status && <div className="banner neutral" role="status">{status}</div>}
      <form className="team-dreaming-form" onSubmit={submit}>
        {BOOLEAN_FIELDS.map((field) => (
          <BooleanOverride
            key={field.key}
            label={field.label}
            value={draft[field.key]}
            effectiveValue={effective?.[field.key] ?? false}
            forced={field.key === "enabled" && effective?.force_enabled === true}
            disabled={disabled || busy}
            onChange={(value) => setBoolean(field.key, value)}
          />
        ))}
        {effective && (
          <div className="dreaming-effective-grid span">
            <span>Cycle start</span>
            <strong>{effective.start_time_local}</strong>
            <span>Timezone</span>
            <strong>{effective.timezone}</strong>
            <span>Max outputs</span>
            <strong>{effective.max_outputs}</strong>
            <span>Source</span>
            <strong>{effective.source === "global_force" ? "Global force" : effective.source === "team" ? "Team" : "Global"}</strong>
          </div>
        )}
        <div className="button-row span">
          <button className="primary-button" type="submit" disabled={disabled || busy}>
            Save dreaming
          </button>
        </div>
      </form>
    </div>
  );
}

function BooleanOverride({
  label,
  value,
  effectiveValue,
  forced,
  disabled,
  onChange,
}: {
  label: string;
  value: boolean | undefined;
  effectiveValue: boolean;
  forced: boolean;
  disabled: boolean;
  onChange: (value: boolean | undefined) => void;
}) {
  const id = `team-dreaming-${label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;
  const hasOverride = value !== undefined;
  const checked = forced ? true : value ?? effectiveValue;
  return (
    <div className="team-dreaming-row">
      <label htmlFor={id}>{label}</label>
      <div className="button-row">
        <div className="toggle-row config-toggle">
          <input
            id={id}
            type="checkbox"
            checked={checked}
            disabled={disabled || forced}
            onChange={(event) => onChange(event.target.checked)}
          />
          <span aria-hidden="true">{checked ? "Enabled" : "Disabled"}</span>
        </div>
        {forced ? (
          <span className="form-meta">Forced</span>
        ) : hasOverride ? (
          <button className="icon-button" type="button" aria-label={`Clear ${label} override`} title={`Clear ${label} override`} disabled={disabled} onClick={() => onChange(undefined)}>
            <X size={16} aria-hidden="true" />
          </button>
        ) : (
          <span className="form-meta">Inherited</span>
        )}
      </div>
    </div>
  );
}

function draftFromConfig(config: Record<string, unknown> | null | undefined): TeamDreamingDraft {
  const dreaming = asRecord(config?.dreaming);
  if (!dreaming) {
    return {};
  }
  return {
    enabled: boolFromUnknown(dreaming.enabled),
    reflect_enabled: boolFromUnknown(dreaming.reflect_enabled),
    reevaluate_enabled: boolFromUnknown(dreaming.reevaluate_enabled),
    dream_enabled: boolFromUnknown(dreaming.dream_enabled),
  };
}

function buildTeamConfigWithDreaming(config: Record<string, unknown> | null | undefined, draft: TeamDreamingDraft): Record<string, unknown> {
  const next = { ...(asRecord(config) ?? {}) };
  const existingDreaming = asRecord(next.dreaming);
  const dreaming = { ...(existingDreaming ?? {}) };
  delete dreaming.start_time_local;
  delete dreaming.timezone;
  delete dreaming.max_outputs;

  setOptional(dreaming, "enabled", draft.enabled);
  setOptional(dreaming, "reflect_enabled", draft.reflect_enabled);
  setOptional(dreaming, "reevaluate_enabled", draft.reevaluate_enabled);
  setOptional(dreaming, "dream_enabled", draft.dream_enabled);

  if (Object.keys(dreaming).length > 0) {
    next.dreaming = dreaming;
  } else {
    delete next.dreaming;
  }
  return next;
}

function setOptional(target: Record<string, unknown>, key: string, value: unknown | undefined) {
  if (value === undefined) {
    delete target[key];
    return;
  }
  target[key] = value;
}

function validateDraft(_draft: TeamDreamingDraft): string {
  return "";
}

function asRecord(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return null;
  }
  return value as Record<string, unknown>;
}

function boolFromUnknown(value: unknown): boolean | undefined {
  if (typeof value === "boolean") {
    return value;
  }
  if (typeof value === "string") {
    const normalized = value.trim().toLowerCase();
    if (normalized === "true") {
      return true;
    }
    if (normalized === "false") {
      return false;
    }
  }
  return undefined;
}
