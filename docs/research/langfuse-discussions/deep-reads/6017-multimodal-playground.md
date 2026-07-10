# #6017 — "Multi-modal input in playground"

- URL: https://github.com/orgs/langfuse/discussions/6017
- Category: Ideas · Votes (2026-07-10): **71** (task estimate ~69; actual higher) · Comments: 19

**Demand (one paragraph).** The Langfuse playground accepts text only; users want
to send **images, PDF, audio, and video** as input. This is a top-10 corpus item
and clusters with a family of multimodal requests (#3004 base64 content 64v,
#4268 image messages in playground 32v, #3074 image_url in prompt types 27v,
#6883 media rendering in dataset items 29v, #4852 OpenAI image-gen observation
25v). Explicitly a blocker for adoption ("not having this feature is a blocker" —
a contributor volunteering to implement it, up=4; "text-only is very limiting").

**Owning surface: first-party plugin (playground/prompt-experiment plugin), with
a kernel-adjacent data-model dependency.** The playground itself is a plugin
surface. But multimodal *content* touches the canonical model and blob storage:
ADR-0018 (span taxonomy / payload shapes) and the `secrets`/blob path must
represent media parts (base64 or mediaId reference) so traces of multimodal calls
round-trip. So: **the playground UX is plugin-owned; the multimodal content
representation is a canonical-model concern (needs-contract-evolution).** Reasoning:
rendering a file picker is plugin work, but "how a message-with-an-image is stored
canonically and referenced via blob storage" is a shared contract every plugin
consumes — that part evolves `api/` (payload shapes) + the blob primitive, not a
single plugin. Positioning: our OTLP-canonical + blob-adapter design already
separates large payloads from the trace row, so multimodal content is a
model-extension, not a re-architecture.
