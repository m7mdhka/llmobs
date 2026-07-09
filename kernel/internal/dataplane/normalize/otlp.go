package normalize

import (
	"encoding/hex"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

// UnmarshalOTLPJSON decodes an OTLP/JSON ExportTraceServiceRequest.
func UnmarshalOTLPJSON(b []byte) (ptrace.Traces, error) {
	req := ptraceotlp.NewExportRequest()
	if err := req.UnmarshalJSON(b); err != nil {
		return ptrace.Traces{}, err
	}
	return req.Traces(), nil
}

// UnmarshalOTLPProto decodes an OTLP/protobuf ExportTraceServiceRequest.
func UnmarshalOTLPProto(b []byte) (ptrace.Traces, error) {
	req := ptraceotlp.NewExportRequest()
	if err := req.UnmarshalProto(b); err != nil {
		return ptrace.Traces{}, err
	}
	return req.Traces(), nil
}

// FromTraces flattens pdata traces into transport-neutral SpanInputs, preserving
// resource and scope context per span. This is the only place that touches pdata;
// normalizers stay pure.
func FromTraces(td ptrace.Traces) []SpanInput {
	var out []SpanInput
	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		rs := rss.At(i)
		resource := rs.Resource().Attributes().AsRaw()
		sss := rs.ScopeSpans()
		for j := 0; j < sss.Len(); j++ {
			ss := sss.At(j)
			scopeName := ss.Scope().Name()
			spans := ss.Spans()
			for k := 0; k < spans.Len(); k++ {
				sp := spans.At(k)
				in := SpanInput{
					TraceID:       hexTraceID(sp.TraceID()),
					SpanID:        hexSpanID(sp.SpanID()),
					Name:          sp.Name(),
					StartUnixNano: uint64(sp.StartTimestamp()),
					EndUnixNano:   uint64(sp.EndTimestamp()),
					StatusCode:    int(sp.Status().Code()),
					StatusMessage: sp.Status().Message(),
					Attributes:    sp.Attributes().AsRaw(),
					Resource:      resource,
					ScopeName:     scopeName,
				}
				if psid := sp.ParentSpanID(); !psid.IsEmpty() {
					in.ParentSpanID = hexSpanID(psid)
				}
				evs := sp.Events()
				for e := 0; e < evs.Len(); e++ {
					ev := evs.At(e)
					in.Events = append(in.Events, SpanEventInput{
						Name:         ev.Name(),
						TimeUnixNano: uint64(ev.Timestamp()),
						Attributes:   ev.Attributes().AsRaw(),
					})
				}
				out = append(out, in)
			}
		}
	}
	return out
}

func hexTraceID(id pcommon.TraceID) string { return hex.EncodeToString(id[:]) }
func hexSpanID(id pcommon.SpanID) string   { return hex.EncodeToString(id[:]) }
