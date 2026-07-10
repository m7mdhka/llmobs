package postgres

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

var errMissingIdentity = errors.New("span event missing project_id or id")

func itoa(i int) string { return strconv.Itoa(i) }

// spanColumns are the promoted/indexed columns extracted from the folded span for
// querying; the authoritative value is the `doc` JSONB.
type spanColumns struct {
	traceID, parentSpanID, kind, rawKind, name *string
	statusCode, environment, release, version  *string
	sessionID, userID, model, provider         *string
	startTime, endTime, completionStartTime    *time.Time
	totalCost                                  *float64
	attributes, usageDetails, costDetails      []byte
	providedUsageDetails, providedCostDetails  []byte
	promptRef, pricingSnapshotRef              []byte
	isDeleted                                  bool
}

func extractSpanColumns(m map[string]any, _ time.Time) spanColumns {
	c := spanColumns{
		traceID:              strPtr(m, "trace_id"),
		parentSpanID:         strPtr(m, "parent_span_id"),
		kind:                 strPtr(m, "kind"),
		rawKind:              strPtr(m, "raw_kind"),
		name:                 strPtr(m, "name"),
		environment:          strPtr(m, "environment"),
		release:              strPtr(m, "release"),
		version:              strPtr(m, "version"),
		sessionID:            strPtr(m, "session_id"),
		userID:               strPtr(m, "user_id"),
		model:                strPtr(m, "model"),
		provider:             strPtr(m, "provider"),
		startTime:            timePtr(m, "start_time"),
		endTime:              timePtr(m, "end_time"),
		completionStartTime:  timePtr(m, "completion_start_time"),
		totalCost:            floatPtr(m, "total_cost"),
		attributes:           jsonbOr(m, "attributes", "{}"),
		usageDetails:         jsonbOr(m, "usage_details", "{}"),
		costDetails:          jsonbOr(m, "cost_details", "{}"),
		providedUsageDetails: jsonbOr(m, "provided_usage_details", "{}"),
		providedCostDetails:  jsonbOr(m, "provided_cost_details", "{}"),
		promptRef:            jsonbOrNull(m, "prompt_ref"),
		pricingSnapshotRef:   jsonbOrNull(m, "pricing_snapshot_ref"),
	}
	if s := statusCode(m); s != nil {
		c.statusCode = s
	}
	if d, ok := m["is_deleted"].(bool); ok {
		c.isDeleted = d
	}
	return c
}

func strPtr(m map[string]any, key string) *string {
	if v, ok := m[key].(string); ok && v != "" {
		return &v
	}
	return nil
}

func floatPtr(m map[string]any, key string) *float64 {
	switch v := m[key].(type) {
	case float64:
		return &v
	case int:
		f := float64(v)
		return &f
	}
	return nil
}

func timePtr(m map[string]any, key string) *time.Time {
	s, ok := m[key].(string)
	if !ok || s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, s); err != nil {
			return nil
		}
	}
	tu := t.UTC()
	return &tu
}

func statusCode(m map[string]any) *string {
	st, ok := m["status"].(map[string]any)
	if !ok {
		return nil
	}
	if code, ok := st["code"].(string); ok && code != "" {
		return &code
	}
	return nil
}

func jsonbOr(m map[string]any, key, fallback string) []byte {
	if v, ok := m[key]; ok && v != nil {
		if b, err := json.Marshal(v); err == nil {
			return b
		}
	}
	return []byte(fallback)
}

func jsonbOrNull(m map[string]any, key string) []byte {
	if v, ok := m[key]; ok && v != nil {
		if b, err := json.Marshal(v); err == nil {
			return b
		}
	}
	return nil
}
