export type TelemetryWindowKey = "15m" | "30m" | "1h" | "12h" | "1d" | "7d" | "30d";

export type TelemetryWindow = {
  key: TelemetryWindowKey;
  from: string;
  to: string;
  step_seconds: number;
  retention_days: number;
};

export type TelemetryScope = {
  type: "system" | "team" | "profile" | "self";
  team_id?: string;
  profile_id?: string;
};

export type TelemetryCard = {
  id: string;
  label: string;
  unit: string;
  value: number;
  available?: boolean;
  status?: "ready" | "inactive" | "unavailable" | "unsupported";
  reason_code?: string;
  reason?: string;
};

export type TelemetryPoint = {
  timestamp: string;
  value: number;
};

export type TelemetrySeries = {
  id: string;
  label: string;
  unit: string;
  points: TelemetryPoint[];
  status?: "ready" | "inactive" | "unavailable" | "unsupported";
  reason_code?: string;
  reason?: string;
};

export type TelemetrySnapshot = {
  available: boolean;
  status?: "ready" | "degraded" | "unavailable";
  generated_at?: string;
  message?: string;
  window: TelemetryWindow;
  scope: TelemetryScope;
  cards: TelemetryCard[];
  windowed_cards?: TelemetryCard[];
  current_cards?: TelemetryCard[];
  series: TelemetrySeries[];
  activity_series?: TelemetrySeries[];
  state_series?: TelemetrySeries[];
};

export type ControlTelemetryQuery = {
  window?: TelemetryWindowKey;
  scope?: "system" | "team" | "profile";
  team_id?: string;
  profile_id?: string;
};

export type UserTelemetryQuery = {
  window?: TelemetryWindowKey;
};

export const telemetryWindowOptions: { value: TelemetryWindowKey; label: string }[] = [
  { value: "15m", label: "15 min" },
  { value: "30m", label: "30 min" },
  { value: "1h", label: "1 hour" },
  { value: "12h", label: "12 hours" },
  { value: "1d", label: "1 day" },
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
];
