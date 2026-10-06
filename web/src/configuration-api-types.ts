import type { SSOConfigItem, GeneralRuntimeConfig } from "./api";

export type GeneralConfigItem = SSOConfigItem;

export type GeneralConfig = {
  update_time: string;
  items: GeneralConfigItem[];
  effective: GeneralRuntimeConfig;
};

export type GeneralConfigInput = {
  items: Array<{
    key: string;
    value: string;
  }>;
};

export type SSOConfigInput = {
  items: Array<{
    key: string;
    value: string;
  }>;
};

export type DreamingRuntimeConfig = {
  enabled: boolean;
  force_enabled: boolean;
  start_time_local: string;
  timezone: string;
  max_outputs: number;
};

export type DreamingEffectiveConfig = DreamingRuntimeConfig & {
  team_enabled: boolean;
  source: "global" | "team" | "global_force" | string;
};
