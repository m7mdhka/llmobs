// This file was generated from JSON Schema using quicktype, do not modify it directly.
// To parse and unparse this JSON data, add this code to your project and do:
//
//    lLMObsCanonicalSpanV1Alpha1, err := UnmarshalLLMObsCanonicalSpanV1Alpha1(bytes)
//    bytes, err = lLMObsCanonicalSpanV1Alpha1.Marshal()
//
//    lLMObsCanonicalTraceV1Alpha1, err := UnmarshalLLMObsCanonicalTraceV1Alpha1(bytes)
//    bytes, err = lLMObsCanonicalTraceV1Alpha1.Marshal()
//
//    lLMObsCanonicalScoreV1Alpha1, err := UnmarshalLLMObsCanonicalScoreV1Alpha1(bytes)
//    bytes, err = lLMObsCanonicalScoreV1Alpha1.Marshal()
//
//    lLMObsCanonicalScoreConfigV1Alpha1, err := UnmarshalLLMObsCanonicalScoreConfigV1Alpha1(bytes)
//    bytes, err = lLMObsCanonicalScoreConfigV1Alpha1.Marshal()
//
//    lLMObsCanonicalMediaReferenceV1Alpha1, err := UnmarshalLLMObsCanonicalMediaReferenceV1Alpha1(bytes)
//    bytes, err = lLMObsCanonicalMediaReferenceV1Alpha1.Marshal()

package model

import "time"

import "encoding/json"

func UnmarshalLLMObsCanonicalSpanV1Alpha1(data []byte) (LLMObsCanonicalSpanV1Alpha1, error) {
	var r LLMObsCanonicalSpanV1Alpha1
	err := json.Unmarshal(data, &r)
	return r, err
}

func (r *LLMObsCanonicalSpanV1Alpha1) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

func UnmarshalLLMObsCanonicalTraceV1Alpha1(data []byte) (LLMObsCanonicalTraceV1Alpha1, error) {
	var r LLMObsCanonicalTraceV1Alpha1
	err := json.Unmarshal(data, &r)
	return r, err
}

func (r *LLMObsCanonicalTraceV1Alpha1) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

func UnmarshalLLMObsCanonicalScoreV1Alpha1(data []byte) (LLMObsCanonicalScoreV1Alpha1, error) {
	var r LLMObsCanonicalScoreV1Alpha1
	err := json.Unmarshal(data, &r)
	return r, err
}

func (r *LLMObsCanonicalScoreV1Alpha1) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

func UnmarshalLLMObsCanonicalScoreConfigV1Alpha1(data []byte) (LLMObsCanonicalScoreConfigV1Alpha1, error) {
	var r LLMObsCanonicalScoreConfigV1Alpha1
	err := json.Unmarshal(data, &r)
	return r, err
}

func (r *LLMObsCanonicalScoreConfigV1Alpha1) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

func UnmarshalLLMObsCanonicalMediaReferenceV1Alpha1(data []byte) (LLMObsCanonicalMediaReferenceV1Alpha1, error) {
	var r LLMObsCanonicalMediaReferenceV1Alpha1
	err := json.Unmarshal(data, &r)
	return r, err
}

func (r *LLMObsCanonicalMediaReferenceV1Alpha1) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

type LLMObsCanonicalSpanV1Alpha1 struct {
	Attributes                                                                                  map[string]interface{}   `json:"attributes"`
	// Generation field: time to first token.                                                                            
	CompletionStartTime                                                                         *time.Time               `json:"completion_start_time"`
	CostDetails                                                                                 map[string]float64       `json:"cost_details,omitempty"`
	CostSource                                                                                  *CostSource              `json:"cost_source"`
	// Absent/null ⇒ open or point_event (02-span.md §3).                                                                
	EndTime                                                                                     *time.Time               `json:"end_time"`
	Environment                                                                                 string                   `json:"environment"`
	// Span events (02-span.md §4.4); union-merged; present on all shapes; not promoted.                                 
	Events                                                                                      []EventElement           `json:"events,omitempty"`
	ID                                                                                          string                   `json:"id"`
	// Opaque payload; MAY contain media tokens (02-span.md §4.3, §7).                                                   
	Input                                                                                       interface{}              `json:"input"`
	// MIME-style rendering hint for input; NOT queryable/promoted (02-span.md §4.3, F6).                                
	InputContentType                                                                            *string                  `json:"input_content_type"`
	// Closed canonical enum (02-span.md §2).                                                                            
	Kind                                                                                        Kind                     `json:"kind"`
	// Generation field: the resolved/served model (e.g. gen_ai.response.model); requested model                         
	// preserved under llmobs.raw.model_requested (02-span.md §5, F7).                                                   
	Model                                                                                       *string                  `json:"model"`
	// Generation field: request parameters as verbatim strings, keyed by name; not parsed at                            
	// normalize time (F2).                                                                                              
	ModelParameters                                                                             map[string]string        `json:"model_parameters"`
	Name                                                                                        string                   `json:"name"`
	// Opaque payload.                                                                                                   
	Output                                                                                      interface{}              `json:"output"`
	// Rendering hint for output; not promoted.                                                                          
	OutputContentType                                                                           *string                  `json:"output_content_type"`
	// In-trace parent; null for a trace-root span.                                                                      
	ParentSpanID                                                                                *string                  `json:"parent_span_id"`
	PricingSnapshotRef                                                                          *PricingSnapshotRefClass `json:"pricing_snapshot_ref"`
	ProjectID                                                                                   string                   `json:"project_id"`
	// Optional prompt linkage as a reference, not a promoted column (02-span.md §5.4).                                  
	PromptRef                                                                                   *PricingSnapshotRefClass `json:"prompt_ref"`
	ProvidedCostDetails                                                                         map[string]float64       `json:"provided_cost_details,omitempty"`
	ProvidedUsageDetails                                                                        map[string]int64         `json:"provided_usage_details,omitempty"`
	// Generation field: model provider (openai, anthropic, bedrock, …); promoted (F7).                                  
	Provider                                                                                    *string                  `json:"provider"`
	// Original pre-normalization source type (02-span.md §2.1).                                                         
	RawKind                                                                                     *string                  `json:"raw_kind"`
	Release                                                                                     *string                  `json:"release"`
	SessionID                                                                                   *string                  `json:"session_id"`
	StartTime                                                                                   time.Time                `json:"start_time"`
	Status                                                                                      Status                   `json:"status"`
	TotalCost                                                                                   *float64                 `json:"total_cost"`
	TraceID                                                                                     string                   `json:"trace_id"`
	UsageDetails                                                                                map[string]int64         `json:"usage_details,omitempty"`
	UserID                                                                                      *string                  `json:"user_id"`
	Version                                                                                     *string                  `json:"version"`
}

// A generic (name, timestamp, attributes) record attached to a span (02-span.md §4.4). Not
// an entity; union-merged on update.
type EventElement struct {
	Attributes map[string]interface{} `json:"attributes,omitempty"`
	Name       string                 `json:"name"`
	Timestamp  time.Time              `json:"timestamp"`
}

// A (type, id) soft pointer with an optional label snapshot (07-references.md). No FK; MAY
// dangle.
type PricingSnapshotRefClass struct {
	ID                                                                               string  `json:"id"`
	// Optional human-readable snapshot captured at write time (07-references.md §2).        
	Label                                                                            *string `json:"label,omitempty"`
	// Referent kind, e.g. score_config, prompt, price, or a plugin namespaced type          
	// (evals/dataset_run_item).                                                             
	Type                                                                             string  `json:"type"`
}

// OTel-aligned status (02-span.md §4.1).
type Status struct {
	Code    CodeEnum `json:"code"`
	Message *string  `json:"message,omitempty"`
}

type LLMObsCanonicalTraceV1Alpha1 struct {
	Attributes                                                             map[string]interface{} `json:"attributes"`
	// MAY be derived as max(span.end_time) by an adapter (03-trace.md §3).                       
	EndTime                                                                *time.Time             `json:"end_time"`
	Environment                                                            string                 `json:"environment"`
	ID                                                                     string                 `json:"id"`
	// Opaque trace-level input.                                                                  
	Input                                                                  interface{}            `json:"input"`
	Name                                                                   *string                `json:"name"`
	// Opaque trace-level output.                                                                 
	Output                                                                 interface{}            `json:"output"`
	ProjectID                                                              string                 `json:"project_id"`
	Release                                                                *string                `json:"release"`
	SessionID                                                              *string                `json:"session_id"`
	StartTime                                                              time.Time              `json:"start_time"`
	Status                                                                 *Status                `json:"status,omitempty"`
	// Set semantics; union-merged on update (03-trace.md §2).                                    
	Tags                                                                   []string               `json:"tags,omitempty"`
	UserID                                                                 *string                `json:"user_id"`
	Version                                                                *string                `json:"version"`
}

type LLMObsCanonicalScoreV1Alpha1 struct {
	Comment      *string                  `json:"comment"`
	ConfigRef    *PricingSnapshotRefClass `json:"config_ref"`
	DataType     DataType                 `json:"data_type"`
	Environment  string                   `json:"environment"`
	ID           string                   `json:"id"`
	Metadata     map[string]interface{}   `json:"metadata"`
	Name         string                   `json:"name"`
	ProjectID    string                   `json:"project_id"`
	Source       Source                   `json:"source"`
	SubjectID    string                   `json:"subject_id"`
	SubjectType  string                   `json:"subject_type"`
	Timestamp    time.Time                `json:"timestamp"`
	ValueNumeric *float64                 `json:"value_numeric"`
	ValueString  *string                  `json:"value_string"`
}

type LLMObsCanonicalScoreConfigV1Alpha1 struct {
	Categories  []Category `json:"categories"`
	DataType    DataType   `json:"data_type"`
	Description *string    `json:"description"`
	ID          string     `json:"id"`
	IsArchived  bool       `json:"is_archived"`
	MaxValue    *float64   `json:"max_value"`
	MinValue    *float64   `json:"min_value"`
	Name        string     `json:"name"`
	ProjectID   string     `json:"project_id"`
}

type Category struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

type LLMObsCanonicalMediaReferenceV1Alpha1 struct {
	ContentType                                                             *string   `json:"content_type"`
	CreatedAt                                                               time.Time `json:"created_at"`
	ProjectID                                                               string    `json:"project_id"`
	// Lowercase hex SHA-256 of the content; the dedup key within a project.          
	Sha256                                                                  string    `json:"sha256"`
	SizeBytes                                                               *int64    `json:"size_bytes"`
}

type CostSource string

const (
	Derived  CostSource = "derived"
	Provided CostSource = "provided"
)

// Closed canonical enum (02-span.md §2).
type Kind string

const (
	AgentStep  Kind = "agent_step"
	Embedding  Kind = "embedding"
	Generation Kind = "generation"
	Guardrail  Kind = "guardrail"
	Retrieval  Kind = "retrieval"
	Span       Kind = "span"
	ToolCall   Kind = "tool_call"
)

type CodeEnum string

const (
	Error CodeEnum = "error"
	Ok    CodeEnum = "ok"
	Unset CodeEnum = "unset"
)

type DataType string

const (
	Boolean     DataType = "boolean"
	Categorical DataType = "categorical"
	Numeric     DataType = "numeric"
)

type Source string

const (
	Code     Source = "code"
	External Source = "external"
	Human    Source = "human"
	LlmJudge Source = "llm_judge"
)
