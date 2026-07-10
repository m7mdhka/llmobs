// Wire-shaped entity types (snake_case), matching what the Query API actually
// returns. The generated @llmobs/query-client types are camelCase
// (quicktype nice-property-names), which diverges from the JSON on the wire — so
// the plugin reads these instead. (Reported as a D4 finding: the SDK should
// expose wire-shaped entity types, or codegen should preserve snake_case.)

export interface WireTrace {
  id: string;
  project_id: string;
  name?: string;
  start_time?: string;
  end_time?: string | null;
  status?: { code?: string };
  environment?: string;
  release?: string;
  version?: string;
  session_id?: string;
  user_id?: string;
  span_count?: number;
  is_open?: boolean;
  "llmobs.dq.incomplete_trace"?: boolean;
  attributes?: Record<string, unknown>;
  tags?: string[];
}

export interface WireSpan {
  id: string;
  trace_id: string;
  parent_span_id?: string;
  name?: string;
  kind: string;
  raw_kind?: string | null;
  start_time?: string;
  end_time?: string | null;
  status?: { code?: string; message?: string };
  input?: unknown;
  output?: unknown;
  model?: string;
  provider?: string;
  attributes?: Record<string, unknown>;
  usage_details?: Record<string, number>;
  provided_usage_details?: Record<string, number>;
  cost_details?: Record<string, number>;
  events?: Array<{ name: string; timestamp?: string | number; attributes?: Record<string, unknown> }>;
}

export interface WireTraceTree {
  trace: WireTrace;
  spans: WireSpan[];
}
