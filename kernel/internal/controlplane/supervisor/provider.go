package supervisor

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/executors"
)

// PluginSpec is the supervised view of a plugin: identity, its approved
// capabilities/permissions (the manifest is authoritative), and its backend
// reachability. Only plugins that declare spec.backend are supervised — Tier-2
// (frontend-only) plugins have no backend to supervise.
type PluginSpec struct {
	ID                  string
	GrantedCapabilities []string
	GrantedScopes       []string
	Backend             executors.Backend
	WatermarkBudget     time.Duration
}

// Provider supplies the currently-installed backend plugins to supervise.
type Provider interface {
	Plugins() []PluginSpec
}

// StaticProvider is a fixed list — used by tests and by any caller that resolves
// specs itself.
type StaticProvider struct{ Specs []PluginSpec }

func (s StaticProvider) Plugins() []PluginSpec { return s.Specs }

// DirProvider scans a plugin directory (the lite dev layout: one subdir per
// plugin with an llmobs-plugin.yaml) and returns the backend plugins.
type DirProvider struct {
	root string
	log  *slog.Logger
}

func NewDirProvider(root string, log *slog.Logger) *DirProvider {
	return &DirProvider{root: root, log: log}
}

// manifestDoc is the subset of the manifest the supervisor needs (capabilities,
// permissions, backend). Kept in sync with the manifest JSON Schema.
type manifestDoc struct {
	Metadata struct {
		ID string `yaml:"id"`
	} `yaml:"metadata"`
	Spec struct {
		Capabilities []string `yaml:"capabilities"`
		Permissions  []string `yaml:"permissions"`
		Backend      *struct {
			URL             string `yaml:"url"`
			HealthPath      string `yaml:"healthPath"`
			InfoPath        string `yaml:"infoPath"`
			WatermarkBudget string `yaml:"watermarkBudget"`
		} `yaml:"backend"`
	} `yaml:"spec"`
}

func (d *DirProvider) Plugins() []PluginSpec {
	if d.root == "" {
		return nil
	}
	entries, err := os.ReadDir(d.root)
	if err != nil {
		d.log.Warn("plugin dir not readable; no backends supervised", "dir", d.root, "err", err.Error())
		return nil
	}
	var out []PluginSpec
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(d.root, e.Name(), "llmobs-plugin.yaml"))
		if err != nil {
			continue
		}
		var m manifestDoc
		if err := yaml.Unmarshal(raw, &m); err != nil {
			d.log.Warn("skipping unparseable manifest", "dir", e.Name(), "err", err.Error())
			continue
		}
		if m.Metadata.ID == "" || m.Spec.Backend == nil || m.Spec.Backend.URL == "" {
			continue // no backend to supervise
		}
		budget, _ := time.ParseDuration(m.Spec.Backend.WatermarkBudget)
		out = append(out, PluginSpec{
			ID:                  m.Metadata.ID,
			GrantedCapabilities: m.Spec.Capabilities,
			GrantedScopes:       m.Spec.Permissions,
			Backend: executors.Backend{
				URL:        m.Spec.Backend.URL,
				InfoPath:   m.Spec.Backend.InfoPath,
				HealthPath: m.Spec.Backend.HealthPath,
			},
			WatermarkBudget: budget,
		})
	}
	return out
}
