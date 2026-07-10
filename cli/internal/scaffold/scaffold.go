// Package scaffold generates a new plugin from an embedded template — the
// `llmobs plugin create` DX loop (H8). The template is born-identical to
// templates/plugin-python (a working plugin), so create -> run is the shortest
// path from zero, closing the B10 "no scaffold, cries and quits" gap.
package scaffold

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

//go:embed templates
var templates embed.FS

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9-]*$`)

// Vars are the substitutions applied to *.tmpl files.
type Vars struct {
	ID   string // owner/name
	Name string // display name
	Slug string // name with '/' -> '-', for compose service names
}

// Create scaffolds a python plugin for id into destDir/<name>. Returns the created
// directory. destDir "" means the current directory.
func Create(id, displayName, destDir string) (string, error) {
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("plugin id must be 'owner/name' (lowercase, hyphens): got %q", id)
	}
	name := id[strings.IndexByte(id, '/')+1:]
	if displayName == "" {
		displayName = name
	}
	vars := Vars{ID: id, Name: displayName, Slug: strings.ReplaceAll(id, "/", "-")}

	root := filepath.Join(destDir, name)
	if _, err := os.Stat(root); err == nil {
		return "", fmt.Errorf("%s already exists — refusing to overwrite", root)
	}

	srcRoot := "templates/plugin-python"
	err := fs.WalkDir(templates, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcRoot, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(root, rel), 0o755)
		}
		data, err := templates.ReadFile(p)
		if err != nil {
			return err
		}
		out := filepath.Join(root, strings.TrimSuffix(rel, ".tmpl"))
		if strings.HasSuffix(p, ".tmpl") {
			t, err := template.New(rel).Parse(string(data))
			if err != nil {
				return fmt.Errorf("parse template %s: %w", rel, err)
			}
			f, err := os.Create(out)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			return t.Execute(f, vars)
		}
		return os.WriteFile(out, data, 0o644)
	})
	if err != nil {
		return "", err
	}
	return root, nil
}
