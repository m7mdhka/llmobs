package pluginsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/platform/secretbox"
)

// memKV is an in-memory KV scoped by (plugin, project, key).
type memKV struct{ data map[string]json.RawMessage }

func newMemKV() *memKV { return &memKV{data: map[string]json.RawMessage{}} }

func (m *memKV) k(p, pr, key string) string { return p + "\x00" + pr + "\x00" + key }

func (m *memKV) Get(_ context.Context, p, pr, key string) (json.RawMessage, bool, error) {
	v, ok := m.data[m.k(p, pr, key)]
	return v, ok, nil
}
func (m *memKV) Set(_ context.Context, p, pr, key string, v json.RawMessage) error {
	m.data[m.k(p, pr, key)] = v
	return nil
}

func testSchema(t *testing.T) *Model {
	t.Helper()
	raw := []byte(`{
	  "type": "object",
	  "required": ["endpoint", "apiKey"],
	  "properties": {
	    "endpoint": {"type": "string", "minLength": 1},
	    "sampleRate": {"type": "number", "minimum": 0, "maximum": 1},
	    "enabled": {"type": "boolean"},
	    "mode": {"type": "string", "enum": ["a", "b"]},
	    "apiKey": {"type": "string", "writeOnly": true}
	  }
	}`)
	m, err := ParseSchema(raw)
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return m
}

func newStore(t *testing.T) (*Store, *memKV) {
	t.Helper()
	box, err := secretbox.NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	kv := newMemKV()
	return NewStore(kv, box), kv
}

// TestSecretNeverRenderedBack is the J2 prove-the-negative: a secret (writeOnly)
// field, once written, is NEVER returned by Get — the client only learns it is set —
// and the stored ciphertext never contains the plaintext.
func TestSecretNeverRenderedBack(t *testing.T) {
	m := testSchema(t)
	s, kv := newStore(t)
	ctx := context.Background()
	const plugin, project = "acme/dash", "projA"
	const secretVal = "sk-super-secret-value-123"

	err := s.Set(ctx, m, plugin, project, map[string]json.RawMessage{
		"endpoint": json.RawMessage(`"https://api.example.com"`),
		"apiKey":   json.RawMessage(`"` + secretVal + `"`),
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	view, err := s.Get(ctx, m, plugin, project)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// The secret is reported as SET but its value is absent from Values.
	if !view.Secrets["apiKey"] {
		t.Fatal("apiKey should be reported as set")
	}
	if _, leaked := view.Values["apiKey"]; leaked {
		t.Fatal("secret must NEVER appear in the returned values")
	}
	// Belt-and-braces: the plaintext appears nowhere in the serialized view.
	blob, _ := json.Marshal(view)
	if bytes.Contains(blob, []byte(secretVal)) {
		t.Fatalf("secret plaintext leaked into the GET response: %s", blob)
	}
	// And it is stored ENCRYPTED — the raw kv document must not contain the plaintext.
	rawDoc := kv.data[kv.k(plugin, project, settingsKey)]
	if bytes.Contains(rawDoc, []byte(secretVal)) {
		t.Fatalf("secret stored in the clear: %s", rawDoc)
	}
	// The non-secret value IS returned.
	if got := string(view.Values["endpoint"]); got != `"https://api.example.com"` {
		t.Fatalf("endpoint value wrong: %s", got)
	}
}

// TestSecretPreservedOnResave: re-saving the form without retyping the secret (empty
// or absent) keeps the stored secret — it is not wiped.
func TestSecretPreservedOnResave(t *testing.T) {
	m := testSchema(t)
	s, _ := newStore(t)
	ctx := context.Background()
	const plugin, project = "acme/dash", "projA"

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Set(ctx, m, plugin, project, map[string]json.RawMessage{
		"endpoint": json.RawMessage(`"https://a"`),
		"apiKey":   json.RawMessage(`"secret1"`),
	}))

	// Re-save with the secret ABSENT and again with EMPTY string — both must preserve.
	must(s.Set(ctx, m, plugin, project, map[string]json.RawMessage{"endpoint": json.RawMessage(`"https://b"`)}))
	must(s.Set(ctx, m, plugin, project, map[string]json.RawMessage{"endpoint": json.RawMessage(`"https://c"`), "apiKey": json.RawMessage(`""`)}))

	view, err := s.Get(ctx, m, plugin, project)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Secrets["apiKey"] {
		t.Fatal("secret must be preserved across re-saves that omit/blank it")
	}
	if got := string(view.Values["endpoint"]); got != `"https://c"` {
		t.Fatalf("non-secret should update, got %s", got)
	}
}

// TestValidationRejectsBadInput: required-missing, wrong type, out-of-enum, and
// out-of-bounds are all rejected as ValidationErrors (client 400s).
func TestValidationRejectsBadInput(t *testing.T) {
	m := testSchema(t)
	s, _ := newStore(t)
	ctx := context.Background()
	const plugin, project = "acme/dash", "projA"

	cases := map[string]map[string]json.RawMessage{
		"missing required endpoint": {"apiKey": json.RawMessage(`"k"`)},
		"missing required secret":   {"endpoint": json.RawMessage(`"https://a"`)},
		"wrong type sampleRate":     {"endpoint": json.RawMessage(`"https://a"`), "apiKey": json.RawMessage(`"k"`), "sampleRate": json.RawMessage(`"nope"`)},
		"enum violation":            {"endpoint": json.RawMessage(`"https://a"`), "apiKey": json.RawMessage(`"k"`), "mode": json.RawMessage(`"z"`)},
		"out of range sampleRate":   {"endpoint": json.RawMessage(`"https://a"`), "apiKey": json.RawMessage(`"k"`), "sampleRate": json.RawMessage(`2`)},
		"unknown field":             {"endpoint": json.RawMessage(`"https://a"`), "apiKey": json.RawMessage(`"k"`), "bogus": json.RawMessage(`1`)},
	}
	for name, incoming := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.Set(ctx, m, plugin, project, incoming)
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if _, ok := AsValidationError(err); !ok {
				t.Fatalf("expected a *ValidationError, got %T: %v", err, err)
			}
		})
	}
}

// TestRequiredSecretSatisfiedByExisting: once a required secret is set, a later save
// that omits it is accepted (required is satisfied by the stored value).
func TestRequiredSecretSatisfiedByExisting(t *testing.T) {
	m := testSchema(t)
	s, _ := newStore(t)
	ctx := context.Background()
	const plugin, project = "acme/dash", "projA"

	if err := s.Set(ctx, m, plugin, project, map[string]json.RawMessage{
		"endpoint": json.RawMessage(`"https://a"`), "apiKey": json.RawMessage(`"k"`),
	}); err != nil {
		t.Fatal(err)
	}
	// Now omit the required secret — should pass because it is already set.
	if err := s.Set(ctx, m, plugin, project, map[string]json.RawMessage{
		"endpoint": json.RawMessage(`"https://a2"`),
	}); err != nil {
		t.Fatalf("omitting an already-set required secret must be allowed: %v", err)
	}
}
