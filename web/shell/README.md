# web/shell

The Module Federation 2.0 **host app** (Rspack). The shell is the frame every
plugin frontend mounts into. It owns:

- Navigation and app chrome.
- Auth UI (session termination happens at the gateway; the shell drives login).
- The plugin loader — loads SHA-pinned, kernel-served MF remotes at runtime.
- The settings renderer (`packages/schema-form` — JSON Schema → settings UI).
- Ops / admin pages.

React, the SDK, the design system, and tokens are **shared singletons** across
the shell and plugins — never bundle a second copy. Colors/spacing/typography
come from `@llmobs/tokens`; components from `@llmobs/ui`. See
[`.claude/rules/frontend.md`](../../.claude/rules/frontend.md).
