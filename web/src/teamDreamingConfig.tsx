import { useEffect, useMemo, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { X } from "lucide-react";
import { SectionHeading } from "./ui/components";

type DreamingBooleanKey = "enabled" | "reflect_enabled" | "reevaluate_enabled" | "dream_enabled";
type DreamingStringKey = "start_time_local" | "timezone";

type TeamDreamingDraft = {
  enabled?: boolean;
  reflect_enabled?: boolean;
  reevaluate_enabled?: boolean;
  dream_enabled?: boolean;
  start_time_local?: string;
  timezone?: string;
  max_outputs?: string;
};

type TeamDreamingConfigFormProps = {
  config: Record<string, unknown> | null | undefined;
  disabled?: boolean;
  onSave: (config: Record<string, unknown>) => Promise<void>;
};

const BOOLEAN_FIELDS: Array<{ key: DreamingBooleanKey; label: string }> = [
  { key: "enabled", label: "Scheduled cycle" },
  { key: "reflect_enabled", label: "Reflect phase" },
  { key: "reevaluate_enabled", label: "Re-evaluate phase" },
  { key: "dream_enabled", label: "Dream phase" },
];

const FALLBACK_TIMEZONES = ["UTC", "America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles", "Europe/London", "Europe/Paris", "Asia/Tokyo", "Asia/Singapore", "Australia/Sydney"];
const SUPPORTED_TIMEZONES = getSupportedTimezones();

export function TeamDreamingConfigForm({ config, disabled = false, onSave }: TeamDreamingConfigFormProps) {
  const [draft, setDraft] = useState<TeamDreamingDraft>(() => draftFromConfig(config));
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  const timezone = draft.timezone ?? "";
  const timezoneValues = useMemo(() => timezoneOptions(timezone), [timezone]);

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

  function setString(key: DreamingStringKey, value: string | undefined) {
    setDraft((current) => ({ ...current, [key]: value }));
  }

  function setMaxOutputs(value: string | undefined) {
    setDraft((current) => ({ ...current, max_outputs: value }));
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
            disabled={disabled || busy}
            onChange={(value) => setBoolean(field.key, value)}
          />
        ))}
        <FieldOverride label="Cycle start time" htmlFor="team-dreaming-start-time" hasOverride={draft.start_time_local !== undefined} disabled={disabled || busy} onClear={() => setString("start_time_local", undefined)}>
          <input
            id="team-dreaming-start-time"
            type="time"
            value={draft.start_time_local ?? ""}
            disabled={disabled || busy}
            onChange={(event) => setString("start_time_local", event.target.value || undefined)}
          />
        </FieldOverride>
        <FieldOverride label="Timezone" htmlFor="team-dreaming-timezone" hasOverride={draft.timezone !== undefined} disabled={disabled || busy} onClear={() => setString("timezone", undefined)}>
          <select
            id="team-dreaming-timezone"
            value={timezone}
            disabled={disabled || busy}
            onChange={(event) => setString("timezone", event.target.value || undefined)}
          >
            <option value="">Inherited</option>
            {timezoneValues.map((option) => (
              <option value={option} key={option}>{option}</option>
            ))}
          </select>
        </FieldOverride>
        <FieldOverride label="Max dream outputs" htmlFor="team-dreaming-max-outputs" hasOverride={draft.max_outputs !== undefined} disabled={disabled || busy} onClear={() => setMaxOutputs(undefined)}>
          <input
            id="team-dreaming-max-outputs"
            type="number"
            min={1}
            max={50}
            inputMode="numeric"
            value={draft.max_outputs ?? ""}
            disabled={disabled || busy}
            onChange={(event) => setMaxOutputs(event.target.value || undefined)}
          />
        </FieldOverride>
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
  disabled,
  onChange,
}: {
  label: string;
  value: boolean | undefined;
  disabled: boolean;
  onChange: (value: boolean | undefined) => void;
}) {
  const id = `team-dreaming-${label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;
  const hasOverride = value !== undefined;
  const checked = value ?? false;
  return (
    <div className="team-dreaming-row">
      <label htmlFor={id}>{label}</label>
      <div className="button-row">
        <div className="toggle-row config-toggle">
          <input
            id={id}
            type="checkbox"
            checked={checked}
            disabled={disabled}
            onChange={(event) => onChange(event.target.checked)}
          />
          <span aria-hidden="true">{checked ? "Enabled" : "Disabled"}</span>
        </div>
        {hasOverride ? (
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

function FieldOverride({
  label,
  htmlFor,
  hasOverride,
  disabled,
  children,
  onClear,
}: {
  label: string;
  htmlFor: string;
  hasOverride: boolean;
  disabled: boolean;
  children: ReactNode;
  onClear: () => void;
}) {
  return (
    <div className="team-dreaming-row">
      <label htmlFor={htmlFor}>{label}</label>
      <div className="button-row">
        {children}
        {hasOverride ? (
          <button className="icon-button" type="button" aria-label={`Clear ${label} override`} title={`Clear ${label} override`} disabled={disabled} onClick={onClear}>
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
    start_time_local: stringFromUnknown(dreaming.start_time_local),
    timezone: stringFromUnknown(dreaming.timezone),
    max_outputs: numberStringFromUnknown(dreaming.max_outputs),
  };
}

function buildTeamConfigWithDreaming(config: Record<string, unknown> | null | undefined, draft: TeamDreamingDraft): Record<string, unknown> {
  const next = { ...(asRecord(config) ?? {}) };
  const existingDreaming = asRecord(next.dreaming);
  const dreaming = { ...(existingDreaming ?? {}) };

  setOptional(dreaming, "enabled", draft.enabled);
  setOptional(dreaming, "reflect_enabled", draft.reflect_enabled);
  setOptional(dreaming, "reevaluate_enabled", draft.reevaluate_enabled);
  setOptional(dreaming, "dream_enabled", draft.dream_enabled);
  setOptional(dreaming, "start_time_local", nonEmpty(draft.start_time_local));
  setOptional(dreaming, "timezone", nonEmpty(draft.timezone));
  setOptional(dreaming, "max_outputs", draft.max_outputs === undefined ? undefined : Number.parseInt(draft.max_outputs, 10));

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

function validateDraft(draft: TeamDreamingDraft): string {
  if (draft.start_time_local !== undefined && !/^\d{2}:\d{2}$/.test(draft.start_time_local)) {
    return "Cycle start time must use HH:MM.";
  }
  if (draft.max_outputs !== undefined) {
    const value = Number.parseInt(draft.max_outputs, 10);
    if (!Number.isFinite(value) || value < 1 || value > 50) {
      return "Max dream outputs must be between 1 and 50.";
    }
  }
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

function stringFromUnknown(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function numberStringFromUnknown(value: unknown): string | undefined {
  if (typeof value === "number" && Number.isFinite(value)) {
    return String(value);
  }
  if (typeof value === "string" && value.trim()) {
    return value.trim();
  }
  return undefined;
}

function nonEmpty(value: string | undefined): string | undefined {
  return value?.trim() || undefined;
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
  const values = new Set<string>(["UTC", ...SUPPORTED_TIMEZONES, ...FALLBACK_TIMEZONES]);
  if (current.trim()) {
    values.add(current.trim());
  }
  return [...values].sort((left, right) => {
    if (left === "UTC") {
      return -1;
    }
    if (right === "UTC") {
      return 1;
    }
    return left.localeCompare(right);
  });
}
