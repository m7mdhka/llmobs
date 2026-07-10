package registry

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DirSource is the dev-mode plugin registry: it scans a directory whose
// immediate subdirectories each hold a plugin — an `llmobs-plugin.yaml` manifest
// and a built frontend bundle under `dist/`. The kernel serves each bundle and
// advertises it to the shell's loader with an integrity hash. No install
// lifecycle yet (Tier-3 arc); this makes first-party plugins loadable in lite.
type DirSource struct {
	root    string
	baseURL string // public path prefix the assets are served under
	log     *slog.Logger
	plugins []Plugin
	// dist roots keyed by the URL segment used in the asset path.
	assetRoots map[string]string
	// devRemotes maps plugin id -> a live dev-server remoteEntry URL (J3). When set
	// for a plugin, the registry advertises that URL instead of the built dist and
	// drops the integrity hash — so `make dev` hot-reloads the plugin's frontend from
	// its own rspack dev server. Empty in production.
	devRemotes map[string]string
}

// DirOption configures a DirSource before its initial scan.
type DirOption func(*DirSource)

// WithDevRemotes overrides plugin remoteEntry URLs with live dev-server URLs (J3),
// keyed by plugin id. Used only by `make dev`; never in production.
func WithDevRemotes(m map[string]string) DirOption {
	return func(ds *DirSource) { ds.devRemotes = m }
}

// loaded manifest shape (subset of the JSON Schema).
type manifestDoc struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		ID      string `yaml:"id"`
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
	} `yaml:"metadata"`
	Spec struct {
		Capabilities []string `yaml:"capabilities"`
		Permissions  []string `yaml:"permissions"`
		Frontend     *struct {
			RemoteName    string `yaml:"remoteName"`
			ExposedModule string `yaml:"exposedModule"`
			Entry         string `yaml:"entry"`
			Nav           []struct {
				Path    string `yaml:"path"`
				Label   string `yaml:"label"`
				Section string `yaml:"section"`
			} `yaml:"nav"`
		} `yaml:"frontend"`
		// SettingsSchema is a path (relative to the plugin dir) to the JSON Schema for
		// the plugin's settings (J2). Loaded at scan time so the kernel knows the
		// secret (writeOnly) fields and can validate a settings write.
		SettingsSchema string `yaml:"settingsSchema"`
	} `yaml:"spec"`
}

// NewDirSource scans root once at construction. assetPrefix is the URL path the
// asset file server is mounted at (e.g. "/v1alpha1/registry/plugins").
func NewDirSource(root, assetPrefix string, log *slog.Logger, opts ...DirOption) *DirSource {
	ds := &DirSource{root: root, baseURL: strings.TrimRight(assetPrefix, "/"), log: log, assetRoots: map[string]string{}}
	for _, o := range opts {
		o(ds)
	}
	ds.scan()
	return ds
}

func (ds *DirSource) Plugins() []Plugin { return ds.plugins }

func (ds *DirSource) scan() {
	if ds.root == "" {
		return
	}
	entries, err := os.ReadDir(ds.root)
	if err != nil {
		ds.log.Warn("plugin dir not readable; no plugins loaded", "dir", ds.root, "err", err.Error())
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(ds.root, e.Name())
		p, distRoot, err := ds.loadPlugin(dir, e.Name())
		if err != nil {
			ds.log.Warn("skipping invalid plugin", "dir", dir, "err", err.Error())
			continue
		}
		ds.plugins = append(ds.plugins, p)
		// A dev-remote plugin has no local dist to serve (distRoot==""); its assets
		// come from its own dev server, so don't register an asset root for it.
		if distRoot != "" {
			ds.assetRoots[assetKey(p.ID)] = distRoot
		}
		ds.log.Info("registered plugin", "id", p.ID, "version", p.Version, "nav", len(p.Nav))
	}
}

func (ds *DirSource) loadPlugin(dir, dirName string) (Plugin, string, error) {
	manifestPath := filepath.Join(dir, "llmobs-plugin.yaml")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return Plugin{}, "", fmt.Errorf("read manifest: %w", err)
	}
	var m manifestDoc
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return Plugin{}, "", fmt.Errorf("parse manifest: %w", err)
	}
	if m.Kind != "Plugin" || m.Metadata.ID == "" {
		return Plugin{}, "", fmt.Errorf("manifest missing kind/id")
	}
	if m.Spec.Frontend == nil {
		return Plugin{}, "", fmt.Errorf("plugin %s has no frontend surface", m.Metadata.ID)
	}
	key := assetKey(m.Metadata.ID)
	nav := make([]NavEntry, 0, len(m.Spec.Frontend.Nav))
	for _, n := range m.Spec.Frontend.Nav {
		nav = append(nav, NavEntry{Path: n.Path, Label: n.Label, Section: n.Section})
	}
	p := Plugin{
		ID:            m.Metadata.ID,
		Name:          m.Metadata.Name,
		Version:       m.Metadata.Version,
		RemoteName:    m.Spec.Frontend.RemoteName,
		ExposedModule: m.Spec.Frontend.ExposedModule,
		Nav:           nav,
		Capabilities:  m.Spec.Capabilities,
		Permissions:   m.Spec.Permissions,
	}
	// Dev hot-reload (J3): when a live dev-server remoteEntry is configured for this
	// plugin, advertise it directly and DON'T read the built dist (it may not exist —
	// the frontend is served by its own rspack dev server). Integrity is dropped (the
	// dev bundle changes every save). Production leaves devRemotes empty and takes the
	// built-dist path below.
	distRoot := ""
	if url, ok := ds.devRemotes[m.Metadata.ID]; ok && url != "" {
		p.RemoteEntry = url
	} else {
		distRoot = filepath.Join(dir, "dist")
		integrity, err := fileIntegrity(filepath.Join(distRoot, m.Spec.Frontend.Entry))
		if err != nil {
			return Plugin{}, "", fmt.Errorf("hash remote entry: %w", err)
		}
		p.RemoteEntry = ds.baseURL + "/" + key + "/assets/" + m.Spec.Frontend.Entry
		p.Integrity = integrity
	}
	// Load the settings JSON Schema (J2) if declared. The path is manifest-relative
	// and must stay inside the plugin dir (no traversal). A missing/invalid schema
	// fails the plugin load — a declared-but-unreadable schema is a manifest error.
	if sp := m.Spec.SettingsSchema; sp != "" {
		schemaPath := filepath.Join(dir, filepath.Clean("/"+sp))
		if !strings.HasPrefix(schemaPath, filepath.Clean(dir)+string(filepath.Separator)) {
			return Plugin{}, "", fmt.Errorf("settingsSchema path escapes plugin dir: %s", sp)
		}
		schemaRaw, err := os.ReadFile(schemaPath)
		if err != nil {
			return Plugin{}, "", fmt.Errorf("read settingsSchema %s: %w", sp, err)
		}
		if !json.Valid(schemaRaw) {
			return Plugin{}, "", fmt.Errorf("settingsSchema %s is not valid JSON", sp)
		}
		p.SettingsSchema = json.RawMessage(schemaRaw)
	}
	return p, distRoot, nil
}

// AssetHandler serves each plugin's dist directory under
// <assetPrefix>/<key>/assets/*. Static files only; no directory listing.
func (ds *DirSource) AssetHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path: <prefix>/<key>/assets/<file...>
		rest := strings.TrimPrefix(r.URL.Path, ds.baseURL+"/")
		key, after, ok := strings.Cut(rest, "/assets/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		root, ok := ds.assetRoots[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// Prevent path traversal.
		clean := filepath.Clean("/" + after)
		full := filepath.Join(root, clean)
		if !strings.HasPrefix(full, filepath.Clean(root)+string(os.PathSeparator)) && full != filepath.Clean(root) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, full)
	})
}

// assetKey turns a namespaced id (owner/name) into a single URL path segment.
func assetKey(id string) string {
	return strings.ReplaceAll(id, "/", "__")
}

// fileIntegrity returns an SRI-style sha384 hash of a file's bytes.
func fileIntegrity(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha512.Sum384(b)
	return "sha384-" + base64.StdEncoding.EncodeToString(sum[:]), nil
}
