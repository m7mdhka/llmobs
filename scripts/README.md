# scripts/

Repo **maintenance** scripts only — not build logic. Build/test/dev logic lives
behind `make` targets (which delegate to `go`, `turbo`, `helm`, etc.); if you
find yourself adding build logic here, add a `make` target instead.

Examples of what belongs here: the license-header insert/check used by
pre-commit, one-off maintenance and migration helpers, and release housekeeping
that isn't a first-class `make` verb.
