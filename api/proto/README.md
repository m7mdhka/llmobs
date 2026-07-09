# proto/ — reserved

Reserved for Protocol Buffers definitions **if** gRPC is ever added as an
internal transport. It is intentionally empty today.

The native ingestion format is OTLP (which has its own protobufs upstream); the
public plugin and Query APIs are HTTP/OpenAPI. Introducing gRPC here would be an
architectural decision requiring an ADR.
