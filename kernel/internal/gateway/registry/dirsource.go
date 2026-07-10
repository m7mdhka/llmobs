package registry

import (
	"crypto/sha512"
	"encoding/base64"
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
	} `yaml:"spec"`
}

// NewDirSource scans root once at construction. assetPrefix is the URL path the
// asset file server is mounted at (e.g. "/v1alpha1/registry/plugins").
func NewDirSource(root, assetPrefix string, log *slog.Logger) *DirSource {
	ds := &DirSource{root: root, baseURL: strings.TrimRight(assetPrefix, "/"), log: log, assetRoots: map[string]string{}}
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
		ds.assetRoots[assetKey(p.ID)] = distRoot
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
	distRoot := filepath.Join(dir, "dist")
	entryFile := filepath.Join(distRoot, m.Spec.Frontend.Entry)
	integrity, err := fileIntegrity(entryFile)
	if err != nil {
		return Plugin{}, "", fmt.Errorf("hash remote entry: %w", err)
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
		RemoteEntry:   ds.baseURL + "/" + key + "/assets/" + m.Spec.Frontend.Entry,
		RemoteName:    m.Spec.Frontend.RemoteName,
		ExposedModule: m.Spec.Frontend.ExposedModule,
		Integrity:     integrity,
		Nav:           nav,
		Capabilities:  m.Spec.Capabilities,
		Permissions:   m.Spec.Permissions,
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
