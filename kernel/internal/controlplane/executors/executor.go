// Package executors implements supervisor executors — the mechanism by which the
// kernel reaches and probes a plugin backend. Arc H ships the external-URL
// executor only (ADR-0023/R2): the operator runs the plugin service however they
// like and registers it by URL; the executor drives the handshake and health
// probes over HTTP. Compose/operator/GitOps executors are deferred (issues). The
// kernel never touches the Docker/K8s socket — the deployability invariant.
//
// Executors are about *reaching* a plugin (reachability + reported state); token
// issuance and lifecycle decisions belong to the supervisor, which holds the
// signing key. This keeps future executors free of crypto.
package executors

import (
	"context"

	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// Backend is the reachability part of a plugin's manifest spec.backend.
type Backend struct {
	URL        string // base URL, e.g. http://langfuse-compat:8080
	InfoPath   string // handshake path; defaults to pluginproto.DefaultInfoPath
	HealthPath string // health path; defaults to pluginproto.DefaultHealthPath
}

// Executor reaches a plugin backend to handshake, probe health, and deliver the
// service token (kernel-initiated push, H7c).
type Executor interface {
	Name() string
	Handshake(ctx context.Context, b Backend) (pluginproto.Info, error)
	Health(ctx context.Context, b Backend) (pluginproto.Health, error)
	// DeliverToken pushes the short-TTL service token to the plugin's registered
	// URL. Kernel-initiated only — there is no plugin-pull counterpart.
	DeliverToken(ctx context.Context, b Backend, token string, expiresUnix int64) error
}
