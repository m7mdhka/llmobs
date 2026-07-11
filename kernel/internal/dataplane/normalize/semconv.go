package normalize

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// SemConv maps official OpenTelemetry GenAI semantic conventions onto the
// canonical model (validation worksheet in api/model/v1alpha1/validation/otel-genai.md).
type SemConv struct{}

func (s *SemConv) Name() string { return "otel-genai" }

// Detect: this is the v1alpha1 dialect and also the fallback, so it always maps.
func (s *SemConv) Detect(SpanInput) bool { return true }

// consumed keys are promoted out of the attributes bag (invariant 6: their value
// is preserved as the promoted field). The usage aliases (usageAliases) are added
// to this set in init() so every mapped token bucket is consumed, not duplicated.
var consumedKeys = map[string]bool{
	"gen_ai.operation.name": true, "gen_ai.provider.name": true, "gen_ai.system": true,
	"gen_ai.request.model": true, "gen_ai.response.model": true,
	"gen_ai.input.messages": true, "gen_ai.output.messages": true,
	"deployment.environment": true, "deployment.environment.name": true,
	"service.version": true, "session.id": true, "gen_ai.conversation.id": true, "user.id": true,
}

// usageAliases maps the de-facto OTel GenAI usage attribute keys onto the
// well-known canonical token buckets (06-usage-cost.md §3.1). First present alias
// per bucket wins. These are PURE ALIASES only (F3) — a rename, never a
// reinterpretation: cache_read/cache_write/reasoning are stored verbatim as
// additive detail keys and are NEVER subtracted from input. Emitters spell the
// cache/reasoning keys several ways (Anthropic-style, OTel-nested, the
// #14902 input_cached alias), so the common spellings are covered here to avoid
// silently dropping the bucket into the raw bag.
var usageAliases = []struct{ attr, bucket string }{
	{"gen_ai.usage.input_tokens", "input"},
	{"gen_ai.usage.output_tokens", "output"},
	{"gen_ai.usage.total_tokens", "total"}, // de-facto extension key (not core semconv)
	{"gen_ai.usage.input_cached_tokens", "cache_read"},
	{"gen_ai.usage.cache_read_input_tokens", "cache_read"},
	{"gen_ai.usage.cache_read.input_tokens", "cache_read"},
	{"gen_ai.usage.cache_creation_input_tokens", "cache_write"},
	{"gen_ai.usage.input_cache_creation", "cache_write"},
	{"gen_ai.usage.cache_creation.input_tokens", "cache_write"},
	{"gen_ai.usage.reasoning_tokens", "reasoning"},
	{"gen_ai.usage.output_reasoning_tokens", "reasoning"},
	// Audio tokens (#79, 06-usage-cost.md §3.1). Multimodal/audio models report audio
	// input/output token counts SEPARATELY from text and price them at a different
	// (often much higher) rate. Mapped to dedicated buckets so cost derivation can price
	// them at the audio rate, not the text rate (a bucket folded into input would bill
	// at the text rate — the Opik #7137/#7253 bug). Pure aliases (F3); the price entry
	// declares audio_input reduces input / audio_output reduces output.
	{"gen_ai.usage.input_audio_tokens", "audio_input"},
	{"gen_ai.usage.input_tokens_details.audio_tokens", "audio_input"},
	{"gen_ai.usage.prompt_tokens_details.audio_tokens", "audio_input"},
	{"gen_ai.usage.output_audio_tokens", "audio_output"},
	{"gen_ai.usage.output_tokens_details.audio_tokens", "audio_output"},
	{"gen_ai.usage.completion_tokens_details.audio_tokens", "audio_output"},
}

func init() {
	for _, al := range usageAliases {
		consumedKeys[al.attr] = true
	}
}

var requestParamKeys = []string{
	"gen_ai.request.temperature", "gen_ai.request.max_tokens", "gen_ai.request.top_p",
	"gen_ai.request.top_k", "gen_ai.request.frequency_penalty", "gen_ai.request.presence_penalty",
	"gen_ai.request.stop_sequences", "gen_ai.request.seed",
}

func (s *SemConv) Map(in SpanInput, ctx Context) map[string]any {
	out := map[string]any{
		"id":         in.SpanID,
		"project_id": ctx.ProjectID,
		"trace_id":   in.TraceID,
		"name":       in.Name,
		"start_time": rfc3339(in.StartUnixNano),
		"status":     mapStatus(in),
	}
	if in.ParentSpanID != "" {
		out["parent_span_id"] = in.ParentSpanID
	}
	if in.EndUnixNano != 0 {
		out["end_time"] = rfc3339(in.EndUnixNano)
	}

	// attributes bag: resource + span attrs, minus promoted keys; raw always preserved.
	attrs := map[string]any{}
	for k, v := range in.Resource {
		if !consumedKeys[k] {
			attrs["resource."+k] = v
		}
	}
	for k, v := range in.Attributes {
		if !consumedKeys[k] && !isRequestParam(k) {
			attrs[k] = v
		}
	}

	// kind + raw_kind
	op := getStr(in.Attributes, "gen_ai.operation.name")
	out["kind"] = mapKind(op)
	if op != "" {
		out["raw_kind"] = op
	}

	// dimensions
	if env, present := firstAttr(in, "deployment.environment.name", "deployment.environment"); present {
		sanitized, changed := SanitizeEnvironment(env)
		out["environment"] = sanitized
		if changed {
			attrs["llmobs.raw.environment"] = env
			attrs["llmobs.dq.dimension_coerced.environment"] = true
		}
	} else {
		out["environment"] = "default"
	}
	if rel := getStr(in.Resource, "service.version"); rel != "" {
		out["release"] = rel
	}
	if sid := firstNonEmpty(getStr(in.Attributes, "session.id"), getStr(in.Attributes, "gen_ai.conversation.id")); sid != "" {
		out["session_id"] = sid
	}
	if uid := firstNonEmpty(getStr(in.Attributes, "user.id"), getStr(in.Resource, "user.id")); uid != "" {
		out["user_id"] = uid
	}

	// opaque input/output. I/O may arrive on span ATTRIBUTES (classic) or, on OTel
	// GenAI semconv v1.37+ emitters, on a span EVENT (e.g. the
	// gen_ai.client.inference.operation.details event) — scan attributes first, then
	// fall back to events so modern emitters don't yield null I/O (#14930). Stored
	// opaque: whatever string the messages carry (incl. the `parts:[…]` shape).
	if v := getStr(in.Attributes, "gen_ai.input.messages"); v != "" {
		out["input"] = v
	} else if v := eventStr(in.Events, "gen_ai.input.messages"); v != "" {
		out["input"] = v
	}
	if v := getStr(in.Attributes, "gen_ai.output.messages"); v != "" {
		out["output"] = v
	} else if v := eventStr(in.Events, "gen_ai.output.messages"); v != "" {
		out["output"] = v
	}

	// generation fields
	reqModel := getStr(in.Attributes, "gen_ai.request.model")
	respModel := getStr(in.Attributes, "gen_ai.response.model")
	model := firstNonEmpty(respModel, reqModel)
	if model != "" {
		out["model"] = model
		if respModel != "" && reqModel != "" && respModel != reqModel {
			attrs["llmobs.raw.model_requested"] = reqModel
		}
		if prov := firstNonEmpty(getStr(in.Attributes, "gen_ai.provider.name"), getStr(in.Attributes, "gen_ai.system")); prov != "" {
			out["provider"] = prov
		}
		if params := requestParams(in.Attributes); len(params) > 0 {
			out["model_parameters"] = params
		}
		// Aggregate spans (invoke_agent/create_agent/execute_tool) frequently carry the
		// SUM of their child model-call usage/cost (the Vercel AI SDK / LangGraph pattern,
		// #81). Extracting it here would double-count against the child leaf that also
		// carries it, inflating every per-trace and per-project total (§7.1, dual-incumbent
		// Langfuse #14808 + Opik #4695). So usage/cost are extracted ONLY for usage-bearing
		// model calls, never aggregate spans — the model/provider are still promoted
		// (informational); with no usage, derivation produces no cost (§7.3/R4), leaving the
		// cost to the child leaf. This is the ingest half of the no-double-count rule (R5);
		// trace-level aggregation is the query-time half (M3).
		if !isAggregateUsageSpan(op) {
			if usage, mismatch := providedUsage(in.Attributes); len(usage) > 0 {
				out["provided_usage_details"] = usage
				if mismatch {
					// Advisory (#14875): provided value buckets sum past the provided total.
					attrs["llmobs.dq.usage_total_mismatch"] = true
				}
			}
			// Provided cost (LM-4 provided-wins): when the client sends cost, preserve
			// it verbatim as provided_cost_details, stamp cost_source=provided, and
			// populate the promoted total_cost so dashboards work unchanged even before
			// kernel derivation exists (#13). This is Dmitri's GPU-seconds path.
			if cost := providedCost(in.Attributes); len(cost) > 0 {
				out["provided_cost_details"] = cost
				out["cost_source"] = "provided"
				if total, ok := cost["total"].(float64); ok {
					out["total_cost"] = total
				}
			}
		}
		// completion_start_time — time to first token (02-span.md §5), when the
		// source provides it. Enables the DSL `ttft` computed field (§4.2).
		if cst, present := firstAttr(in, "gen_ai.response.completion_start_time"); present {
			out["completion_start_time"] = cst
		}
	}

	// span events
	if len(in.Events) > 0 {
		evs := make([]any, 0, len(in.Events))
		for _, e := range in.Events {
			evs = append(evs, map[string]any{
				"name":       e.Name,
				"timestamp":  rfc3339(e.TimeUnixNano),
				"attributes": e.Attributes,
			})
		}
		out["events"] = evs
	}

	out["attributes"] = attrs
	return out
}

// isAggregateUsageSpan reports whether an operation type denotes an aggregate span
// (an agent/tool step) that may carry usage duplicating its child model calls, so its
// usage/cost MUST NOT be extracted (#81, §7.1). Only usage-bearing model-call ops (chat,
// generate_content, …) and unknown/untyped spans have their usage read.
func isAggregateUsageSpan(op string) bool {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "invoke_agent", "create_agent", "execute_tool":
		return true
	default:
		return false
	}
}

func mapKind(op string) string {
	switch op {
	case "chat", "text_completion", "generate_content":
		return "generation"
	case "embeddings":
		return "embedding"
	case "execute_tool":
		return "tool_call"
	case "invoke_agent", "create_agent":
		return "agent_step"
	default:
		return "span"
	}
}

func mapStatus(in SpanInput) map[string]any {
	st := map[string]any{}
	switch in.StatusCode {
	case 2:
		st["code"] = "error"
	case 1:
		st["code"] = "ok"
	default:
		st["code"] = "unset"
	}
	if in.StatusMessage != "" {
		st["message"] = in.StatusMessage
	}
	return st
}

func requestParams(a map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range requestParamKeys {
		if v, ok := a[k]; ok && v != nil {
			name := strings.TrimPrefix(k, "gen_ai.request.")
			out[name] = fmt.Sprint(v) // verbatim string (F2)
		}
	}
	return out
}

// providedUsage maps the provider's token counts verbatim into the well-known
// canonical buckets (F3: no reinterpretation, cache never subtracted from input).
// It returns the map and whether the provided total is inconsistent with the value
// buckets (the advisory #14875 signal — the caller stamps the dq attribute).
func providedUsage(a map[string]any) (usage map[string]any, totalMismatch bool) {
	out := map[string]any{}
	providerTotal := false
	for _, al := range usageAliases {
		if _, exists := out[al.bucket]; exists {
			continue // first alias present per bucket wins
		}
		if v, ok := toInt(a[al.attr]); ok {
			out[al.bucket] = v
			if al.bucket == "total" {
				providerTotal = true
			}
		}
	}
	if len(out) == 0 {
		return out, false
	}
	if !providerTotal {
		// Synthesize total from the two PRIMARY buckets only. cache_read/cache_write
		// (a detail of input) and reasoning (a detail of output) are NOT summed in —
		// adding them would double-count when the provider's input/output already
		// include them (F3: we cannot assume otherwise). Matches read-time reduction (§6).
		if in, iok := out["input"].(int64); iok {
			if o, ook := out["output"].(int64); ook {
				out["total"] = in + o
			}
		}
	} else {
		totalMismatch = usageBucketsExceedTotal(out)
	}
	return out, totalMismatch
}

// usageBucketsExceedTotal reports whether the sum of the non-total value buckets
// exceeds the provided total beyond a tolerance of max(1, total*1%) — the #14875
// double-count-suspect heuristic (e.g. an inclusive `input` reported alongside a
// separate cache bucket, with `total` the smaller real figure). ADVISORY ONLY: the
// values are still stored verbatim; this only raises a dq flag for a consumer to
// weigh, and may false-positive on a provider whose `input` is genuinely inclusive
// of cache (F3 forbids us assuming either way).
func usageBucketsExceedTotal(u map[string]any) bool {
	total, ok := u["total"].(int64)
	if !ok {
		return false
	}
	var sum int64
	for k, v := range u {
		if k == "total" {
			continue
		}
		if n, ok := v.(int64); ok {
			sum += n
		}
	}
	tol := total / 100
	if tol < 1 {
		tol = 1
	}
	return sum > total+tol
}

// eventStr returns the first string value of key found across a span's events.
// GenAI I/O (semconv v1.37+) migrated from span attributes onto a span EVENT
// (e.g. gen_ai.client.inference.operation.details); scanning by attribute key
// rather than event name keeps this robust to the exact event chosen.
func eventStr(events []SpanEventInput, key string) string {
	for _, e := range events {
		if v, ok := e.Attributes[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// providedCost maps client-sent cost into provided_cost_details (LM-4). Amounts are
// decimals; total is summed from input+output when not sent explicitly. Values are
// sanitized to FINITE, NON-NEGATIVE numbers: a client's provided cost short-circuits
// derivation (R1) and is stored verbatim, so a NaN/Inf/negative would land in
// total_cost and poison every SUM(total_cost) aggregate across the project (NaN is
// contagious; Inf can break the Decimal64(12) write). A bad cost value is dropped
// (bad data never corrupts a valid path), not stored.
func providedCost(a map[string]any) map[string]any {
	out := map[string]any{}
	if v, ok := goodCost(a["gen_ai.usage.input_cost"]); ok {
		out["input"] = v
	}
	if v, ok := goodCost(a["gen_ai.usage.output_cost"]); ok {
		out["output"] = v
	}
	if v, ok := goodCost(a["gen_ai.usage.cost"]); ok {
		out["total"] = v
	} else if in, iok := out["input"].(float64); iok {
		if o, ook := out["output"].(float64); ook {
			out["total"] = in + o
		}
	}
	return out
}

// goodCost accepts only a finite, non-negative cost amount.
func goodCost(v any) (float64, bool) {
	f, ok := toFloat(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, false
	}
	return f, true
}

func isRequestParam(k string) bool {
	for _, p := range requestParamKeys {
		if p == k {
			return true
		}
	}
	return false
}

// helpers

func getStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func firstAttr(in SpanInput, keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := in.Attributes[k].(string); ok && v != "" {
			return v, true
		}
		if v, ok := in.Resource[k].(string); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func rfc3339(nanos uint64) string {
	return time.Unix(0, int64(nanos)).UTC().Format(time.RFC3339Nano)
}

func toInt(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	default:
		return 0, false
	}
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	default:
		return 0, false
	}
}
