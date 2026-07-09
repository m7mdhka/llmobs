// Package postgres implements the lite-profile storage adapter.
//
// This file is the real implementation of the canonical merge algorithm
// (api/model/v1alpha1/05-update-semantics.md), written from the spec. The lite
// adapter applies it as merge-on-write (read-modify-write UPDATE under a row
// lock); the same fold is the conformance target the ClickHouse adapter must
// also satisfy.
package postgres

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Op is an ingested event operation.
type Op string

const (
	OpUpsert Op = "upsert"
	OpDelete Op = "delete"
)

// Event is one ingested event targeting a single entity (05 §1). Payload is a
// partial field set for upserts.
type Event struct {
	Op      Op
	EventTS time.Time
	EventID string
	Payload map[string]any
}

// frozenFields per entity type (05 §5).
var frozenFields = map[string]map[string]bool{
	"span":  {"id": true, "project_id": true, "trace_id": true, "kind": true, "start_time": true, "environment": true},
	"trace": {"id": true, "project_id": true, "start_time": true, "environment": true},
	"score": {"id": true, "project_id": true, "subject_type": true, "subject_id": true, "timestamp": true, "environment": true},
}

// mapFields are deep-merged per leaf key (05 §2); everything else is a scalar
// field-group replaced latest-wins.
var mapFields = map[string]bool{
	"attributes": true, "metadata": true, "model_parameters": true,
	"usage_details": true, "cost_details": true,
	"provided_usage_details": true, "provided_cost_details": true,
}

// isSet reports whether a value "sets" its field-group (05 §2.1): not-set when
// nil, "", empty array, or a composite whose leaves are all unset (recursively).
func isSet(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		for _, x := range t {
			if isSet(x) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// orderKey compares two events by (event_ts, event_id) lexicographically (05 §2).
type orderKey struct {
	ts  time.Time
	eid string
}

func (a orderKey) less(b orderKey) bool {
	if !a.ts.Equal(b.ts) {
		return a.ts.Before(b.ts)
	}
	return a.eid < b.eid
}

type group struct {
	val any
	key orderKey
}

// Fold computes the observable entity state from a set of events, per 05 §2–§5.
// entityType selects the frozen-field set ("span"|"trace"|"score"). The fold is
// order-independent and idempotent.
func Fold(entityType string, events []Event) map[string]any {
	frozen := frozenFields[entityType]

	scalars := map[string]group{}             // dotted leaf path -> winning (value, key)
	tags := map[string]bool{}                 // set union
	spanEvents := map[string]map[string]any{} // dedup id -> event
	frozenFirst := map[string]group{}
	raw := map[string]any{}    // llmobs.raw.<field>
	dq := map[string]float64{} // frozen_field_conflict.<field> counts
	var isDeleted *group

	// Process ascending by (event_ts, event_id): earliest sets a frozen field
	// first; later events overwrite non-frozen groups.
	ordered := append([]Event(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return orderKey{ordered[i].EventTS, ordered[i].EventID}.less(orderKey{ordered[j].EventTS, ordered[j].EventID})
	})

	for _, ev := range ordered {
		k := orderKey{ev.EventTS, ev.EventID}
		delVal := ev.Op == OpDelete
		if isDeleted == nil || isDeleted.key.less(k) {
			isDeleted = &group{val: delVal, key: k}
		}
		if ev.Op == OpDelete {
			continue
		}
		for field, value := range ev.Payload {
			switch {
			case field == "tags":
				if arr, ok := value.([]any); ok && isSet(value) {
					for _, t := range arr {
						if s, ok := t.(string); ok {
							tags[s] = true
						}
					}
				}
			case field == "events":
				if arr, ok := value.([]any); ok {
					for _, e := range arr {
						if m, ok := e.(map[string]any); ok {
							spanEvents[spanEventID(m)] = m
						}
					}
				}
			case !isSet(value):
				// empty never clobbers
			case frozen[field]:
				if prev, ok := frozenFirst[field]; ok {
					if !jsonEqual(prev.val, value) {
						raw["llmobs.raw."+field] = value
						dq["frozen_field_conflict."+field]++
					}
				} else {
					frozenFirst[field] = group{val: value, key: k}
					setScalar(scalars, field, value, k)
				}
			case mapFields[field]:
				if m, ok := value.(map[string]any); ok {
					for path, leaf := range flatten(field, m) {
						if isSet(leaf) {
							setScalar(scalars, path, leaf, k)
						}
					}
				}
			default:
				setScalar(scalars, field, value, k)
			}
		}
	}

	out := map[string]any{}
	for path, g := range scalars {
		assignPath(out, strings.Split(path, "."), g.val)
	}
	for k, v := range raw {
		assignPath(out, []string{"attributes", k}, v)
	}
	if len(dq) > 0 {
		out["dq"] = float64Map(dq)
	}
	if len(tags) > 0 {
		ts := make([]string, 0, len(tags))
		for t := range tags {
			ts = append(ts, t)
		}
		sort.Strings(ts)
		out["tags"] = toAnySlice(ts)
	}
	if len(spanEvents) > 0 {
		evs := make([]map[string]any, 0, len(spanEvents))
		for _, e := range spanEvents {
			evs = append(evs, e)
		}
		sort.SliceStable(evs, func(i, j int) bool { return spanEventLess(evs[i], evs[j]) })
		anyEvs := make([]any, len(evs))
		for i, e := range evs {
			anyEvs[i] = e
		}
		out["events"] = anyEvs
	}
	del := false
	if isDeleted != nil {
		del, _ = isDeleted.val.(bool)
	}
	out["is_deleted"] = del
	return out
}

func setScalar(scalars map[string]group, path string, val any, k orderKey) {
	if g, ok := scalars[path]; !ok || g.key.less(k) {
		scalars[path] = group{val: val, key: k}
	}
}

// flatten produces dotted leaf paths for a map-field's nested value.
func flatten(prefix string, m map[string]any) map[string]any {
	out := map[string]any{}
	for kk, vv := range m {
		p := prefix + "." + kk
		if nested, ok := vv.(map[string]any); ok {
			for np, nv := range flatten(p, nested) {
				out[np] = nv
			}
		} else {
			out[p] = vv
		}
	}
	return out
}

func assignPath(root map[string]any, path []string, val any) {
	d := root
	for _, p := range path[:len(path)-1] {
		next, ok := d[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			d[p] = next
		}
		d = next
	}
	d[path[len(path)-1]] = val
}

func spanEventID(m map[string]any) string {
	b, _ := json.Marshal(map[string]any{"name": m["name"], "timestamp": m["timestamp"], "attributes": m["attributes"]})
	return string(b)
}

func spanEventLess(a, b map[string]any) bool {
	ai, _ := toFloat(a["timestamp"])
	bi, _ := toFloat(b["timestamp"])
	if ai != bi {
		return ai < bi
	}
	an, _ := a["name"].(string)
	bn, _ := b["name"].(string)
	return an < bn
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	default:
		return 0, false
	}
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func float64Map(m map[string]float64) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
