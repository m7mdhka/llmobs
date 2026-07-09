# cli/

The `llmobs` command — a separate, thin, fast Go module
(`github.com/m7mdhka/llmobs/cli`) so it builds and ships independently of the
kernel.

Commands (implemented under `internal/`): `init`, `add`, `dev`, `apply`,
`render`, `bundle`, `backup`, `plugin`.

- `init` — scaffold a deployment.
- `add` — add a plugin to a deployment.
- `dev` — run the lite profile locally.
- `apply` / `render` — reconcile / render supervisor manifests (works with the
  executors in `kernel/internal/controlplane/executors`).
- `bundle` — build offline/air-gap bundles.
- `backup` — backup/restore.
- `plugin` — author-side commands, incl. `plugin create` (scaffolds from
  `templates/`).

Like everything else, the CLI may not import `kernel/internal/...`.
