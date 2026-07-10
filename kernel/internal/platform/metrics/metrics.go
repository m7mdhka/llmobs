// Package metrics is a minimal, dependency-free Prometheus/OpenMetrics registry.
// Stdlib-only (go-style: reach for a dep only when stdlib can't do it, and never
// on the hot path without an ADR). It supports labelled counters, gauges, and
// fixed-bucket histograms, plus a sampled-gauge callback for scrape-time values
// (DB pool stats). Label discipline is the caller's job: project_id yes;
// unbounded labels (trace/user ids) never — this package will happily explode if
// you feed it high cardinality, so don't.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Registry holds all instruments and renders the exposition.
type Registry struct {
	mu         sync.Mutex
	counters   map[string]*counterVec
	gauges     map[string]*gaugeVec
	histograms map[string]*histogramVec
	sampled    []sampledGauge
	help       map[string]string
}

type sampledGauge struct {
	name, help string
	fn         func() float64
}

func New() *Registry {
	return &Registry{
		counters:   map[string]*counterVec{},
		gauges:     map[string]*gaugeVec{},
		histograms: map[string]*histogramVec{},
		help:       map[string]string{},
	}
}

// labelKey renders a stable key for a label set (sorted).
func labelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(escape(labels[k]))
	}
	return b.String()
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// CounterAdd increments a counter series by v (>=0).
func (r *Registry) CounterAdd(name, help string, labels map[string]string, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.counters[name]
	if c == nil {
		c = &counterVec{series: map[string]labelledValue{}}
		r.counters[name] = c
		r.help[name] = help
	}
	k := labelKey(labels)
	s := c.series[k]
	s.labels = labels
	s.value += v
	c.series[k] = s
}

// GaugeSet sets a gauge series.
func (r *Registry) GaugeSet(name, help string, labels map[string]string, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.gauges[name]
	if g == nil {
		g = &gaugeVec{series: map[string]labelledValue{}}
		r.gauges[name] = g
		r.help[name] = help
	}
	g.series[labelKey(labels)] = labelledValue{labels: labels, value: v}
}

// SampledGauge registers a gauge whose value is read at scrape time.
func (r *Registry) SampledGauge(name, help string, fn func() float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sampled = append(r.sampled, sampledGauge{name: name, help: help, fn: fn})
}

// Observe records a value into a histogram series.
func (r *Registry) Observe(name, help string, labels map[string]string, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.histograms[name]
	if h == nil {
		h = &histogramVec{series: map[string]*histSeries{}}
		r.histograms[name] = h
		r.help[name] = help
	}
	k := labelKey(labels)
	s := h.series[k]
	if s == nil {
		s = &histSeries{labels: labels, counts: make([]uint64, len(defaultBuckets))}
		h.series[k] = s
	}
	s.sum += v
	s.count++
	for i, b := range defaultBuckets {
		if v <= b {
			s.counts[i]++
		}
	}
}

// defaultBuckets are seconds-oriented latency buckets.
var defaultBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type labelledValue struct {
	labels map[string]string
	value  float64
}

type counterVec struct{ series map[string]labelledValue }
type gaugeVec struct{ series map[string]labelledValue }
type histogramVec struct{ series map[string]*histSeries }

type histSeries struct {
	labels map[string]string
	counts []uint64
	sum    float64
	count  uint64
}

// Handler renders the exposition (Prometheus text format 0.0.4 / OpenMetrics-ish).
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var b strings.Builder

		names := make([]string, 0, len(r.counters))
		for n := range r.counters {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			writeHelpType(&b, n, r.help[n], "counter")
			writeSeries(&b, n, r.counters[n].series)
		}

		names = names[:0]
		for n := range r.gauges {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			writeHelpType(&b, n, r.help[n], "gauge")
			writeSeries(&b, n, r.gauges[n].series)
		}

		for _, sg := range r.sampled {
			writeHelpType(&b, sg.name, sg.help, "gauge")
			fmt.Fprintf(&b, "%s %s\n", sg.name, formatFloat(sg.fn()))
		}

		names = names[:0]
		for n := range r.histograms {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			writeHelpType(&b, n, r.help[n], "histogram")
			for _, s := range r.histograms[n].series {
				for i, bound := range defaultBuckets {
					lbls := withLabel(s.labels, "le", formatFloat(bound))
					fmt.Fprintf(&b, "%s_bucket{%s} %d\n", n, renderLabels(lbls), s.counts[i])
				}
				lbls := withLabel(s.labels, "le", "+Inf")
				fmt.Fprintf(&b, "%s_bucket{%s} %d\n", n, renderLabels(lbls), s.count)
				fmt.Fprintf(&b, "%s_sum{%s} %s\n", n, renderLabels(s.labels), formatFloat(s.sum))
				fmt.Fprintf(&b, "%s_count{%s} %d\n", n, renderLabels(s.labels), s.count)
			}
		}
		_, _ = w.Write([]byte(b.String()))
	})
}

func writeHelpType(b *strings.Builder, name, help, typ string) {
	if help != "" {
		fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	}
	fmt.Fprintf(b, "# TYPE %s %s\n", name, typ)
}

func writeSeries(b *strings.Builder, name string, series map[string]labelledValue) {
	for _, s := range series {
		if len(s.labels) == 0 {
			fmt.Fprintf(b, "%s %s\n", name, formatFloat(s.value))
			continue
		}
		fmt.Fprintf(b, "%s{%s} %s\n", name, renderLabels(s.labels), formatFloat(s.value))
	}
}

func renderLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=\"%s\"", k, escape(labels[k]))
	}
	return strings.Join(parts, ",")
}

func withLabel(labels map[string]string, k, v string) map[string]string {
	out := make(map[string]string, len(labels)+1)
	for lk, lv := range labels {
		out[lk] = lv
	}
	out[k] = v
	return out
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}
