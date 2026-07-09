export interface LLMObsCanonicalSpanV1Alpha1 {
    attributes: { [key: string]: any };
    /**
     * Generation field: time to first token.
     */
    completionStartTime?: Date | null;
    costDetails?:         { [key: string]: number };
    costSource?:          CostSource | null;
    /**
     * Absent/null ⇒ open or point_event (02-span.md §3).
     */
    endTime?:    Date | null;
    environment: string;
    /**
     * Span events (02-span.md §4.4); union-merged; present on all shapes; not promoted.
     */
    events?: EventElement[];
    id:      string;
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
    pricingSnapshotRef?: PricingSnapshotRefClass | null;
    projectID:           string;
    /**
     * Optional prompt linkage as a reference, not a promoted column (02-span.md §5.4).
     */
    promptRef?:            PricingSnapshotRefClass | null;
    providedCostDetails?:  { [key: string]: number };
    providedUsageDetails?: { [key: string]: number };
    /**
     * Generation field: model provider (openai, anthropic, bedrock, …); promoted (F7).
     */
    provider?: null | string;
    /**
     * Original pre-normalization source type (02-span.md §2.1).
     */
    rawKind?:      null | string;
    release?:      null | string;
    sessionID?:    null | string;
    startTime:     Date;
    status:        Status;
    totalCost?:    number | null;
    traceID:       string;
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
export interface EventElement {
    attributes?: { [key: string]: any };
    name:        string;
    timestamp:   Date;
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
export interface PricingSnapshotRefClass {
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
export interface Status {
    code:     Code;
    message?: string;
}

export enum Code {
    Error = "error",
    Ok = "ok",
    Unset = "unset",
}

export interface LLMObsCanonicalTraceV1Alpha1 {
    attributes: { [key: string]: any };
    /**
     * MAY be derived as max(span.end_time) by an adapter (03-trace.md §3).
     */
    endTime?:    Date | null;
    environment: string;
    id:          string;
    /**
     * Opaque trace-level input.
     */
    input?: any;
    name?:  null | string;
    /**
     * Opaque trace-level output.
     */
    output?:    any;
    projectID:  string;
    release?:   null | string;
    sessionID?: null | string;
    startTime:  Date;
    status?:    Status;
    /**
     * Set semantics; union-merged on update (03-trace.md §2).
     */
    tags?:    string[];
    userID?:  null | string;
    version?: null | string;
}

export interface LLMObsCanonicalScoreV1Alpha1 {
    comment?:      null | string;
    configRef?:    PricingSnapshotRefClass | null;
    dataType:      DataType;
    environment:   string;
    id:            string;
    metadata:      { [key: string]: any };
    name:          string;
    projectID:     string;
    source:        Source;
    subjectID:     string;
    subjectType:   string;
    timestamp:     Date;
    valueNumeric?: number | null;
    valueString?:  null | string;
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
    id:           string;
    isArchived:   boolean;
    maxValue?:    number | null;
    minValue?:    number | null;
    name:         string;
    projectID:    string;
}

export interface Category {
    label: string;
    value: number;
}

export interface LLMObsCanonicalMediaReferenceV1Alpha1 {
    contentType?: null | string;
    createdAt:    Date;
    projectID:    string;
    /**
     * Lowercase hex SHA-256 of the content; the dedup key within a project.
     */
    sha256:     string;
    sizeBytes?: number | null;
}

export interface LLMObsQueryDSLDocumentV1Alpha1 {
    aggregations?: AggregationElement[];
    cursor?:       string;
    filters?:      FilterElement[];
    groupBy?:      Array<GroupByClass | string>;
    limit?:        number;
    orderBy?:      OrderByElement[];
    scores?:       ScoreElement[];
    target:        Target;
    timeRange:     TimeRange;
    version:       Version;
}

export interface AggregationElement {
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

export interface FilterElement {
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

export interface OrderByElement {
    dir:   Dir;
    field: string;
}

export enum Dir {
    Asc = "asc",
    Desc = "desc",
}

export interface ScoreElement {
    dataType?: DataType;
    name:      string;
    op:        ScoreOp;
    source?:   Source;
    value:     any;
}

export enum ScoreOp {
    Eq = "eq",
    Gt = "gt",
    Gte = "gte",
    In = "in",
    LTE = "lte",
    Lt = "lt",
    Neq = "neq",
    NotIn = "not_in",
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
