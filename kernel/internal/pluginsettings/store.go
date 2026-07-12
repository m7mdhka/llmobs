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
// (plugin_id, project_id, user_id). Settings are PROJECT-shared plugin config, so this store
// always passes the project-scope sentinel (user_id "") — a plugin's settings are the same for
// every user of the project (per-user state uses the kv primitive's user scope).
type KV interface {
	Get(ctx context.Context, pluginID, projectID, userID, key string) (json.RawMessage, bool, error)
	Set(ctx context.Context, pluginID, projectID, userID, key string, value json.RawMessage) error
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
	if m.Custom {
		// Custom mode: non-secret values are opaque, so return ALL of them EXCEPT any
		// key that is currently a declared secret. Skipping declared-secret keys makes the
		// encrypt-and-never-return guarantee hold BY CONSTRUCTION — enforced once at this read
		// seam, never re-checked per caller: even if a field was reclassified from opaque to
		// writeOnly and a stale plaintext value lingers under that name, it is never served —
		// mirroring the schema-mode Get's guarantee. A secret's ciphertext
		// lives in doc.Secrets and is NEVER decrypted; only its set/not-set is reported.
		secret := map[string]bool{}
		for _, f := range m.Fields {
			if f.Secret {
				secret[f.Name] = true
			}
		}
		for k, raw := range doc.Values {
			if secret[k] {
				continue // a declared secret is never served as a plaintext value
			}
			v.Values[k] = raw
		}
		for name := range secret {
			_, set := doc.Secrets[name]
			v.Secrets[name] = set
		}
		return v, nil
	}
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
	seal := func(name string, raw json.RawMessage) error {
		var plain string
		if err := json.Unmarshal(raw, &plain); err != nil {
			return &ValidationError{Field: name, Reason: "secret must be a string"}
		}
		ct, nonce, err := s.box.Seal([]byte(plain))
		if err != nil {
			return fmt.Errorf("seal secret %q: %w", name, err)
		}
		doc.Secrets[name] = sealed{
			Ciphertext: base64.StdEncoding.EncodeToString(ct),
			Nonce:      base64.StdEncoding.EncodeToString(nonce),
		}
		delete(doc.Values, name) // reap any plaintext lingering under a (now-)secret name
		return nil
	}
	if m.Custom {
		// Custom mode: a declared secret is encrypted (preserve on empty) exactly as
		// in schema mode; every OTHER incoming key is stored as opaque JSON. A secret name
		// is NEVER written to the plaintext Values map — so it can never be read back.
		secret := map[string]bool{}
		for _, f := range m.Fields {
			if f.Secret {
				secret[f.Name] = true
			}
		}
		for name, raw := range incoming {
			if secret[name] {
				if isEmptyString(raw) {
					continue // preserve existing
				}
				if err := seal(name, raw); err != nil {
					return err
				}
				delete(doc.Values, name) // defense: a secret never lingers in plaintext
				continue
			}
			doc.Values[name] = raw
		}
	} else {
		for _, f := range m.Fields {
			raw, present := incoming[f.Name]
			if !present {
				continue
			}
			if f.Secret {
				if isEmptyString(raw) {
					continue // preserve existing
				}
				if err := seal(f.Name, raw); err != nil {
					return err
				}
				continue
			}
			doc.Values[f.Name] = raw
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	// Bound the stored document — critical in custom mode where non-secret values are
	// un-schema'd opaque JSON (a plugin must not turn project-shared settings into
	// unbounded storage). Checked against the MERGED doc so growth across writes is caught.
	if len(out) > MaxValueBytes {
		return &ValidationError{Field: "", Reason: fmt.Sprintf("settings document exceeds %d bytes", MaxValueBytes)}
	}
	return s.kv.Set(ctx, pluginID, projectID, "", settingsKey, out)
}

func (s *Store) load(ctx context.Context, pluginID, projectID string) (stored, error) {
	raw, found, err := s.kv.Get(ctx, pluginID, projectID, "", settingsKey)
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
