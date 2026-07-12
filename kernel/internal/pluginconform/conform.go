// Package pluginconform is the plugin conformance harness. The storage
// conformance (tools/conformance) proves an adapter reproduces the merge; THIS
// proves a plugin honours the plugin contract: manifest validity,
// capability-within-grant, handshake correctness, and health/watermark shape.
// First-party plugins run through it in CI — the future "verified plugin" bar.
//
// Cross-tenant isolation is a KERNEL invariant, proven where it is enforced
// (the intersection and primitive-isolation prove-the-negatives); a plugin
// cannot violate it, so the harness asserts the kernel-side guarantees exist
// rather than re-testing them from the plugin side.
package pluginconform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/m7mdhka/llmobs/kernel/internal/jobs"
	"github.com/m7mdhka/llmobs/kernel/internal/plugindata"
	"github.com/m7mdhka/llmobs/kernel/internal/pluginsettings"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// Result is one conformance check outcome.
type Result struct {
	Check  string
	Pass   bool
	Detail string
}

// Report is the set of results plus a pass rollup.
type Report struct{ Results []Result }

func (r Report) OK() bool {
	for _, x := range r.Results {
		if !x.Pass {
			return false
		}
	}
	return true
}

func (r *Report) add(check string, pass bool, detail string) {
	r.Results = append(r.Results, Result{Check: check, Pass: pass, Detail: detail})
}

// Manifest is the parsed plugin manifest (the fields conformance inspects).
type Manifest struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		ID      string `yaml:"id"`
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
	} `yaml:"metadata"`
	Spec struct {
		Capabilities   []string `yaml:"capabilities"`
		Permissions    []string `yaml:"permissions"`
		SettingsSchema string   `yaml:"settingsSchema"`
		// SettingsView selects schema vs custom mode; a runner passes
		// (SettingsView == "custom") to CheckSettingsSchema so a custom plugin's
		// intentionally non-subset schema is validated for secrets, not the flat subset.
		SettingsView string `yaml:"settingsView"`
		Frontend     *struct {
			RemoteName    string `yaml:"remoteName"`
			ExposedModule string `yaml:"exposedModule"`
			Entry         string `yaml:"entry"`
		} `yaml:"frontend"`
		Backend *struct {
			URL        string `yaml:"url"`
			HealthPath string `yaml:"healthPath"`
		} `yaml:"backend"`
		Store *struct {
			Collections []struct {
				Name   string `yaml:"name"`
				Fields []struct {
					Name    string `yaml:"name"`
					Type    string `yaml:"type"`
					Indexed bool   `yaml:"indexed"`
				} `yaml:"fields"`
			} `yaml:"collections"`
		} `yaml:"store"`
		Jobs []struct {
			Name     string `yaml:"name"`
			Schedule string `yaml:"schedule"`
			Path     string `yaml:"path"`
		} `yaml:"jobs"`
	} `yaml:"spec"`
}

var (
	idRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9-]*$`)
	semRe  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+].+)?$`)
	permRe = regexp.MustCompile(`^[a-z_]+:[a-z_.]+$`)
	capSet = map[string]bool{"ingest": true, "query": true, "write": true, "events": true, "jobs": true, "kv": true, "secrets": true, "surface": true, "store": true}
)

// CheckManifest runs the static (no-backend) conformance checks over a manifest.
func CheckManifest(raw []byte) (Report, *Manifest) {
	var rep Report
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		rep.add("manifest-parses", false, err.Error())
		return rep, nil
	}
	rep.add("manifest-parses", true, "")
	rep.add("apiVersion", m.APIVersion == "llmobs.dev/v1alpha1", m.APIVersion)
	rep.add("kind", m.Kind == "Plugin", m.Kind)
	rep.add("id-format", idRe.MatchString(m.Metadata.ID), m.Metadata.ID)
	rep.add("version-semver", semRe.MatchString(m.Metadata.Version), m.Metadata.Version)

	capsOK, badCap := true, ""
	for _, c := range m.Spec.Capabilities {
		if !capSet[c] {
			capsOK, badCap = false, c
		}
	}
	rep.add("capabilities-known", capsOK, badCap)

	permsOK, badPerm := true, ""
	for _, p := range m.Spec.Permissions {
		if !permRe.MatchString(p) {
			permsOK, badPerm = false, p
		}
	}
	rep.add("permissions-format", permsOK, badPerm)

	// A surface capability requires a frontend; a backend requires url+healthPath.
	if hasCap(m.Spec.Capabilities, "surface") {
		rep.add("surface-has-frontend", m.Spec.Frontend != nil, "capability 'surface' declared")
	}
	if m.Spec.Backend != nil {
		rep.add("backend-url+health", m.Spec.Backend.URL != "" && m.Spec.Backend.HealthPath != "", "")
	}
	// Store collections must validate against their declared query-surface rules.
	if m.Spec.Store != nil {
		for _, c := range m.Spec.Store.Collections {
			spec := plugindata.CollectionSpec{Name: c.Name}
			for _, f := range c.Fields {
				spec.Fields = append(spec.Fields, plugindata.FieldSpec{Name: f.Name, Type: plugindata.FieldType(f.Type), Indexed: f.Indexed})
			}
			err := spec.Validate()
			rep.add("store-collection:"+c.Name, err == nil, detailErr(err))
		}
	}
	// Jobs need a name + path; a declared schedule must parse.
	for _, j := range m.Spec.Jobs {
		ok := j.Name != "" && j.Path != ""
		if j.Schedule != "" {
			if _, err := jobs.Due(j.Schedule, time.Now(), time.Time{}); err != nil {
				ok = false
			}
		}
		rep.add("job:"+j.Name, ok, j.Schedule)
	}
	return rep, &m
}

// CheckSettingsSchema verifies a plugin's settings JSON Schema. In SCHEMA mode it
// must be within the supported flat subset the kernel + SchemaForm both validate (so a
// settings tab renders and writes validate identically). In CUSTOM mode the plugin owns
// validation of its opaque non-secret values, so the schema is checked only for valid
// writeOnly (secret) declarations — non-subset property types are allowed. Called by
// conformance when the manifest declares settings; the caller supplies the loaded schema
// bytes (may be empty in custom mode) and whether the plugin is in custom mode.
func CheckSettingsSchema(raw []byte, custom bool) Report {
	var rep Report
	if len(raw) == 0 {
		// A custom plugin may carry no schema (no secrets); schema mode requires one.
		rep.add("settings-schema-json", custom, "schema mode requires a settingsSchema")
		return rep
	}
	if !json.Valid(raw) {
		rep.add("settings-schema-json", false, "not valid JSON")
		return rep
	}
	rep.add("settings-schema-json", true, "")
	_, err := pluginsettings.ParseSchema(raw, custom)
	label := "settings-schema-subset"
	if custom {
		label = "settings-schema-secrets"
	}
	rep.add(label, err == nil, detailErr(err))
	return rep
}

// CheckLive runs the backend conformance checks (handshake + health +
// capability-within-grant) against a running plugin.
func CheckLive(ctx context.Context, backendURL string, m *Manifest, client *http.Client) Report {
	var rep Report
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	var info pluginproto.Info
	if err := getJSON(ctx, client, backendURL+pluginproto.DefaultInfoPath, &info); err != nil {
		rep.add("handshake-reachable", false, err.Error())
		return rep
	}
	rep.add("handshake-reachable", true, "")
	rep.add("handshake-id-matches", info.ID == m.Metadata.ID, info.ID+" vs "+m.Metadata.ID)
	rep.add("handshake-protocol-supported", pluginproto.ProtocolSupported(info.PluginAPIVersion), info.PluginAPIVersion)
	// Capability-within-grant: the plugin must NOT echo a capability the manifest
	// did not grant (defence in depth — the manifest is authoritative).
	rep.add("capability-within-grant", info.Check(m.Metadata.ID, m.Spec.Capabilities) == nil, detailErr(info.Check(m.Metadata.ID, m.Spec.Capabilities)))

	var h pluginproto.Health
	if err := getJSON(ctx, client, backendURL+pluginproto.DefaultHealthPath, &h); err != nil {
		rep.add("health-reachable", false, err.Error())
		return rep
	}
	rep.add("health-reachable", true, "")
	rep.add("health-live", h.Live, "")
	return rep
}

func hasCap(caps []string, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

func detailErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func getJSON(ctx context.Context, c *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
