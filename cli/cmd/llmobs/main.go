// Command llmobs is the plugin-author CLI. H8 ships `plugin create` — the scaffold
// step of the DX loop (the rest of the CLI — init/dev/apply/render/bundle/backup —
// is tracked separately). Stdlib-only; no third-party dependency.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/m7mdhka/llmobs/cli/internal/scaffold"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "plugin":
		pluginCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `llmobs — plugin-author CLI

Usage:
  llmobs plugin create <owner/name> [--name "Display Name"] [--dir .]

Commands:
  plugin create   Scaffold a new Python plugin from the built-in template.
`)
}

func pluginCmd(args []string) {
	// Shape: plugin create <owner/name> [flags]. The id comes first; flags follow
	// (stdlib flag parsing stops at the first positional, so the id must lead).
	if len(args) < 2 || args[0] != "create" {
		fmt.Fprintln(os.Stderr, "usage: llmobs plugin create <owner/name> [--name ...] [--dir .]")
		os.Exit(2)
	}
	id := args[1]
	fs := flag.NewFlagSet("plugin create", flag.ExitOnError)
	name := fs.String("name", "", "human display name (default: the plugin name)")
	dir := fs.String("dir", ".", "parent directory to create the plugin in")
	_ = fs.Parse(args[2:])
	created, err := scaffold.Create(id, *name, *dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Created plugin %s at %s\n", id, created)
	fmt.Printf("Next:\n  cd %s\n  (declare capabilities in llmobs-plugin.yaml, implement backend/app.py)\n  python3 backend/interop_test.py   # offline contract check\n", created)
}
