// Package postgres implements the lite-profile storage adapter.
//
// This file is the real implementation of the canonical merge algorithm
// (api/model/v1alpha1/05-update-semantics.md), written from the spec. The lite
// adapter applies it as merge-on-write under a row lock (LM-5); the same fold is
// the conformance target the ClickHouse adapter must also satisfy.
//
// Merge is per-field-group: each field-group carries its own (event_ts, event_id)
// provenance, so out-of-order updates fold correctly (spec V3) — not a single
// row-level stamp. The stored provenance lets a read-modify-write reproduce the
// full ordered Fold from the persisted state + one new event (issue #17 closed).
package postgres

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// Op and Event are the neutral ingest types, owned by the storage package. They
// are aliased here so the adapter's fold code reads naturally and external
// callers use the neutral types.
type Op = storage.Op

const (
	OpUpsert = storage.OpUpsert
	OpDelete = storage.OpDelete
)

// Event is one ingested event targeting a single entity (05 §1).
type Event = storage.Event

// frozenFields per entity type (05 §5).
var frozenFields = map[string]map[string]bool{
	"span":  {"id": true, "project_id": true, "trace_id": true, "kind": true, "start_time": true, "environment": true},
	"trace": {"id": true, "project_id": true, "start_time": true, "environment": true},
	"score": {"id": true, "project_id": true, "subject_type": true, "subject_id": true, "timestamp": true, "environment": true},
}

// mapFields are deep-merged per leaf key (05 §2); everything else is a scalar
// field-group replaced wholesale, latest-wins.
var mapFields = map[string]bool{
	"attributes": true, "metadata": true, "model_parameters": true,
	"usage_details": true, "cost_details": true,
	"provided_usage_details": true, "provided_cost_details": true,
}

// isDeletedKey is the reserved provenance key for the is_deleted field-group.
const isDeletedKey = "is_deleted"

// pathSep joins field-group path segments so that attribute keys containing dots
// (e.g. "gen_ai.request.model") stay a single key and only real nested objects
// deep-merge — never conflate a dotted key with a path. It is SOH (0x01): absent
// from real keys, and unlike NUL (0x00) it is legal in Postgres jsonb, which the
// provenance column persists (NUL raises SQLSTATE 22P05).
const pathSep = "\x01"

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

// Stamp is the JSON-serializable provenance of a field-group.
type Stamp struct {
	TS  time.Time `json:"ts"`
	EID string    `json:"eid"`
}

func (s Stamp) key() orderKey { return orderKey{ts: s.TS, eid: s.EID} }
func stampOf(k orderKey) Stamp {
	return Stamp{TS: k.ts, EID: k.eid}
}

// Provenance maps a field-group path (and the reserved is_deleted key) to its
// winning stamp. Persisted alongside the folded state.
type Provenance map[string]Stamp

// folder accumulates the fold; it can be seeded from stored state + provenance.
type folder struct {
	frozen      map[string]bool
	scalars     map[string]group // dotted leaf path -> winning (value, key)
	tags        map[string]bool
	spanEvents  map[string]map[string]any
	frozenFirst map[string]group
	raw         map[string]any     // llmobs.raw.<field>
	dq          map[string]float64 // frozen_field_conflict.<field> counts
	isDeleted   *group
}

func newFolder(entityType string) *folder {
	return &folder{
		frozen:      frozenFields[entityType],
		scalars:     map[string]group{},
		tags:        map[string]bool{},
		spanEvents:  map[string]map[string]any{},
		frozenFirst: map[string]group{},
		raw:         map[string]any{},
		dq:          map[string]float64{},
	}
}

// apply folds one event into the accumulator (order-independent per field-group).
func (f *folder) apply(ev Event) {
	k := orderKey{ts: ev.EventTS, eid: ev.EventID}
	if f.isDeleted == nil || f.isDeleted.key.less(k) {
		f.isDeleted = &group{val: ev.Op == OpDelete, key: k}
	}
	if ev.Op == OpDelete {
		return
	}
	for field, value := range ev.Payload {
		switch {
		case field == "tags":
			if arr, ok := value.([]any); ok {
				for _, t := range arr {
					if s, ok := t.(string); ok {
						f.tags[s] = true
					}
				}
			}
		case field == "events":
			if arr, ok := value.([]any); ok {
				for _, e := range arr {
					if m, ok := e.(map[string]any); ok {
						f.spanEvents[spanEventID(m)] = m
					}
				}
			}
		case !isSet(value):
			// empty never clobbers
		case f.frozen[field]:
			f.applyFrozen(field, value, k)
		case mapFields[field]:
			if m, ok := value.(map[string]any); ok {
				for path, leaf := range flatten(field, m) {
					if isSet(leaf) {
						setScalar(f.scalars, path, leaf, k)
					}
				}
			}
		default:
			setScalar(f.scalars, field, value, k)
		}
	}
}

// applyFrozen resolves a frozen field: the earliest (event_ts,event_id) setter
// wins the value; a later-or-earlier differing value is absorbed (llmobs.raw.* +
// dq), never applied. Earliest-wins holds regardless of arrival order.
func (f *folder) applyFrozen(field string, value any, k orderKey) {
	prev, ok := f.frozenFirst[field]
	if !ok {
		f.frozenFirst[field] = group{val: value, key: k}
		setScalar(f.scalars, field, value, k)
		return
	}
	if k.less(prev.key) {
		// the new event is earlier -> it becomes the frozen value; old conflicts
		if !jsonEqual(prev.val, value) {
			f.raw["llmobs.raw."+field] = prev.val
			f.dq["frozen_field_conflict."+field]++
		}
		f.frozenFirst[field] = group{val: value, key: k}
		f.scalars[field] = group{val: value, key: k}
		return
	}
	if !jsonEqual(prev.val, value) {
		f.raw["llmobs.raw."+field] = value
		f.dq["frozen_field_conflict."+field]++
	}
}

// state reconstructs the observable entity.
func (f *folder) state() map[string]any {
	out := map[string]any{}
	for path, g := range f.scalars {
		assignPath(out, strings.Split(path, pathSep), g.val)
	}
	for k, v := range f.raw {
		assignPath(out, []string{"attributes", k}, v)
	}
	if len(f.dq) > 0 {
		out["dq"] = float64Map(f.dq)
	}
	if len(f.tags) > 0 {
		ts := make([]string, 0, len(f.tags))
		for t := range f.tags {
			ts = append(ts, t)
		}
		sort.Strings(ts)
		out["tags"] = toAnySlice(ts)
	}
	if len(f.spanEvents) > 0 {
		evs := make([]map[string]any, 0, len(f.spanEvents))
		for _, e := range f.spanEvents {
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
	if f.isDeleted != nil {
		del, _ = f.isDeleted.val.(bool)
	}
	out["is_deleted"] = del
	return out
}

// provenance returns the winning stamp per scalar field-group + is_deleted.
func (f *folder) provenance() Provenance {
	p := Provenance{}
	for path, g := range f.scalars {
		p[path] = stampOf(g.key)
	}
	if f.isDeleted != nil {
		p[isDeletedKey] = stampOf(f.isDeleted.key)
	}
	return p
}

// Fold computes the observable entity state from a set of events, per 05 §2–§5.
// entityType selects the frozen-field set. Order-independent and idempotent.
func Fold(entityType string, events []Event) map[string]any {
	f := newFolder(entityType)
	ordered := append([]Event(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return orderKey{ordered[i].EventTS, ordered[i].EventID}.less(orderKey{ordered[j].EventTS, ordered[j].EventID})
	})
	for _, ev := range ordered {
		f.apply(ev)
	}
	return f.state()
}

// MergeEvent applies one event to a stored (state, provenance), returning the new
// (state, provenance). This is the read-modify-write the lite adapter runs under
// the row lock; because provenance is per-field-group, it is equivalent to the
// full Fold over the whole ordered event set (out-of-order safe).
func MergeEvent(entityType string, state map[string]any, prov Provenance, ev Event) (map[string]any, Provenance) {
	f := seedFolder(entityType, state, prov)
	f.apply(ev)
	return f.state(), f.provenance()
}

// seedFolder reconstructs a folder from stored state + provenance.
func seedFolder(entityType string, state map[string]any, prov Provenance) *folder {
	f := newFolder(entityType)
	for k, v := range state {
		switch {
		case k == "tags":
			if arr, ok := v.([]any); ok {
				for _, t := range arr {
					if s, ok := t.(string); ok {
						f.tags[s] = true
					}
				}
			}
		case k == "events":
			if arr, ok := v.([]any); ok {
				for _, e := range arr {
					if m, ok := e.(map[string]any); ok {
						f.spanEvents[spanEventID(m)] = m
					}
				}
			}
		case k == isDeletedKey:
			del, _ := v.(bool)
			f.isDeleted = &group{val: del, key: prov[isDeletedKey].key()}
		case k == "dq":
			if m, ok := v.(map[string]any); ok {
				for dk, dv := range m {
					if n, ok := toFloat(dv); ok {
						f.dq[dk] = n
					}
				}
			}
		case mapFields[k]:
			if m, ok := v.(map[string]any); ok {
				for path, leaf := range flatten(k, m) {
					f.scalars[path] = group{val: leaf, key: prov[path].key()}
				}
			}
		default:
			f.scalars[k] = group{val: v, key: prov[k].key()}
		}
	}
	for field := range f.frozen {
		if g, ok := f.scalars[field]; ok {
			f.frozenFirst[field] = g
		}
	}
	return f
}

func setScalar(scalars map[string]group, path string, val any, k orderKey) {
	if g, ok := scalars[path]; !ok || g.key.less(k) {
		scalars[path] = group{val: val, key: k}
	}
}

// flatten produces pathSep-joined leaf paths for a map-field's nested value.
// Only real nested objects recurse; a top-level key with dots stays one key.
func flatten(prefix string, m map[string]any) map[string]any {
	out := map[string]any{}
	for kk, vv := range m {
		p := prefix + pathSep + kk
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
