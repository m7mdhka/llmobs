package normalize

import (
	"fmt"
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
// is preserved as the promoted field).
var consumedKeys = map[string]bool{
	"gen_ai.operation.name": true, "gen_ai.provider.name": true, "gen_ai.system": true,
	"gen_ai.request.model": true, "gen_ai.response.model": true,
	"gen_ai.input.messages": true, "gen_ai.output.messages": true,
	"gen_ai.usage.input_tokens": true, "gen_ai.usage.output_tokens": true,
	"deployment.environment": true, "deployment.environment.name": true,
	"service.version": true, "session.id": true, "gen_ai.conversation.id": true, "user.id": true,
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

	// opaque input/output
	if v := getStr(in.Attributes, "gen_ai.input.messages"); v != "" {
		out["input"] = v
	}
	if v := getStr(in.Attributes, "gen_ai.output.messages"); v != "" {
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
		if usage := providedUsage(in.Attributes); len(usage) > 0 {
			out["provided_usage_details"] = usage
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

func providedUsage(a map[string]any) map[string]any {
	out := map[string]any{}
	if v, ok := toInt(a["gen_ai.usage.input_tokens"]); ok {
		out["input"] = v
	}
	if v, ok := toInt(a["gen_ai.usage.output_tokens"]); ok {
		out["output"] = v
	}
	if in, iok := out["input"].(int64); iok {
		if o, ook := out["output"].(int64); ook {
			out["total"] = in + o
		}
	}
	return out
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
