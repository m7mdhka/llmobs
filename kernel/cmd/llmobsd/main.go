// Command llmobsd is the LLMObs kernel daemon (lite profile). It runs OTLP
// ingestion, the middleware chain, storage, and the Query API in one process.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"google.golang.org/grpc"

	"github.com/m7mdhka/llmobs/kernel/internal/bus"
	"github.com/m7mdhka/llmobs/kernel/internal/bus/redisstore"
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
	"github.com/m7mdhka/llmobs/kernel/internal/reprice"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/backfill"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/clickhouse"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/dualstore"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
	"github.com/m7mdhka/llmobs/kernel/pkg/brand"
)

// The dual-read resurrection guard (dualstore.EraseSpans) requires the lite store to
// resolve erase-candidate ids and the scale store to suppress explicit ids. Assert the
// real adapters satisfy those capabilities at compile time, so a regression that drops
// either method fails the build rather than silently re-opening the erasure gap.
var (
	_ dualstore.EraseIDResolver = (*postgres.Store)(nil)
	_ dualstore.EraseSuppressor = (*clickhouse.Store)(nil)
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

	// Price table (ADR-0029, Arc M): seed default per-token prices so cost derivation
	// works out of the box. Idempotent (ON CONFLICT DO NOTHING) — an operator's edits
	// (new versions) are never clobbered by a re-seed on the next boot.
	priceStore := postgres.NewPriceStore(pool)
	if err := priceStore.SeedDefaults(rootCtx); err != nil {
		return err
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
	if qt, terr := time.ParseDuration(cfg.QueryStmtTimeout); terr == nil {
		store.SetQueryTimeout(qt) // K1.5: server-side statement_timeout backstop for DSL reads
	}

	// Scale dual-read (ADR-0026 RULING-MIG6). When a ClickHouse-scale store is
	// configured, the kernel runs the PERMANENT dual-read layer: writeStore becomes
	// the dual decorator (writes → scale with seed-on-migrate), and the query server
	// unifies lite∪scale on every read. This is the boundary crossing that never
	// strands a self-hoster — no read window where a just-written span is missing.
	// Empty ClickHouseURL => single-store lite (unchanged).
	var writeStore storage.TelemetryStore = store
	// Re-pricing (M4) scans the store the spans LIVE in. In lite that is Postgres; in a
	// dual (scale) deployment a run must cover BOTH tiers, so the scale ClickHouse store
	// is appended below. The run state is always Postgres (control-plane).
	repriceScanners := []reprice.NamedScanner{{Name: "lite", Scanner: store}}
	var dual *dualstore.Store
	if cfg.ClickHouseURL != "" {
		scale, closeScale, cErr := buildScaleStore(rootCtx, cfg, log)
		if cErr != nil {
			log.Error("scale (ClickHouse) store init failed", "err", cErr.Error())
			os.Exit(1)
		}
		defer closeScale()
		dual = dualstore.New(store, scale)
		writeStore = dual
		repriceScanners = append(repriceScanners, reprice.NamedScanner{Name: "scale", Scanner: scale})
		log.Info("scale dual-read enabled (Postgres-lite ∪ ClickHouse-scale)")

		// Optional lite→scale backfill (L5). Convenience-only: dual-read already makes
		// lite data readable, so this just moves cold rows onto scale in the background.
		// Runs on its OWN generous execution budget (NOT the interactive read timeout),
		// resumable across restarts, and fully DECOUPLED from boot readiness — /readyz
		// never waits on it and a partial backfill is correct via dual-read.
		if cfg.BackfillOnBoot {
			budget, _ := time.ParseDuration(cfg.BackfillBudget)
			runner := backfill.New(store, scale, backfill.Config{
				ChunkSize: cfg.BackfillChunkSize, Budget: budget,
			}, log)
			go func() {
				// #90: single-flight across replicas. Without a guard, EVERY replica with
				// BackfillOnBoot runs the backfill concurrently — racing the shared
				// (ts,project_id,id) cursor and hammering ClickHouse with N× the load. A
				// dedicated advisory-locked connection (the same pg_try_advisory_lock
				// pattern the supervisor uses, a DISTINCT key) makes exactly one replica
				// run it; the rest skip. A skipped replica loses nothing — dual-read already
				// serves the data, so the backfill is pure housekeeping. The lock is held
				// for the run's duration and released when it finishes.
				const backfillLockKey int64 = 0x6c6c6d6f627366 // "llmobsf" — distinct from the supervisor lock
				conn, err := pool.Acquire(rootCtx)
				if err != nil {
					log.Warn("backfill: cannot acquire leader connection; skipping this replica", "err", err.Error())
					return
				}
				defer conn.Release()
				var leader bool
				if err := conn.QueryRow(rootCtx, "SELECT pg_try_advisory_lock($1)", backfillLockKey).Scan(&leader); err != nil {
					log.Warn("backfill: leader election failed; skipping this replica", "err", err.Error())
					return
				}
				if !leader {
					log.Info("backfill: another replica holds the backfill lock; skipping (dual-read still serves the data)")
					return
				}
				defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", backfillLockKey) }()
				log.Info("lite→scale backfill started (background, resumable, single-flight leader)")
				res, berr := runner.Run(rootCtx)
				if berr != nil {
					log.Error("lite→scale backfill stopped with error (resumable on next boot)", "err", berr.Error())
					return
				}
				log.Info("lite→scale backfill run finished",
					"spans_migrated", res.SpansMigrated, "scores_migrated", res.ScoresMigrated,
					"dead_lettered", res.DeadLettered, "complete", res.Complete)
			}()
		}
	}
	// Durable event bus (H6): Postgres-backed for lite (no new infra); the scale
	// profile uses the Redis/Valkey Streams backend when configured (ADR-0028). The
	// shared bus.Bus (replay/at-least-once/backlog-cap/DLQ) is identical either way —
	// only the Store differs. Publishes span.ingested; plugins subscribe via poll/ack.
	var eventStore bus.Store = postgres.NewEventStore(pool)
	if cfg.EventRedisURL != "" || len(cfg.EventRedisSentinelAddrs) > 0 {
		rdb, rerr := redisstore.Dial(rootCtx, redisstore.Config{
			URL:              cfg.EventRedisURL,
			MasterName:       cfg.EventRedisMasterName,
			SentinelAddrs:    cfg.EventRedisSentinelAddrs,
			Password:         cfg.EventRedisPassword,
			SentinelPassword: cfg.EventRedisSentinelPassword,
			Username:         cfg.EventRedisUsername,     // #126 ACL/IAM user
			PasswordFile:     cfg.EventRedisPasswordFile, // #126 rotating token file
		})
		if rerr != nil {
			log.Error("scale event bus (Redis/Valkey) init failed", "err", rerr.Error())
			os.Exit(1)
		}
		defer func() { _ = rdb.Close() }()
		rstore := redisstore.New(rdb, strings.ToLower(brand.Name))
		// #98: bound each stream to the delivery window (the backlog cap). Nothing older
		// than backlogCap behind latest is deliverable (the bus dead-letters it), so the
		// approximate trim never drops deliverable backlog.
		rstore.SetStreamMaxLen(int64(cfg.EventBacklogCap))
		eventStore = rstore
		log.Info("scale event bus enabled (Redis/Valkey Streams)", "stream_max_len", cfg.EventBacklogCap)
	}
	eventBus := bus.New(eventStore, int64(cfg.EventBacklogCap))
	reg := normalize.Default()
	skew, _ := time.ParseDuration(cfg.ClockSkewThreshold)
	presets, customRules := parseRedactConfig(cfg.RedactPresets, cfg.RedactCustomJSON)
	pipe := pipeline.New(pool, writeStore, reg, eventBus, pipeline.Config{
		Metrics: mreg, SkewThreshold: skew, RedactPresets: presets, RedactCustom: customRules,
		Signal: persistHealth,
		// Cost derivation (M2): the enrich stage resolves the price table (control-plane
		// Postgres, both profiles) to derive cost at ingest. Fail-soft — a lookup miss/
		// error leaves cost null, never fails ingest.
		Prices: priceStore, Logger: log,
	})

	receiver := ingest.NewReceiver(pipe, log, cfg.IngestQueueSize, 4, mreg)
	// Scale profile: swap the in-memory ack-window for the durable WAL spool
	// (ADR-0027) so the ack is ack-after-durable and undrained records replay on
	// boot. Local WAL only for now (nil archive sink); the object-store tier is
	// wired when an S3/MinIO sink is configured.
	if cfg.IngestSpoolDir != "" {
		spool, serr := ingest.NewWALSpool(cfg.IngestSpoolDir, cfg.IngestQueueSize, 0, nil, time.Second)
		if serr != nil {
			log.Error("durable ingest spool init failed; refusing to fall back to lossy in-memory queue", "err", serr.Error())
			os.Exit(1)
		}
		receiver.SetSpool(spool)
		log.Info("durable WAL ingest spool enabled", "dir", cfg.IngestSpoolDir)
	}
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
	// Immediate revocation (Arc O / O4, #63): make EVERY signed-token verify consult the
	// revocation store by construction. All plugin-token verification (Query auth, plugin
	// primitives, jobs) funnels through pluginSigner.Verify*, so one injected checker covers
	// them all — a revoked user's frontend token / assertion or a revoked plugin's service
	// token is denied on the next request, not at TTL. Fails closed on a store error.
	pluginSigner.SetRevocationChecker(func(ctx context.Context, jti, pluginID, userEmail string, issuedAt time.Time) (bool, error) {
		return controlplane.TokenLive(ctx, pool, jti, pluginID, userEmail, issuedAt)
	})

	maxWindow, _ := time.ParseDuration(cfg.QueryMaxWindow)
	qsrv := query.NewServer(store, pool, log, maxWindow, mreg, pluginSigner)
	qsrv.SetMaxResponseBytes(cfg.QueryMaxResponseBytes) // #83 response-size ceiling; 0 keeps the built-in default
	if dual != nil {
		qsrv.SetDualStore(dual) // reads unify lite∪scale (RULING-MIG6); see query.DualRouter
	}

	// Plugin supervisor (H2): discovers backend plugins (spec.backend) from the
	// plugin dir, handshakes + health-probes them via the external-URL executor,
	// and issues service tokens. Only the leader replica supervises (advisory lock,
	// below).
	sup := supervisor.New(supervisor.NewDirProvider(cfg.PluginDir, log),
		executors.NewExternalURL(5*time.Second), pluginSigner, mreg, log, supervisor.Config{}, nil)
	// Immediate plugin-token revocation on disable (Arc O / O4): a disabled plugin's issued
	// service token is denied at the next verify, not at TTL; re-enable clears the revocation.
	sup.SetTokenRevoker(
		func(id, reason string) {
			if err := controlplane.RevokePluginTokens(rootCtx, pool, id, reason); err != nil {
				log.Error("revoke plugin tokens failed", "plugin_id", id, "err", err)
			}
		},
		func(id string) { _ = controlplane.Unrevoke(rootCtx, pool, controlplane.RevokeKindPlugin, id) },
	)

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
	// Provisioning (Arc O / O3): create-org + invite/set-role/remove-member. Every member
	// sibling enters ONE shared gate resolved against the TARGET org; roles are capped
	// strictly below the actor's. create-org gates on instance-admin (default-org owner).
	auth.RegisterProvisioning(apiMux)
	// User revocation (Arc O / O4, #63): instance-admin disables an account and denies its
	// entire credential subtree immediately (sessions + minted keys + live plugin tokens).
	auth.RegisterRevocation(apiMux)
	// OIDC/SSO (Arc O / O5, #21): per-org external IdP login. The client secret is sealed with a
	// STABLE key (SECRETBOX_KEY) so config survives restart; without one it falls back to an
	// ephemeral box (config re-entry after restart — dev only). NO plan gating: OSS core.
	ssoBox, ssoStable := ssoSecretBox(cfg.SecretboxKey)
	if !ssoStable {
		log.Warn("SSO secret box is EPHEMERAL — SSO config will not survive a restart",
			"set_env", brand.Env("SECRETBOX_KEY"), "value", "base64 32-byte key")
	}
	ssoHTTP := &http.Client{Timeout: 15 * time.Second}
	auth.RegisterSSO(apiMux, ssoBox, ssoHTTP, cfg.PublicURL)
	// Price table management (ADR-0029): session-authed; reads open to any session,
	// writes gated to admin (config authority). Global entries carry no project; the
	// per-project discount is scoped to the caller's own project.
	// Re-pricing trigger (M4): an admin-gated background job that re-derives cost across
	// history when a price version is superseded or a discount changes. Runs on its own
	// generous budget (NOT the interactive read timeout), resumable, and bounded — never
	// nulls an existing cost on a transient blip (it stops loud and resumes).
	repriceLauncher := reprice.NewLauncher(rootCtx, repriceScanners, store, priceStore, reprice.Config{}, 2, log)
	auth.RegisterPricing(apiMux, priceStore, repriceLauncher)
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
	// roleResolver resolves a user's membership role in the org that owns a project (O2) —
	// the per-request-per-project authority the plugin mints and the query seam use. Same
	// currency for humans and plugins; a non-member/unknown project yields "" (no scope).
	roleResolver := func(ctx context.Context, userID, projectID string) (string, error) {
		return controlplane.RoleForProject(ctx, pool, userID, projectID)
	}
	apiMux.Handle("/api/plugins/", pluginproxy.New(sup, pluginSigner, projectResolver, roleResolver, log).Handler("/api/plugins/"))
	// Frontend-token mint (J1): session-authed; the shell mints a per-plugin,
	// short-TTL, audience-bound frontend token scoped to plugin-grant ∩ session ∩
	// project. Least-privilege-by-default for cooperating frontends, NOT a boundary
	// against a hostile one (same-origin — see frontendtoken.Handler).
	apiMux.HandleFunc("/v1alpha1/plugin-frontend-token",
		frontendtoken.New(pluginSigner, regSource, projectResolver, roleResolver).Mint)
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
	settingsSchema := func(pluginID string) (json.RawMessage, bool, bool) { return registry.SchemaFor(regSource, pluginID) }
	// Settings WRITES require configuration authority (admins today, #21 seam): the
	// session role must carry a write scope. The frontend token already bounded the
	// plugin + tenant; this is the "who may administer" half so a viewer cannot
	// overwrite project-shared config/secrets.
	canWriteSettings := func(r *http.Request, projectID string) bool {
		sess, ok := authhttp.SessionFrom(r.Context())
		if !ok {
			return false
		}
		// O2: resolve the user's role IN THE TARGET PROJECT'S ORG (not the ambient
		// default-org role) — a viewer in this project's org cannot write its settings even
		// if they are an owner of another org. Fail closed on any resolution error.
		role, err := controlplane.RoleForProject(r.Context(), pool, sess.User.ID, projectID)
		if err != nil {
			return false
		}
		return perm.HasWriteAuthority(perm.RoleScopes(role))
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
	// Flush the WAL's final checkpoint and stop its checkpointer (no-op for the
	// in-memory spool). Undrained durable records replay on next boot.
	if err := receiver.Close(); err != nil {
		log.Warn("ingest spool close", "err", err.Error())
	}
	log.Info("stopped")
	return nil
}

// buildScaleStore dials the ClickHouse-scale adapter, migrates it on boot (if
// enabled), and applies the mandatory per-query resource caps (RULING-CH9 — the
// adapter fails closed until all four are set). Returns the store and a close func.
func buildScaleStore(ctx context.Context, cfg platform.Config, log *slog.Logger) (*clickhouse.Store, func(), error) {
	opts, err := ch.ParseDSN(cfg.ClickHouseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CLICKHOUSE_URL: %w", err)
	}
	// #109 read-after-write: OFF by default. Enable only for a multi-replica ClickHouse
	// behind a distributing LB with a hard RYW requirement (accepts the quorum write cost).
	// Single-node / sticky-endpoint deployments get RYW for free and should leave this off.
	if cfg.CHReadYourWrites {
		clickhouse.ApplyReadYourWrites(opts)
		log.Info("ClickHouse read-your-writes enabled (insert_quorum=auto + select_sequential_consistency); writes wait for replica quorum")
	}
	conn, err := ch.Open(opts)
	if err != nil {
		return nil, nil, fmt.Errorf("opening ClickHouse connection: %w", err)
	}
	closeFn := func() { _ = conn.Close() }
	// FAIL LOUD before serving any read (#88): a ClickHouse too old to honor the
	// deleted mask could read a GDPR-erased span back, so refuse to start. Also
	// feature-detects the lazy-materialization setting so reads can disable it where it
	// exists (a plan reorder could otherwise surface an erased row past the mask).
	info, err := clickhouse.ProbeServer(ctx, conn)
	if err != nil {
		closeFn()
		return nil, nil, fmt.Errorf("clickhouse erasure-readiness check: %w", err)
	}
	if cfg.MigrateOnBoot {
		if err := clickhouse.Migrate(ctx, conn, clickhouse.Config{Cluster: cfg.CHCluster}); err != nil {
			closeFn()
			return nil, nil, fmt.Errorf("migrating ClickHouse schema: %w", err)
		}
	}
	scale := clickhouse.NewStore(conn)
	scale.SetLazyMaterializationGuard(info.HasLazyMaterialization)
	execTimeout, _ := time.ParseDuration(cfg.CHMaxExecutionTime)
	scale.SetReadLimits(clickhouse.ReadLimits{
		MaxExecutionTime: execTimeout,
		MaxMemoryUsage:   uint64(cfg.CHMaxMemoryBytes),
		MaxRowsToRead:    uint64(cfg.CHMaxRowsToRead),
		MaxBytesToRead:   uint64(cfg.CHMaxBytesToRead),
	})
	if ttl, terr := time.ParseDuration(cfg.ErasureSuppressionTTL); terr == nil {
		scale.SetErasureSuppressionTTL(ttl)
	}
	log.Info("ClickHouse-scale store ready", "migrate_on_boot", cfg.MigrateOnBoot, "cluster", cfg.CHCluster)
	return scale, closeFn, nil
}

// ssoSecretBox builds the box that seals SSO client secrets. A base64 32-byte SECRETBOX_KEY
// yields a STABLE box (config survives restart); an empty/invalid key falls back to an ephemeral
// random box (stable=false) so SSO still works within a single boot for dev. Returns stable=false
// when the fallback is used.
func ssoSecretBox(b64Key string) (*secretbox.Box, bool) {
	if b64Key != "" {
		if raw, err := base64.StdEncoding.DecodeString(b64Key); err == nil && len(raw) == 32 {
			var key [32]byte
			copy(key[:], raw)
			if box, err := secretbox.New(key); err == nil {
				return box, true
			}
		}
	}
	box, err := secretbox.NewRandom()
	if err != nil {
		return nil, false
	}
	return box, false
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
