// Command otel-genai-demo emits one agent-shaped trace (nested agent step, an LLM
// generation with tool calls + usage, a tool execution, and a retrieval) using the
// official OpenTelemetry Go SDK with GenAI semantic-convention attributes, pointed
// at a local LLMObs kernel's OTLP/HTTP receiver. It prints the trace id.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	ctx := context.Background()
	endpoint := env("LLMOBS_OTLP_ENDPOINT", "localhost:4318")
	apiKey := env("LLMOBS_API_KEY", "sk-e2e-demo-key")

	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
		otlptracehttp.WithHeaders(map[string]string{"Authorization": "Bearer " + apiKey}),
	)
	must(err)

	res, err := resource.New(ctx, resource.WithAttributes(
		attribute.String("service.name", "support-svc"),
		attribute.String("service.version", "2.3.1"),
		attribute.String("deployment.environment.name", "Production"),
	))
	must(err)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	tr := tp.Tracer("otel-genai-demo")

	// root: agent step
	ctx, agent := tr.Start(ctx, "invoke_agent support-agent",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.agent.name", "support-agent"),
			attribute.String("gen_ai.conversation.id", "conv-789"),
			attribute.String("user.id", "user-42"),
		))
	traceID := agent.SpanContext().TraceID().String()

	// child: LLM generation with tool calls + usage
	genCtx, gen := tr.Start(ctx, "chat gpt-4o",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "chat"),
			attribute.String("gen_ai.provider.name", "openai"),
			attribute.String("gen_ai.request.model", "gpt-4o"),
			attribute.String("gen_ai.response.model", "gpt-4o-2024-08-06"),
			attribute.Float64("gen_ai.request.temperature", 0.2),
			attribute.Int("gen_ai.request.max_tokens", 256),
			attribute.Int("gen_ai.usage.input_tokens", 812),
			attribute.Int("gen_ai.usage.output_tokens", 96),
			attribute.String("gen_ai.input.messages", `[{"role":"user","content":"refund order 55, email me at jane@example.com, card 4111 1111 1111 1111"}]`),
			attribute.String("gen_ai.output.messages", `[{"role":"assistant","tool_calls":[{"id":"call_1","name":"get_order"}]}]`),
		))
	gen.AddEvent("gen_ai.choice", trace.WithAttributes(attribute.String("finish_reason", "tool_calls")))
	gen.End()

	// child: tool execution
	_, tool := tr.Start(genCtx, "execute_tool get_order",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "execute_tool"),
			attribute.String("gen_ai.tool.name", "get_order"),
			attribute.String("gen_ai.tool.call.id", "call_1"),
		))
	tool.End()

	// child: retrieval (no gen_ai op — maps to kind=span by design)
	_, ret := tr.Start(ctx, "vector_search refund_policies",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "qdrant"),
			attribute.Int("db.vector.query.top_k", 5),
		))
	ret.End()

	agent.End()

	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	must(tp.ForceFlush(shCtx))
	must(tp.Shutdown(shCtx))

	fmt.Println(traceID)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
