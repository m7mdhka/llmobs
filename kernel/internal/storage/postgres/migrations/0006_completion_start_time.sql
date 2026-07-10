-- completion_start_time (PR-E3, audit item 14 / Marco): the promoted generation
-- field (02-span.md §5) that anchors time-to-first-token. Nullable; set by the
-- normalizer when the source provides it. `duration` and `ttft` (DSL §4.2) are
-- computed at query time from this + start_time/end_time — no columns of their own.
ALTER TABLE spans
    ADD COLUMN IF NOT EXISTS completion_start_time TIMESTAMPTZ;
