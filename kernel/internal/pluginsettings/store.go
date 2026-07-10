package pluginsettings

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// settingsKey is the reserved kv document key holding a plugin's settings for one
// project. Settings is one document per (plugin, project), stored via the kv store.
const settingsKey = "__settings__"

// KV is the storage surface (interface-at-consumer): the plugin kv store scoped to
// (plugin_id, project_id). The store never opens a DB itself.
type KV interface {
	Get(ctx context.Context, pluginID, projectID, key string) (json.RawMessage, bool, error)
	Set(ctx context.Context, pluginID, projectID, key string, value json.RawMessage) error
}

// Box seals/opens secret values with the kernel master key (the same envelope used
// by the secrets primitive).
type Box interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
	Open(ciphertext, nonce []byte) ([]byte, error)
}

// Store persists plugin settings: non-secret values in the clear, secret
// (writeOnly) fields envelope-encrypted and never returned.
type Store struct {
	kv  KV
	box Box
}

func NewStore(kv KV, box Box) *Store { return &Store{kv: kv, box: box} }

// stored is the on-disk shape of a settings document.
type stored struct {
	Values  map[string]json.RawMessage `json:"values"`  // non-secret fields, plaintext
	Secrets map[string]sealed          `json:"secrets"` // secret fields, envelope-encrypted
}

type sealed struct {
	Ciphertext string `json:"ciphertext"` // base64
	Nonce      string `json:"nonce"`      // base64
}

// View is what a GET returns: non-secret values plus, for each secret field, only a
// boolean saying whether a value is stored. THE PROVE-THE-NEGATIVE: a secret value is
// never decrypted or included here — the client learns "set / not set", nothing more.
type View struct {
	Values  map[string]json.RawMessage `json:"values"`
	Secrets map[string]bool            `json:"secrets"`
}

// Get returns the rendered view for (plugin, project). Missing document → empty view
// (all secrets not-set, values empty). Secret ciphertext is never opened.
func (s *Store) Get(ctx context.Context, m *Model, pluginID, projectID string) (View, error) {
	doc, err := s.load(ctx, pluginID, projectID)
	if err != nil {
		return View{}, err
	}
	v := View{Values: map[string]json.RawMessage{}, Secrets: map[string]bool{}}
	for _, f := range m.Fields {
		if f.Secret {
			_, set := doc.Secrets[f.Name]
			v.Secrets[f.Name] = set
			continue
		}
		if raw, ok := doc.Values[f.Name]; ok {
			v.Values[f.Name] = raw
		}
	}
	return v, nil
}

// Set validates and persists an incoming settings write. Non-secret fields overwrite;
// a secret field that is provided-and-non-empty is (re)encrypted, while a secret that
// is absent or empty is PRESERVED (re-saving a form never wipes a secret the user did
// not retype). Unknown/invalid input returns a *ValidationError (a client 400).
func (s *Store) Set(ctx context.Context, m *Model, pluginID, projectID string, incoming map[string]json.RawMessage) error {
	doc, err := s.load(ctx, pluginID, projectID)
	if err != nil {
		return err
	}
	alreadySet := map[string]bool{}
	for name := range doc.Secrets {
		alreadySet[name] = true
	}
	if err := Validate(m, incoming, alreadySet); err != nil {
		return err
	}
	if doc.Values == nil {
		doc.Values = map[string]json.RawMessage{}
	}
	if doc.Secrets == nil {
		doc.Secrets = map[string]sealed{}
	}
	for _, f := range m.Fields {
		raw, present := incoming[f.Name]
		if !present {
			continue
		}
		if f.Secret {
			if isEmptyString(raw) {
				continue // preserve existing
			}
			var plain string
			if err := json.Unmarshal(raw, &plain); err != nil {
				return &ValidationError{Field: f.Name, Reason: "secret must be a string"}
			}
			ct, nonce, err := s.box.Seal([]byte(plain))
			if err != nil {
				return fmt.Errorf("seal secret %q: %w", f.Name, err)
			}
			doc.Secrets[f.Name] = sealed{
				Ciphertext: base64.StdEncoding.EncodeToString(ct),
				Nonce:      base64.StdEncoding.EncodeToString(nonce),
			}
			continue
		}
		doc.Values[f.Name] = raw
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	return s.kv.Set(ctx, pluginID, projectID, settingsKey, out)
}

func (s *Store) load(ctx context.Context, pluginID, projectID string) (stored, error) {
	raw, found, err := s.kv.Get(ctx, pluginID, projectID, settingsKey)
	if err != nil {
		return stored{}, fmt.Errorf("load settings: %w", err)
	}
	doc := stored{Values: map[string]json.RawMessage{}, Secrets: map[string]sealed{}}
	if !found {
		return doc, nil
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return stored{}, fmt.Errorf("decode settings doc: %w", err)
	}
	return doc, nil
}
