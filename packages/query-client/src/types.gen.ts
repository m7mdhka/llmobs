export interface LLMObsCanonicalSpanV1Alpha1 {
    /**
     * Open key/value map. Holds every non-promoted attribute; raw attributes always preserved.
     * Keys under 'llmobs.*' are kernel-owned (02-span.md §6).
     */
    attributes: { [key: string]: any };
    /**
     * Generation field: time to first token.
     */
    completionStartTime?: Date | null;
    /**
     * Cost amounts as decimals (adapters preserve >= 12 fractional digits). Same well-known
     * keys as usageMap.
     */
    costDetails?: { [key: string]: number };
    costSource?:  CostSource | null;
    /**
     * Absent/null ⇒ open or point_event (02-span.md §3).
     */
    endTime?: Date | null;
    /**
     * Sanitized low-cardinality dimension (08-data-quality.md §2). Frozen.
     */
    environment: string;
    /**
     * Span events (02-span.md §4.4); union-merged; present on all shapes; not promoted.
     */
    events?: Event[];
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    id: string;
    /**
     * Opaque payload; MAY contain media tokens (02-span.md §4.3, §7).
     */
    input?: any;
    /**
     * MIME-style rendering hint for input; NOT queryable/promoted (02-span.md §4.3, F6).
     */
    inputContentType?: null | string;
    /**
     * Closed canonical enum (02-span.md §2).
     */
    kind: Kind;
    /**
     * Generation field: the resolved/served model (e.g. gen_ai.response.model); requested model
     * preserved under llmobs.raw.model_requested (02-span.md §5, F7).
     */
    model?: null | string;
    /**
     * Generation field: request parameters as verbatim strings, keyed by name; not parsed at
     * normalize time (F2).
     */
    modelParameters?: { [key: string]: string } | null;
    name:             string;
    /**
     * Opaque payload.
     */
    output?: any;
    /**
     * Rendering hint for output; not promoted.
     */
    outputContentType?: null | string;
    /**
     * In-trace parent; null for a trace-root span.
     */
    parentSpanID?:       null | string;
    pricingSnapshotRef?: PricingSnapshotRef | null;
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    projectID: string;
    /**
     * Optional prompt linkage as a reference, not a promoted column (02-span.md §5.4).
     */
    promptRef?: PromptRef | null;
    /**
     * Cost amounts as decimals (adapters preserve >= 12 fractional digits). Same well-known
     * keys as usageMap.
     */
    providedCostDetails?: { [key: string]: number };
    /**
     * Usage counts (non-negative integers). Well-known keys: input, output, total, cache_read,
     * cache_write, reasoning, audio, image (06-usage-cost.md §3). Provider-reported semantics;
     * not comparable across providers.
     */
    providedUsageDetails?: { [key: string]: number };
    /**
     * Generation field: model provider (openai, anthropic, bedrock, …); promoted (F7).
     */
    provider?: null | string;
    /**
     * Original pre-normalization source type (02-span.md §2.1).
     */
    rawKind?:   null | string;
    release?:   null | string;
    sessionID?: null | string;
    /**
     * RFC 3339 UTC instant, millisecond precision or finer.
     */
    startTime: Date;
    /**
     * OTel-aligned status (02-span.md §4.1).
     */
    status:     LLMObsCanonicalSpanV1Alpha1Status;
    totalCost?: number | null;
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    traceID: string;
    /**
     * Usage counts (non-negative integers). Well-known keys: input, output, total, cache_read,
     * cache_write, reasoning, audio, image (06-usage-cost.md §3). Provider-reported semantics;
     * not comparable across providers.
     */
    usageDetails?: { [key: string]: number };
    userID?:       null | string;
    version?:      null | string;
}

export enum CostSource {
    Derived = "derived",
    Provided = "provided",
}

/**
 * A generic (name, timestamp, attributes) record attached to a span (02-span.md §4.4). Not
 * an entity; union-merged on update.
 */
export interface Event {
    /**
     * Open key/value map. Holds every non-promoted attribute; raw attributes always preserved.
     * Keys under 'llmobs.*' are kernel-owned (02-span.md §6).
     */
    attributes?: { [key: string]: any };
    name:        string;
    /**
     * RFC 3339 UTC instant, millisecond precision or finer.
     */
    timestamp: Date;
}

/**
 * Closed canonical enum (02-span.md §2).
 */
export enum Kind {
    AgentStep = "agent_step",
    Embedding = "embedding",
    Generation = "generation",
    Guardrail = "guardrail",
    Retrieval = "retrieval",
    Span = "span",
    ToolCall = "tool_call",
}

/**
 * A (type, id) soft pointer with an optional label snapshot (07-references.md). No FK; MAY
 * dangle.
 */
export interface PricingSnapshotRef {
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    id: string;
    /**
     * Optional human-readable snapshot captured at write time (07-references.md §2).
     */
    label?: string;
    /**
     * Referent kind, e.g. score_config, prompt, price, or a plugin namespaced type
     * (evals/dataset_run_item).
     */
    type: string;
}

/**
 * A (type, id) soft pointer with an optional label snapshot (07-references.md). No FK; MAY
 * dangle.
 */
export interface PromptRef {
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    id: string;
    /**
     * Optional human-readable snapshot captured at write time (07-references.md §2).
     */
    label?: string;
    /**
     * Referent kind, e.g. score_config, prompt, price, or a plugin namespaced type
     * (evals/dataset_run_item).
     */
    type: string;
}

/**
 * OTel-aligned status (02-span.md §4.1).
 */
export interface LLMObsCanonicalSpanV1Alpha1Status {
    code:     Code;
    message?: string;
}

export enum Code {
    Error = "error",
    Ok = "ok",
    Unset = "unset",
}

export interface LLMObsCanonicalTraceV1Alpha1 {
    /**
     * Open key/value map. Holds every non-promoted attribute; raw attributes always preserved.
     * Keys under 'llmobs.*' are kernel-owned (02-span.md §6).
     */
    attributes: { [key: string]: any };
    /**
     * MAY be derived as max(span.end_time) by an adapter (03-trace.md §3).
     */
    endTime?: Date | null;
    /**
     * Sanitized low-cardinality dimension (08-data-quality.md §2). Frozen.
     */
    environment: string;
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    id: string;
    /**
     * Opaque trace-level input.
     */
    input?: any;
    name?:  null | string;
    /**
     * Opaque trace-level output.
     */
    output?: any;
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    projectID:  string;
    release?:   null | string;
    sessionID?: null | string;
    /**
     * RFC 3339 UTC instant, millisecond precision or finer.
     */
    startTime: Date;
    /**
     * OTel-aligned status (02-span.md §4.1).
     */
    status?: LLMObsCanonicalTraceV1Alpha1Status;
    /**
     * Set semantics; union-merged on update (03-trace.md §2).
     */
    tags?: string[];
    /**
     * Derived trace-level cost: SUM of NON-aggregate spans' total_cost (06-usage-cost.md §7.1;
     * agent_step/tool_call excluded to avoid double-counting). Query-time derived like
     * end_time; null when the trace has no leaf cost.
     */
    totalCost?: number | null;
    userID?:    null | string;
    version?:   null | string;
}

/**
 * OTel-aligned status (02-span.md §4.1).
 */
export interface LLMObsCanonicalTraceV1Alpha1Status {
    code:     Code;
    message?: string;
}

export interface LLMObsCanonicalScoreV1Alpha1 {
    comment?:   null | string;
    configRef?: ConfigRef | null;
    dataType:   DataType;
    /**
     * Sanitized low-cardinality dimension (08-data-quality.md §2). Frozen.
     */
    environment: string;
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    id: string;
    /**
     * Open key/value map. Holds every non-promoted attribute; raw attributes always preserved.
     * Keys under 'llmobs.*' are kernel-owned (02-span.md §6).
     */
    metadata: { [key: string]: any };
    name:     string;
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    projectID: string;
    source:    Source;
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    subjectID:   string;
    subjectType: string;
    /**
     * RFC 3339 UTC instant, millisecond precision or finer.
     */
    timestamp:     Date;
    valueNumeric?: number | null;
    valueString?:  null | string;
}

/**
 * A (type, id) soft pointer with an optional label snapshot (07-references.md). No FK; MAY
 * dangle.
 */
export interface ConfigRef {
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    id: string;
    /**
     * Optional human-readable snapshot captured at write time (07-references.md §2).
     */
    label?: string;
    /**
     * Referent kind, e.g. score_config, prompt, price, or a plugin namespaced type
     * (evals/dataset_run_item).
     */
    type: string;
}

export enum DataType {
    Boolean = "boolean",
    Categorical = "categorical",
    Numeric = "numeric",
}

export enum Source {
    Code = "code",
    External = "external",
    Human = "human",
    LlmJudge = "llm_judge",
}

export interface LLMObsCanonicalScoreConfigV1Alpha1 {
    categories?:  Category[] | null;
    dataType:     DataType;
    description?: null | string;
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    id:         string;
    isArchived: boolean;
    maxValue?:  number | null;
    minValue?:  number | null;
    name:       string;
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    projectID: string;
}

export interface Category {
    label: string;
    value: number;
}

export interface LLMObsCanonicalMediaReferenceV1Alpha1 {
    contentType?: null | string;
    /**
     * RFC 3339 UTC instant, millisecond precision or finer.
     */
    createdAt: Date;
    /**
     * Stable identity string, unique within (project_id, entity).
     */
    projectID: string;
    /**
     * Lowercase hex SHA-256 of the content; the dedup key within a project.
     */
    sha256:     string;
    sizeBytes?: number | null;
}

export interface LLMObsQueryDSLDocumentV1Alpha1 {
    aggregations?: Aggregation[];
    cursor?:       string;
    filters?:      Filter[];
    groupBy?:      Array<GroupByClass | string>;
    limit?:        number;
    orderBy?:      OrderBy[];
    scores?:       Score[];
    target:        Target;
    timeRange:     TimeRange;
    version:       Version;
}

export interface Aggregation {
    alias?: string;
    field?: string;
    key?:   string;
    op:     AggregationOp;
}

export enum AggregationOp {
    Avg = "avg",
    Count = "count",
    CountDistinct = "count_distinct",
    Max = "max",
    Min = "min",
    P50 = "p50",
    P90 = "p90",
    P95 = "p95",
    P99 = "p99",
    Sum = "sum",
}

export interface Filter {
    field?: string;
    op?:    AnyOp;
    value?: any;
    key?:   string;
    any?:   Any[];
}

export interface Any {
    field:  string;
    op:     AnyOp;
    value?: any;
    key?:   string;
}

export enum AnyOp {
    Contains = "contains",
    ContainsAll = "contains_all",
    ContainsAny = "contains_any",
    Eq = "eq",
    Exists = "exists",
    Gt = "gt",
    Gte = "gte",
    In = "in",
    IsNull = "is_null",
    LTE = "lte",
    Lt = "lt",
    Neq = "neq",
    NotIn = "not_in",
    RefEq = "ref_eq",
    StartsWith = "starts_with",
}

export interface GroupByClass {
    field:    Field;
    interval: Interval;
}

export enum Field {
    StartTime = "start_time",
    Timestamp = "timestamp",
}

export enum Interval {
    The1D = "1d",
    The1H = "1h",
    The1M = "1m",
    The5M = "5m",
}

export interface OrderBy {
    dir:   Dir;
    field: string;
}

export enum Dir {
    Asc = "asc",
    Desc = "desc",
}

export interface Score {
    dataType: DataType;
    name:     string;
    op:       ScoreOp;
    source?:  Source;
    value:    any;
}

export enum ScoreOp {
    Eq = "eq",
    Gt = "gt",
    Gte = "gte",
    In = "in",
    LTE = "lte",
    Lt = "lt",
    Neq = "neq",
}

export enum Target {
    Scores = "scores",
    Spans = "spans",
    Traces = "traces",
}

export interface TimeRange {
    from: Date;
    to:   Date;
}

export enum Version {
    V1Alpha1 = "v1alpha1",
}
