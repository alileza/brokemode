// Mirrors internal/metrics.StreamPayload on the Go side.

export interface TelemetrySnapshot {
  time: string;
  gpu_active_ratio: number;
  package_power_w: number;
  memory_pressure_pct: number;
  wired_bytes: number;
  compressed_bytes: number;
  ollama_rss_bytes: number;
  thermal_level: number;
  cpu_speed_limit: number;
}

export interface RequestRecord {
  time: string;
  model: string;
  alias?: string;
  stream: boolean;
  status: number;
  ttft_ms: number;
  decode_tps: number;
  tokens_in: number;
  tokens_out: number;
  peak_rss_bytes: number;
}

export interface StreamPayload {
  telemetry: TelemetrySnapshot;
  budget_gb: number;
  decode_tps: number;
  ttft_ms: number;
  recent: RequestRecord[] | null;
}
