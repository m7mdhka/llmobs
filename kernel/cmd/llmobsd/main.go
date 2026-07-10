// Command llmobsd is the LLMObs kernel daemon (lite profile). It runs OTLP
// ingestion, the middleware chain, storage, and the Query API in one process.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/m7mdhka/llmobs/kernel/internal/bus"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/executors"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/supervisor"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/ingest"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/ingesthealth"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/normalize"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/pipeline"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/query"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/redact"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/authhttp"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/frontendtoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginapi"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginproxy"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/registry"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/webui"
	"github.com/m7mdhka/llmobs/kernel/internal/jobs"
	"github.com/m7mdhka/llmobs/kernel/internal/platform"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/secretbox"
	"github.com/m7mdhka/llmobs/kernel/internal/pluginsettings"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
	"github.com/m7mdhka/llmobs/kernel/pkg/brand"
)

func main() {
	if err := run(); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	log := platform.NewLogger(cfg)
	log.Info(brand.Name+" kernel starting", "otlp_http", cfg.OTLPHTTPAddr, "api", cfg.APIAddr)

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := platform.NewPGPool(rootCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// wait for the database to be reachable (compose start ordering)
	if err := waitForDB(rootCtx, pool, log); err != nil {
		return err
	}

	if cfg.MigrateOnBoot {
		if err := postgres.Migrate(rootCtx, pool); err != nil {
			return err
		}
		log.Info("migrations applied")
	}

	project, key, created, err := controlplane.Bootstrap(rootCtx, pool, cfg.BootstrapProject, cfg.BootstrapAPIKey)
	if err != nil {
		return err
	}
	if created {
		// print the key exactly once
		log.Warn("bootstrap created a default api key — store it now, it is not shown again",
			"project", project, "api_key", key)
	}

	adminCreated, err := controlplane.BootstrapAdmin(rootCtx, pool, cfg.BootstrapAdminEml, cfg.BootstrapAdminPwd)
	if err != nil {
		return err
	}
	if adminCreated {
		log.Warn("bootstrap created the admin user — sign in with the configured password",
			"email", cfg.BootstrapAdminEml)
	}

	// Metrics registry, shared across pipeline + query. Pool stats are sampled at
	// scrape time. Labels are project_id only — never trace/user ids (cardinality).
	mreg := metrics.New()
	mreg.SampledGauge("llmobs_db_pool_total_conns", "Total DB pool connections.", func() float64 { return float64(pool.Stat().TotalConns()) })
	mreg.SampledGauge("llmobs_db_pool_idle_conns", "Idle DB pool connections.", func() float64 { return float64(pool.Stat().IdleConns()) })
	mreg.SampledGauge("llmobs_db_pool_acquired_conns", "Acquired DB pool connections.", func() float64 { return float64(pool.Stat().AcquiredConns()) })

	// Persist-health signal (G2): the persist stage folds each outcome into it; the
	// receivers read it for backpressure and /readyz reads it for readiness, so all
	// three agree on "can storage accept writes right now?".
	persistHealth := ingesthealth.New(cfg.PersistUnhealthyThreshold)
	mreg.SampledGauge("llmobs_persist_healthy", "1 when the storage adapter is accepting writes, else 0.",
		func() float64 {
			if persistHealth.Healthy() {
				return 1
			}
			return 0
		})

	store := postgres.NewStore(pool)
	if ttl, terr := time.ParseDuration(cfg.ErasureSuppressionTTL); terr == nil {
		store.SetErasureSuppressionTTL(ttl) // G3 tombstone retention
	}
	// Durable event bus (H6): Postgres-backed for lite (no new infra). Publishes
	// span.ingested from the pipeline; plugins subscribe via poll/ack.
	eventBus := bus.New(postgres.NewEventStore(pool), int64(cfg.EventBacklogCap))
	reg := normalize.Default()
	skew, _ := time.ParseDuration(cfg.ClockSkewThreshold)
	presets, customRules := parseRedactConfig(cfg.RedactPresets, cfg.RedactCustomJSON)
	pipe := pipeline.New(pool, store, reg, eventBus, pipeline.Config{
		Metrics: mreg, SkewThreshold: skew, RedactPresets: presets, RedactCustom: customRules,
		Signal: persistHealth,
	})

	receiver := ingest.NewReceiver(pipe, log, cfg.IngestQueueSize, 4, mreg)
	receiver.SetPersistSignal(persistHealth) // shed 503/UNAVAILABLE when persistence is unhealthy
	receiver.Start(rootCtx)
	// Ingest queue occupancy is scrape-sampled so an operator can see saturation
	// (the A1/A3 diagnosis) approaching the high-water backpressure mark.
	mreg.SampledGauge("llmobs_ingest_queue_depth", "In-process ingest queue depth (acked, not yet persisted).",
		func() float64 { return float64(receiver.QueueLen()) })
	mreg.SampledGauge("llmobs_ingest_queue_capacity", "In-process ingest queue capacity.",
		func() float64 { return float64(receiver.QueueCap()) })

	// The kernel's Ed25519 plugin signing key (in-memory for lite, ADR-0023): mints
	// + verifies service tokens (supervisor) and identity assertions (proxy), and
	// verifies both when a plugin calls the Query API (the computed intersection).
	pluginSigner, err := plugintoken.NewSigner()
	if err != nil {
		return err
	}

	maxWindow, _ := time.ParseDuration(cfg.QueryMaxWindow)
	qsrv := query.NewServer(store, pool, log, maxWindow, mreg, pluginSigner)

	// Plugin supervisor (H2): discovers backend plugins (spec.backend) from the
	// plugin dir, handshakes + health-probes them via the external-URL executor,
	// and issues service tokens. Only the leader replica supervises (advisory lock,
	// below).
	sup := supervisor.New(supervisor.NewDirProvider(cfg.PluginDir, log),
		executors.NewExternalURL(5*time.Second), pluginSigner, mreg, log, supervisor.Config{}, nil)

	// Job scheduler (H6b): Postgres-backed, runs on the supervisor's leader cadence.
	// The runner presents a system assertion scoped to the plugin's OWN grant on the
	// default project — never more (pin 1); runs are audited in plugin_job_runs.
	defaultProject, _ := controlplane.DefaultProjectID(rootCtx, pool)
	scheduler := jobs.New(sup, jobs.NewRunner(pluginSigner, 30*time.Second, 3),
		postgres.NewJobStore(pool), defaultProject, log, nil)

	// API server: auth + query + health, all behind the session middleware so a
	// resolved session is available to every downstream handler.
	auth := authhttp.New(pool, log, cfg.CookieSecure)
	apiMux := http.NewServeMux()
	platform.NewHealth(pool, persistHealth).Register(apiMux)
	auth.Register(apiMux)
	auth.RegisterKeys(apiMux)
	var regSource registry.Source = registry.EmptySource{}
	if cfg.PluginDir != "" {
		var opts []registry.DirOption
		if dev := parseDevRemotes(cfg.DevPluginRemotes); len(dev) > 0 {
			log.Warn("dev plugin remotes active — advertising live dev-server URLs (not for production)", "remotes", dev)
			opts = append(opts, registry.WithDevRemotes(dev))
		}
		regSource = registry.NewDirSource(cfg.PluginDir, "/v1alpha1/registry/plugins", log, opts...)
	}
	registry.NewHandler(regSource).Register(apiMux)
	// Supervisor ops API: snapshot readable by any authenticated caller; disable/
	// enable gated to sessions (admin today; RBAC beyond admin is issue #21). The
	// more-specific prefix wins over the /v1alpha1/ query catch-all below.
	apiMux.Handle("/v1alpha1/supervisor/", sup.Handler("/v1alpha1/supervisor", func(r *http.Request) bool {
		_, ok := authhttp.SessionFrom(r.Context())
		return ok
	}))
	// Jobs ops API: on-demand trigger + run status, actor from the admin session.
	jobActor := func(r *http.Request) (string, bool) {
		if sess, ok := authhttp.SessionFrom(r.Context()); ok {
			return "session:" + sess.User.Email, true
		}
		return "", false
	}
	jobs.NewHandler(scheduler, jobActor).Register(apiMux, "/v1alpha1/jobs")
	// Plugin backend proxy (H3): /api/plugins/{id}/* → the running plugin backend,
	// cookie stripped + identity assertion injected. Only running plugins proxy.
	projectResolver := func(r *http.Request) (string, error) {
		if p := r.Header.Get("X-LLMObs-Project"); p != "" {
			return p, nil
		}
		return controlplane.DefaultProjectID(r.Context(), pool)
	}
	apiMux.Handle("/api/plugins/", pluginproxy.New(sup, pluginSigner, projectResolver, log).Handler("/api/plugins/"))
	// Frontend-token mint (J1): session-authed; the shell mints a per-plugin,
	// short-TTL, audience-bound frontend token scoped to plugin-grant ∩ session ∩
	// project. Least-privilege-by-default for cooperating frontends, NOT a boundary
	// against a hostile one (same-origin — see frontendtoken.Handler).
	apiMux.HandleFunc("/v1alpha1/plugin-frontend-token",
		frontendtoken.New(pluginSigner, regSource, projectResolver).Mint)
	// Plugin data primitives (H4+): a plugin backend reaches these with its double
	// token; each is capability-gated and tenant-scoped from the assertion.
	pluginAuthz := pluginauth.New(pluginSigner, nil)
	// Publish the kernel public key so a plugin backend (any language) can verify
	// kernel-signed assertions (H7 finding #2). Unauthenticated — the key is public.
	pluginapi.NewKernelKey(pluginSigner.Public()).Register(apiMux, "/v1alpha1/plugin/kernel-key")
	pluginapi.NewKV(pluginAuthz, postgres.NewPluginKV(pool)).Register(apiMux, "/v1alpha1/plugin/kv")
	// Secrets: envelope-encrypted with the kernel master key (in-memory for lite,
	// ADR-0023). The plaintext is never persisted/logged and returned only to the
	// owning plugin's authenticated delivery.
	secretBox, err := secretbox.NewRandom()
	if err != nil {
		return err
	}
	pluginapi.NewSecrets(pluginAuthz, postgres.NewPluginSecrets(pool), secretBox, log).Register(apiMux, "/v1alpha1/plugin/secrets")
	// Store: plugin-owned structured collections. The supervisor provisions each
	// plugin's collections during starting (migration = health signal, H5).
	pluginStore := postgres.NewPluginStore(pool)
	sup.SetProvisioner(pluginStore)
	pluginapi.NewStore(pluginAuthz, pluginStore).Register(apiMux, "/v1alpha1/plugin/store")
	// Settings (J2, ADR-0024): `kv` made frontend-reachable + schema-aware. Authed by
	// the J1 frontend token (a pure-frontend plugin's only credential); writeOnly
	// (secret) fields are envelope-encrypted with the same box as secrets and never
	// returned. The schema comes from the registry (loaded from the manifest).
	settingsStore := pluginsettings.NewStore(postgres.NewPluginKV(pool), secretBox)
	settingsSchema := func(pluginID string) (json.RawMessage, bool) { return registry.SchemaFor(regSource, pluginID) }
	// Settings WRITES require configuration authority (admins today, #21 seam): the
	// session role must carry a write scope. The frontend token already bounded the
	// plugin + tenant; this is the "who may administer" half so a viewer cannot
	// overwrite project-shared config/secrets.
	canWriteSettings := func(r *http.Request) bool {
		sess, ok := authhttp.SessionFrom(r.Context())
		return ok && perm.HasWriteAuthority(perm.RoleScopes(sess.User.Role))
	}
	pluginapi.NewSettings(pluginAuthz, settingsStore, settingsSchema, canWriteSettings).Register(apiMux, "/v1alpha1/plugin/settings")
	// Events: durable subscribe (poll/ack) over the Postgres event bus.
	pluginapi.NewEvents(pluginAuthz, eventBus).Register(apiMux, "/v1alpha1/plugin/events")
	// Ingest: a compat plugin (cap:ingest) pushes OTLP spans through the SAME
	// pipeline as native OTLP — kernel-stamped source, project from the assertion.
	pluginapi.NewIngest(pluginAuthz, pipe, defaultProject).Register(apiMux, "/v1alpha1/plugin/ingest")
	apiMux.HandleFunc("/v1alpha1/whoami", qsrv.Whoami)
	apiMux.Handle("/v1alpha1/", qsrv.Handler())
	// The web shell (static SPA) is served at the origin root unless the kernel is
	// run headless (LLMOBS_SERVE_SHELL=false) — then / is API-only, for Kenji's
	// own-UI deployment. More-specific API prefixes above always win.
	if cfg.ServeShell {
		apiMux.Handle("/", webui.Handler(cfg.WebUIDir))
	}
	apiServer := &http.Server{Addr: cfg.APIAddr, Handler: auth.Middleware(apiMux), ReadHeaderTimeout: 5 * time.Second}

	// Metrics: served on a SEPARATE bind (default :9090) so the scrape surface is
	// network-isolated from the public API — an operator exposes it only to
	// Prometheus, not to tenants (see the report's scrape-auth decision). If
	// LLMOBS_METRICS_ADDR is empty, /metrics is mounted on the API server instead.
	var metricsServer *http.Server
	if cfg.MetricsAddr != "" {
		mmux := http.NewServeMux()
		mmux.Handle("/metrics", mreg.Handler())
		metricsServer = &http.Server{Addr: cfg.MetricsAddr, Handler: mmux, ReadHeaderTimeout: 5 * time.Second}
	} else {
		apiMux.Handle("/metrics", mreg.Handler())
	}

	// OTLP HTTP receiver server.
	otlpServer := &http.Server{Addr: cfg.OTLPHTTPAddr, Handler: receiver.Handler(), ReadHeaderTimeout: 5 * time.Second}

	// OTLP gRPC receiver server (4317).
	grpcServer := grpc.NewServer()
	receiver.RegisterGRPC(grpcServer)
	grpcLis, err := net.Listen("tcp", cfg.OTLPGRPCAddr)
	if err != nil {
		return err
	}

	errCh := make(chan error, 4)
	go serve(apiServer, log, "api", errCh)
	go serve(otlpServer, log, "otlp-http", errCh)
	if metricsServer != nil {
		go serve(metricsServer, log, "metrics", errCh)
	}
	go func() {
		log.Info("otlp-grpc listening", "addr", cfg.OTLPGRPCAddr)
		if err := grpcServer.Serve(grpcLis); err != nil {
			errCh <- err
		}
	}()

	// Plugin supervisor loop, gated by leadership so only one replica supervises
	// (the pg advisory-lock pattern the migration runner proves). A dedicated conn
	// holds the lock for the process lifetime; releasing on shutdown frees it.
	reconcileEvery, _ := time.ParseDuration(cfg.PluginReconcileInterval)
	if reconcileEvery <= 0 {
		reconcileEvery = 15 * time.Second
	}
	go func() {
		const supervisorLockKey int64 = 0x6c6c6d6f627332 // "llmobs2"
		conn, err := pool.Acquire(rootCtx)
		if err != nil {
			log.Warn("supervisor: cannot acquire leader connection; not supervising", "err", err.Error())
			return
		}
		defer conn.Release()
		var leader bool
		if err := conn.QueryRow(rootCtx, "SELECT pg_try_advisory_lock($1)", supervisorLockKey).Scan(&leader); err != nil {
			log.Warn("supervisor: leader election failed; not supervising", "err", err.Error())
			return
		}
		if !leader {
			log.Info("supervisor: another replica is leader; standing by")
			return
		}
		log.Info("supervisor: acquired leadership", "interval", reconcileEvery.String())
		defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", supervisorLockKey) }()
		t := time.NewTicker(reconcileEvery)
		defer t.Stop()
		sup.Reconcile(rootCtx)
		scheduler.Tick(rootCtx)
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-t.C:
				sup.Reconcile(rootCtx)
				scheduler.Tick(rootCtx) // dispatch due jobs on the same leader cadence
			}
		}
	}()

	select {
	case <-rootCtx.Done():
		log.Info("shutdown signal received; draining ingest queue")
	case err := <-errCh:
		log.Error("server error", "err", err.Error())
	}

	// Ordered shutdown (G1): (1) shed brand-new OTLP immediately with a retryable
	// 503/UNAVAILABLE, (2) stop the listeners so no new work is enqueued, then
	// (3) DRAIN the acked-but-unpersisted queue within a bounded deadline. The
	// deadline (LLMOBS_SHUTDOWN_DRAIN_TIMEOUT) is shorter than K8s
	// terminationGracePeriodSeconds so the drain finishes before SIGKILL; whatever
	// cannot drain in time is counted, not dropped silently.
	receiver.BeginDrain()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = apiServer.Shutdown(shutdownCtx)
	_ = otlpServer.Shutdown(shutdownCtx)
	if metricsServer != nil {
		_ = metricsServer.Shutdown(shutdownCtx)
	}
	grpcServer.GracefulStop()

	drainTimeout, err := time.ParseDuration(cfg.ShutdownDrainTimeout)
	if err != nil || drainTimeout <= 0 {
		drainTimeout = 20 * time.Second
	}
	drainCtx, drainCancel := context.WithTimeout(context.Background(), drainTimeout)
	defer drainCancel()
	receiver.DrainAndWait(drainCtx)
	log.Info("stopped")
	return nil
}

// parseRedactConfig turns the CSV preset list ("none" disables) and the JSON
// custom-rule array into the pipeline's redaction config.
func parseRedactConfig(presetsCSV, customJSON string) ([]string, []redact.CustomRule) {
	var presets []string
	if p := strings.TrimSpace(presetsCSV); p != "" && p != "none" {
		for _, name := range strings.Split(p, ",") {
			if n := strings.TrimSpace(name); n != "" {
				presets = append(presets, n)
			}
		}
	}
	var custom []redact.CustomRule
	if strings.TrimSpace(customJSON) != "" {
		_ = json.Unmarshal([]byte(customJSON), &custom)
	}
	return presets, custom
}

// parseDevRemotes parses the J3 `make dev` override "id=url,id=url" into a map of
// plugin id -> live dev-server remoteEntry URL. Malformed entries are skipped.
func parseDevRemotes(spec string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, url, ok := strings.Cut(part, "=")
		id, url = strings.TrimSpace(id), strings.TrimSpace(url)
		if ok && id != "" && url != "" {
			out[id] = url
		}
	}
	return out
}

func serve(s *http.Server, log *slog.Logger, name string, errCh chan<- error) {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- err
	}
	_ = log
	_ = name
}

func waitForDB(ctx context.Context, pool interface {
	Ping(context.Context) error
}, log *slog.Logger) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		if err := pool.Ping(ctx); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("database not reachable within 60s")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
			log.Info("waiting for database")
		}
	}
}
