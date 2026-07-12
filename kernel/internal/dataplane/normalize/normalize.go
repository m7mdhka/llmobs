// Package normalize maps ingestion dialects onto the canonical model. Normalizers
// are pure per-span functions: attributes in, canonical span
// out, no I/O. New dialects drop in as files implementing Normalizer and register
// in the registry; first match wins.
package normalize

// SpanInput is the transport-neutral view of one span a normalizer maps. The OTLP
// receiver (and the conformance test) build these from pdata; a normalizer never
// touches the wire format directly.
type SpanInput struct {
	TraceID       string // lowercase hex
	SpanID        string // lowercase hex
	ParentSpanID  string // lowercase hex, "" if root
	Name          string
	StartUnixNano uint64
	EndUnixNano   uint64 // 0 if unset
	StatusCode    int    // OTLP: 0 UNSET, 1 OK, 2 ERROR
	StatusMessage string
	Attributes    map[string]any
	Resource      map[string]any
	ScopeName     string
	Events        []SpanEventInput
}

// SpanEventInput is one OTLP span event (a log-record-on-a-span).
type SpanEventInput struct {
	Name         string
	TimeUnixNano uint64
	Attributes   map[string]any
}

// Context carries per-ingestion state the wire cannot supply.
type Context struct {
	ProjectID string
}

// Normalizer converts a dialect's span into a canonical span map.
type Normalizer interface {
	// Name identifies the dialect (also the fixture directory).
	Name() string
	// Detect reports whether this normalizer handles the span.
	Detect(SpanInput) bool
	// Map produces the canonical span (the ingest event payload).
	Map(SpanInput, Context) map[string]any
}

// Registry is an ordered set of normalizers; first Detect match wins, with a
// default fallback.
type Registry struct {
	normalizers []Normalizer
	fallback    Normalizer
}

func NewRegistry(fallback Normalizer, ns ...Normalizer) *Registry {
	return &Registry{normalizers: ns, fallback: fallback}
}

// Normalize picks the first matching normalizer (else the fallback), maps, and
// runs the shared post-map passes every transport must share:
// attribute-key sanitization.
func (r *Registry) Normalize(in SpanInput, ctx Context) map[string]any {
	var out map[string]any
	for _, n := range r.normalizers {
		if n.Detect(in) {
			out = n.Map(in, ctx)
			break
		}
	}
	if out == nil {
		out = r.fallback.Map(in, ctx)
	}
	if attrs, ok := out["attributes"].(map[string]any); ok {
		SanitizeAttributeKeys(attrs)
	}
	// Strip NUL from every string value (attributes, nested, and promoted fields) so a span
	// with a NUL never fails the lite Postgres INSERT while landing on scale — one shared stage,
	// both profiles identical.
	SanitizeNullBytes(out)
	return out
}

// Default returns a registry with the OTel GenAI semconv normalizer as both the
// matcher and the fallback (v1alpha1 has one dialect).
func Default() *Registry {
	sc := &SemConv{}
	return NewRegistry(sc, sc)
}
