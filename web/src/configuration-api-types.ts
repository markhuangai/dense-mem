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

export type DecisionTasks = {
  deduplication: boolean;
  security_review: boolean;
  conflict_review: boolean;
  recall_reranking: boolean;
};

export type DecisionConfig = {
  update_time: string;
  items: Array<SSOConfigItem & { source: "default" | "override" }>;
  effective: {
    revision: string;
    provider: {
      configured: boolean;
      provider: string;
      model: string;
      max_input_tokens: number;
      max_question_input_tokens: number;
      long_context_enabled: boolean;
    };
    preferences: DecisionTasks;
    effective: DecisionTasks;
    overrides: DecisionTasks;
    stale: boolean;
    last_verified_at: string;
  };
};
