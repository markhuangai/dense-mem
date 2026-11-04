export type ProviderTestTarget = {
  target_id: string;
  endpoint: string;
  model: string;
  roles: string[];
  operation: "assess" | "embed" | "decide";
  credential_configuration: string;
  configuration_revision: string;
  available: boolean;
  unavailable_reason?: string;
  timeout_ms: number;
};

export type ProviderTestExchange = {
  exchange_id: string;
  status_code: number;
  duration_ms: number;
  dns_ms: number | null;
  connect_ms: number | null;
  tls_ms: number | null;
  first_byte_ms: number | null;
  connection_reused: boolean | null;
  failure_phase?: string;
  error?: string;
  request_capture_state: string;
  response_capture_state: string;
  capture_reason?: string;
};

export type ProviderTestResult = {
  test_id: string;
  correlation_id: string;
  server_instance: string;
  started_at: string;
  duration_ms: number;
  target: ProviderTestTarget;
  sample_version: string;
  outcome: "succeeded" | "failed" | "cancelled";
  reason: string;
  failure_phase?: string;
  guidance: string;
  error?: string;
  http_status: number | null;
  network_state: "not_dispatched" | "no_http_response" | "http_response_received";
  diagnostics?: { queue_ms?: number; admission_ms?: number; http_ms?: number; http_timeout_ms?: number; failure_phase?: string };
  exchanges: ProviderTestExchange[];
  capture_state: string;
  detailed_logs_available: boolean;
};
