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

	// Seed the default per-token price table so cost derivation works out of the box.
	//
	// Prices live in Postgres in BOTH the lite and scale profiles. They are small,
	// read-heavy-at-ingest, rarely-edited reference metadata — not telemetry — so they
	// belong with the control plane (always Postgres) rather than in append-only
	// ClickHouse. Keeping them out of ClickHouse avoids putting mutable reference data in
	// an append-only store and keeps a single source of truth instead of a per-profile
	// fork. The enrich stage reads this table (it already holds the pool) and caches it
	// in-process, invalidating the cache when a new version appears.
	//
	// The table is user-editable WITHOUT a code change, and it is append-only and
	// history-preserving: an edit to a (provider, model) price INSERTS A NEW VERSION row;
	// prior versions are never updated or deleted. That immutability is the whole point.
	// A derived cost records the exact price row it was billed against by its primary key,
	// the string "<provider>/<model>#<version>", so a later price correction can re-derive
	// historical spans deterministically against the precise old rates. The version that
	// applies to a span is the newest one whose effective_from is <= the span's start time
	// — so a price dated in the future never retroactively re-bills history, and a
	// re-pricing pass finds every span whose stored reference points at a now-superseded
	// version and re-emits it. A hardcoded price map (or a mutable upsert) would break all
	// of this: correcting or adding a model would need a code change, and it would lose the
	// exact historical rate a span was actually priced with — which is what makes a price
	// correction's backfill reproducible.
	//
	// Pricing is DATA, not code. One derivation path prices every provider and every token
	// bucket (input/output, cache read/write, reasoning, audio, …) by reading the rates
	// off the entry: each rate carries its per-token price and, optionally, which base
	// count it is subtracted from (a "reduces" hint), so the residual and any tiered
	// above-threshold rates are computed by iterating the entry — never a hardcoded
	// "cache_read subtracts from input" branch. Adding a provider or a new detail key is
	// adding a ROW here, never a new code branch. Provider/model are stored canonical
	// (normalized by the one function used at both seed and lookup time, so a lookup can
	// never silently miss and bill zero); the raw provider string stays on the span
	// verbatim.
	//
	// SeedDefaults is idempotent — it inserts each default row with ON CONFLICT (provider,
	// model, version) DO NOTHING — and it is the ONLY writer that stamps a row as a
	// "default"; every API/operator write is stamped "override" (the provenance cannot be
	// spoofed from the outside). So a re-seed on the next boot can never clobber an
	// operator's edits: their edits are newer "override" versions with a later
	// effective_from that win resolution, and the re-seed's version-1 default rows simply
	// no-op against the existing rows. Per-project negotiated discounts are a separate,
	// project-scoped multiplier applied at derivation, so one tenant can never edit
	// another tenant's — or the global list — price.
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

	// One shared persist-health signal answers a single question everywhere: can the
	// storage adapter accept writes right now? It is a tiny, dependency-free, lock-free
	// (atomic) value passed EXPLICITLY to the three parties that must agree on the answer,
	// rather than a global — so there is no shared mutable state and no import cycle:
	//   - the persist stage (the WRITER) folds each write outcome into it;
	//   - the OTLP receivers (a READER) use it for backpressure — they shed load with a
	//     503 / gRPC UNAVAILABLE while persistence is unhealthy, so a client backs off
	//     instead of the kernel accepting writes it cannot durably store;
	//   - the /readyz handler (a READER) uses it for readiness.
	//
	// The signal does not flap. It flips UNHEALTHY only after a RUN of consecutive persist
	// failures reaches the configured threshold (defaulting to 1 — flip on the first real
	// failure), never on a single transient error, and it recovers on the FIRST success.
	// Context-cancellation / deadline errors are deliberately NOT counted as storage-health
	// failures: they mean shutdown or a request timeout, not that storage is unwell.
	persistHealth := ingesthealth.New(cfg.PersistUnhealthyThreshold)
	// Export the same flag as a gauge so the readiness/backpressure state is observable:
	// 1 when storage is accepting writes, 0 when it is shedding.
	mreg.SampledGauge("llmobs_persist_healthy", "1 when the storage adapter is accepting writes, else 0.",
		func() float64 {
			if persistHealth.Healthy() {
				return 1
			}
			return 0
		})

	store := postgres.NewStore(pool)
	if ttl, terr := time.ParseDuration(cfg.ErasureSuppressionTTL); terr == nil {
		store.SetErasureSuppressionTTL(ttl) // how long an erasure tombstone blocks re-delivery of an erased span
	}
	if qt, terr := time.ParseDuration(cfg.QueryStmtTimeout); terr == nil {
		store.SetQueryTimeout(qt) // server-side statement_timeout backstop for DSL reads
	}

	// When a ClickHouse-scale store is configured, the kernel runs a PERMANENT dual-read
	// layer instead of a one-shot migration cutover. writeStore becomes a dual decorator:
	// new writes go to the scale (ClickHouse) store, and a lite-only span that is touched
	// is seeded across on the way. Every read unifies the two tiers — the query server
	// returns lite ∪ scale, deduplicating on a span's identity and preferring the scale
	// copy. The point is that the lite→scale boundary crossing never strands a self-hoster:
	// a just-written span is immediately readable, a historical lite span is always
	// readable, and there is no window in which either is missing — so correctness never
	// depends on a backfill having finished. An empty ClickHouseURL leaves the kernel on
	// the single-store lite path, unchanged.
	var writeStore storage.TelemetryStore = store
	// A re-pricing run (re-derives cost after a price edit) scans the store the spans
	// actually LIVE in. In lite that is Postgres; in a dual (scale) deployment a run must
	// cover BOTH tiers, so the scale ClickHouse store is appended below. The run's cursor/
	// state is always Postgres — it is control-plane metadata, in both profiles.
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

		// The optional lite→scale backfill copies cold historical rows from lite onto the
		// scale engine. It is convenience-only: dual-read already makes lite data readable,
		// so this just moves rows so the older data also benefits from ClickHouse over time
		// — safe to start, stop, restart, or skip entirely. It runs on its OWN generous
		// execution budget (NOT the interactive read timeout), is resumable across restarts
		// via a persisted cursor, and is fully DECOUPLED from boot readiness: /readyz never
		// waits on it, and a partially-complete backfill is still correct because dual-read
		// serves whatever has not migrated yet.
		if cfg.BackfillOnBoot {
			budget, _ := time.ParseDuration(cfg.BackfillBudget)
			runner := backfill.New(store, scale, backfill.Config{
				ChunkSize: cfg.BackfillChunkSize, Budget: budget,
			}, log)
			go func() {
				// Single-flight across replicas. Without a guard, EVERY replica with
				// BackfillOnBoot would run the backfill concurrently — racing the one shared
				// (ts, project_id, id) resume cursor and hammering ClickHouse with N× the
				// load. Gate it behind a dedicated advisory-locked Postgres connection (the
				// same leader-election pattern the plugin supervisor uses, under a DISTINCT
				// lock key) so exactly one replica runs it and the rest skip. A skipped
				// replica loses nothing — dual-read already serves the data, so the backfill
				// is pure housekeeping. The lock is held for the run's duration and released
				// when it finishes (the deferred unlock runs before the connection is
				// returned to the pool, so a reused connection never carries a stale lock).
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
	// The durable event bus is the substrate behind the plugins' `events` primitive: a
	// durable event log with a per-subscriber consumer offset. It publishes span.ingested;
	// plugins subscribe by poll/ack. Durability comes from the log + offsets, never from a
	// LISTEN/NOTIFY-style wake (that is a latency optimization only) — a subscriber that
	// was down simply replays from its stored offset on its next poll.
	//
	// The bus is factored exactly like storage: all the hard logic — replay-from-offset,
	// at-least-once delivery (poll does not advance the offset; only ack does), the
	// per-subscriber backlog cap → dead-letter, and tenant/topic isolation — lives in one
	// backend-agnostic layer over a tiny persistence seam. Only the persistence backend
	// differs by profile, and the two are behaviorally interchangeable (the same
	// conformance suite runs against both):
	//   - lite  → Postgres-backed. NO new infrastructure — the lite promise is a single
	//             dependency, Postgres only.
	//   - scale → Redis/Valkey Streams, selected below when a Redis URL or Sentinel set is
	//             configured. Boot fails CLOSED if the configured Redis/Valkey can't init.
	var eventStore bus.Store = postgres.NewEventStore(pool)
	if cfg.EventRedisURL != "" || len(cfg.EventRedisSentinelAddrs) > 0 {
		rdb, rerr := redisstore.Dial(rootCtx, redisstore.Config{
			URL:              cfg.EventRedisURL,
			MasterName:       cfg.EventRedisMasterName,
			SentinelAddrs:    cfg.EventRedisSentinelAddrs,
			Password:         cfg.EventRedisPassword,
			SentinelPassword: cfg.EventRedisSentinelPassword,
			Username:         cfg.EventRedisUsername,     // ACL / IAM user (ElastiCache IAM, Memorystore ACL)
			PasswordFile:     cfg.EventRedisPasswordFile, // rotating token file, re-read on each (re)connect
		})
		if rerr != nil {
			log.Error("scale event bus (Redis/Valkey) init failed", "err", rerr.Error())
			os.Exit(1)
		}
		defer func() { _ = rdb.Close() }()
		rstore := redisstore.New(rdb, strings.ToLower(brand.Name))
		// Bound each stream to the delivery window (the backlog cap) so the Streams log
		// cannot grow without limit. This is safe because anything more than backlogCap
		// behind the latest id is NOT deliverable — the bus dead-letters a subscriber that
		// far behind — so the approximate trim only reclaims entries no subscriber can
		// still consume; it never drops deliverable backlog.
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
		// The enrich stage derives per-token cost at ingest: it resolves the price table
		// (control-plane Postgres, both profiles) for the span's provider/model, applies the
		// resolved entry's rates plus any per-project discount, and stamps the cost. It is
		// FAIL-SOFT — a price lookup miss or error leaves the cost null and NEVER fails
		// ingest, so a missing or misconfigured price can never drop telemetry.
		Prices: priceStore, Logger: log,
	})

	receiver := ingest.NewReceiver(pipe, log, cfg.IngestQueueSize, 4, mreg)
	// The default ingest path is async-ack: the receiver reads the body, enqueues it in a
	// bounded in-process channel, and acks; the rest of the pipeline runs on a worker pool
	// AFTER the ack. That keeps the ack fast (no DB on the ack path) but leaves two
	// silent-loss windows — a SIGKILL that outlives the drain deadline loses everything
	// still queued, and a persist error drops an already-acked job.
	//
	// When a spool directory is configured (the scale profile), swap that in-memory
	// ack-window for a durable write-ahead-log spool: a record is fsynced to a local
	// append-only WAL (per-record CRC + monotonic seq) BEFORE the ack, so the ack is
	// ack-after-durable and any records not yet drained REPLAY on the next boot (a torn
	// final write is caught by the CRC and truncated). Replay is idempotent — a
	// re-delivered record is a harmless merge — and it replays THROUGH the erasure
	// suppression guard, so a crash-then-replay can never resurrect an erased span.
	//
	// Local WAL only for now (nil archive sink). The archival tier — a background flusher
	// that ships sealed, fully-persisted segments to object storage for cold retention,
	// strictly behind the WAL and never on the hot ack path — is wired when an S3/MinIO
	// sink is configured. If the spool can't initialize we FAIL CLOSED and exit rather than
	// silently reverting to the lossy in-memory queue (which would quietly reopen exactly
	// the loss windows the spool exists to close).
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
	// Ingest queue occupancy is scrape-sampled so an operator can watch saturation approach
	// the high-water backpressure mark — the point at which the receiver starts shedding
	// with 503 / gRPC UNAVAILABLE — e.g. when storage slows or is unavailable mid-ingest.
	mreg.SampledGauge("llmobs_ingest_queue_depth", "In-process ingest queue depth (acked, not yet persisted).",
		func() float64 { return float64(receiver.QueueLen()) })
	mreg.SampledGauge("llmobs_ingest_queue_capacity", "In-process ingest queue capacity.",
		func() float64 { return float64(receiver.QueueCap()) })

	// The kernel's Ed25519 plugin-signing key (generated in memory at boot, for lite). It
	// mints and verifies the two tokens of the double-token auth model: a SERVICE token
	// (which plugin, and what it may do — carried by the supervisor, short-TTL + refresh)
	// and a per-request IDENTITY assertion (who the acting user is, and what they may see —
	// carried by the proxy, audience-bound to the calling plugin, short-TTL). When a plugin
	// calls the Query API BOTH are verified and the granted permission is the INTERSECTION
	// of the two: the kernel never trusts a plugin-supplied identity, and session cookies
	// are never forwarded to plugins. Because the key is in memory in lite, a kernel restart
	// rotates it and invalidates every live service token — a normal, safe outcome; the
	// supervisor simply re-handshakes.
	pluginSigner, err := plugintoken.NewSigner()
	if err != nil {
		return err
	}
	// Immediate revocation: make EVERY signed-token verify consult the revocation store, by
	// construction. All plugin-token verification (Query auth, the plugin primitives, jobs)
	// funnels through pluginSigner.Verify*, so this ONE injected checker covers them all — a
	// revoked user's frontend token / assertion, or a revoked plugin's service token, is
	// denied on the NEXT request rather than lingering until its TTL expires. It fails
	// CLOSED on a revocation-store error (a token whose liveness can't be confirmed is
	// refused, never allowed through).
	pluginSigner.SetRevocationChecker(func(ctx context.Context, jti, pluginID, userEmail string, issuedAt time.Time) (bool, error) {
		return controlplane.TokenLive(ctx, pool, jti, pluginID, userEmail, issuedAt)
	})

	maxWindow, _ := time.ParseDuration(cfg.QueryMaxWindow)
	qsrv := query.NewServer(store, pool, log, maxWindow, mreg, pluginSigner)
	qsrv.SetMaxResponseBytes(cfg.QueryMaxResponseBytes) // serialized-response byte ceiling; 0 keeps the built-in default
	if dual != nil {
		qsrv.SetDualStore(dual) // every read unifies the lite and scale stores
	}

	// The plugin supervisor discovers backend plugins (those declaring spec.backend) from
	// the plugin directory, handshakes + health-probes each through the external-URL
	// executor, and issues them service tokens. Only the LEADER replica supervises — gated
	// by the advisory lock set up below — so N replicas never each drive the same plugins.
	sup := supervisor.New(supervisor.NewDirProvider(cfg.PluginDir, log),
		executors.NewExternalURL(5*time.Second), pluginSigner, mreg, log, supervisor.Config{}, nil)
	// Immediate plugin-token revocation on disable: when a plugin is disabled, its issued
	// service token is denied at the NEXT verify rather than lingering until its TTL
	// expires; re-enabling the plugin clears the revocation.
	sup.SetTokenRevoker(
		func(id, reason string) {
			if err := controlplane.RevokePluginTokens(rootCtx, pool, id, reason); err != nil {
				log.Error("revoke plugin tokens failed", "plugin_id", id, "err", err)
			}
		},
		func(id string) { _ = controlplane.Unrevoke(rootCtx, pool, controlplane.RevokeKindPlugin, id) },
	)

	// The job scheduler runs plugin jobs (Postgres-backed) on the supervisor's leader
	// cadence — so, like the supervisor, only one replica schedules. Each run presents a
	// bounded "system assertion" that is audience-bound to the plugin and scoped to the
	// plugin's OWN declared grant on the default project — never more than the plugin itself
	// holds (no god-mode system actor) — and every run is audited in plugin_job_runs.
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
	// Provisioning — the endpoints that change WHO has authority (create an org, invite a
	// member, set a member's role, remove a member). This is where an authorization bug
	// stops being a read leak and becomes account takeover, so every member-provisioning
	// sibling enters ONE shared gate as its first act: the gate resolves the actor's role in
	// the TARGET org and requires members-manage authority there, and an assigned role is
	// capped strictly below the actor's own — no member can escalate themselves. create-org
	// gates on the instance admin (the default-org owner).
	auth.RegisterProvisioning(apiMux)
	// User revocation. The three kernel-signed tokens (service token, identity assertion,
	// frontend token) verify by signature + expiry alone, so before this a "revoked" user's
	// browser token kept working until its TTL. Now an instance admin who disables an account
	// denies its ENTIRE credential subtree on the very next request — sessions, minted API
	// keys, AND live plugin tokens — because a derived credential must never outlive its
	// deriver.
	auth.RegisterRevocation(apiMux)
	// OIDC / SSO: per-org login through an external identity provider — the one place the
	// kernel accepts a client-supplied identity, so the whole security property is that the
	// IdP's ID token is fully verified (signature against the rotating JWKS, plus iss/aud/exp)
	// BEFORE any field in it is read. The stored per-org client secret is sealed with a STABLE
	// key (SECRETBOX_KEY) so SSO config survives a restart; without one it falls back to an
	// ephemeral box (config must be re-entered after a restart — dev only). Never feature-
	// gated: SSO is in the OSS core.
	ssoBox, ssoStable := ssoSecretBox(cfg.SecretboxKey)
	if !ssoStable {
		log.Warn("SSO secret box is EPHEMERAL — SSO config will not survive a restart",
			"set_env", brand.Env("SECRETBOX_KEY"), "value", "base64 32-byte key")
	}
	ssoHTTP := &http.Client{Timeout: 15 * time.Second}
	auth.RegisterSSO(apiMux, ssoBox, ssoHTTP, cfg.PublicURL)
	// Price-table management: session-authed; reads open to any session, writes gated to
	// admin (config authority). Global price entries carry no project; the per-project
	// discount is scoped to the caller's OWN project, so one tenant can never edit another
	// tenant's — or the global — price.
	//
	// Re-pricing trigger: an admin-gated background job that re-derives cost across history
	// when a price version is superseded or a discount changes. It runs on its own generous
	// budget (NOT the interactive read timeout), is resumable, and is bounded — it never
	// nulls an existing cost on a transient blip; it stops loud and resumes.
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
	// Supervisor ops API: the snapshot is readable by any authenticated caller; disable /
	// enable are gated to sessions (admin today; finer roles are a follow-on). The more-
	// specific prefix wins over the /v1alpha1/ query catch-all below.
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
	// Plugin backend proxy: /api/plugins/{id}/* forwards to the running plugin backend with
	// the session cookie STRIPPED and a kernel-signed identity assertion injected in its
	// place — and only a running plugin proxies.
	projectResolver := func(r *http.Request) (string, error) {
		if p := r.Header.Get("X-LLMObs-Project"); p != "" {
			return p, nil
		}
		return controlplane.DefaultProjectID(r.Context(), pool)
	}
	// roleResolver resolves a user's membership role in the org that OWNS the given project —
	// the per-request, per-project authority that both the plugin-token mint and the query
	// seam intersect against. It is the same role currency for humans and plugins; a
	// non-member or unknown project yields "" (no scope).
	roleResolver := func(ctx context.Context, userID, projectID string) (string, error) {
		return controlplane.RoleForProject(ctx, pool, userID, projectID)
	}
	apiMux.Handle("/api/plugins/", pluginproxy.New(sup, pluginSigner, projectResolver, roleResolver, log).Handler("/api/plugins/"))
	// Frontend-token mint: session-authed. The shell mints a per-plugin, short-TTL,
	// audience-bound frontend token whose scope is (plugin grant ∩ session ∩ project). It is
	// least-privilege-by-default for a cooperating frontend, NOT a boundary against a hostile
	// one (it is same-origin — see frontendtoken.Handler).
	apiMux.HandleFunc("/v1alpha1/plugin-frontend-token",
		frontendtoken.New(pluginSigner, regSource, projectResolver, roleResolver).Mint)
	// The plugin data primitives (kv, secrets, store, settings): a plugin backend reaches
	// these carrying its double token (service token + user assertion); each endpoint is
	// capability-gated and tenant-scoped FROM that assertion, never from anything the plugin
	// claims about itself.
	pluginAuthz := pluginauth.New(pluginSigner, nil)
	// Publish the kernel's PUBLIC signing key so a plugin backend in any language can verify
	// the kernel-signed assertions it receives. Unauthenticated by design — the key is
	// public; only the private half, held in-process, can mint.
	pluginapi.NewKernelKey(pluginSigner.Public()).Register(apiMux, "/v1alpha1/plugin/kernel-key")
	pluginapi.NewKV(pluginAuthz, postgres.NewPluginKV(pool)).Register(apiMux, "/v1alpha1/plugin/kv")
	// Secrets: envelope-encrypted with the kernel master key (held in memory in lite). The
	// plaintext is never persisted or logged, and is returned only to the owning plugin over
	// its authenticated delivery.
	secretBox, err := secretbox.NewRandom()
	if err != nil {
		return err
	}
	pluginapi.NewSecrets(pluginAuthz, postgres.NewPluginSecrets(pool), secretBox, log).Register(apiMux, "/v1alpha1/plugin/secrets")
	// Store: plugin-owned structured collections. The supervisor provisions each plugin's
	// collections while the plugin is starting, so a failed migration is a plugin-health
	// signal — the plugin does not become healthy until its collections exist.
	pluginStore := postgres.NewPluginStore(pool)
	sup.SetProvisioner(pluginStore)
	pluginapi.NewStore(pluginAuthz, pluginStore).Register(apiMux, "/v1alpha1/plugin/store")
	// Settings is the `kv` primitive made frontend-reachable and schema-aware — NOT a new
	// primitive: it gives every plugin a settings tab with zero settings-specific backend
	// code. Authed by the frontend token (a pure-frontend plugin's only credential);
	// writeOnly (secret) fields are envelope-encrypted with the same box as plugin secrets
	// and never returned on read. The schema comes from the registry (loaded from the
	// plugin manifest).
	settingsStore := pluginsettings.NewStore(postgres.NewPluginKV(pool), secretBox)
	settingsSchema := func(pluginID string) (json.RawMessage, bool, bool) { return registry.SchemaFor(regSource, pluginID) }
	// Settings WRITES require configuration authority (admins today; finer roles a follow-
	// on): the session's role must carry a write scope. The frontend token already bounded
	// the plugin + tenant; this is the "who may administer" half, so a viewer cannot
	// overwrite project-shared config/secrets.
	canWriteSettings := func(r *http.Request, projectID string) bool {
		sess, ok := authhttp.SessionFrom(r.Context())
		if !ok {
			return false
		}
		// Resolve the user's role IN THE TARGET PROJECT'S ORG (not their ambient default-org
		// role): a viewer in this project's org cannot write its settings even if they are an
		// owner of a DIFFERENT org. Fail closed on any resolution error.
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

	// Ordered shutdown: (1) shed brand-new OTLP immediately with a retryable
	// 503 / gRPC UNAVAILABLE, (2) stop the listeners so no new work is enqueued, then
	// (3) DRAIN the acked-but-not-yet-persisted queue within a bounded deadline. The
	// deadline (LLMOBS_SHUTDOWN_DRAIN_TIMEOUT) is set shorter than the Kubernetes
	// terminationGracePeriodSeconds so the drain finishes before SIGKILL; whatever cannot
	// drain in time is COUNTED, not dropped silently (and on the durable spool it replays
	// on the next boot rather than being lost).
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

// buildScaleStore dials the ClickHouse-scale adapter, migrates it on boot (if enabled),
// and applies the mandatory per-query resource caps — the adapter refuses to emit a read
// until all four caps (execution time, memory, rows, bytes) are set, so a pathological
// query can never take the cluster down. Returns the store and a close func.
func buildScaleStore(ctx context.Context, cfg platform.Config, log *slog.Logger) (*clickhouse.Store, func(), error) {
	opts, err := ch.ParseDSN(cfg.ClickHouseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CLICKHOUSE_URL: %w", err)
	}
	// Read-after-write across replicas: OFF by default. Enable it only for a multi-replica
	// ClickHouse behind a distributing load balancer that has a hard read-your-writes
	// requirement and accepts the quorum write-latency cost. Single-node and sticky-endpoint
	// deployments get read-after-write for free (a replica sees its own writes) and should
	// leave this off.
	if cfg.CHReadYourWrites {
		clickhouse.ApplyReadYourWrites(opts)
		log.Info("ClickHouse read-your-writes enabled (insert_quorum=auto + select_sequential_consistency); writes wait for replica quorum")
	}
	conn, err := ch.Open(opts)
	if err != nil {
		return nil, nil, fmt.Errorf("opening ClickHouse connection: %w", err)
	}
	closeFn := func() { _ = conn.Close() }
	// FAIL LOUD before serving any read: a ClickHouse too old to honor the deleted mask on
	// reads could read a GDPR-erased (lightweight-deleted) span back, so refuse to start on
	// an unsupported version rather than silently serve erased data. Also feature-detects the
	// lazy-materialization setting so reads can disable it where it exists (a query-plan
	// reorder could otherwise surface an erased row past the deleted mask).
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

// parseDevRemotes parses the `make dev` override "id=url,id=url" into a map of plugin id
// -> live dev-server remoteEntry URL, so a plugin frontend hot-reloads from its dev server
// instead of the built bundle. Malformed entries are skipped.
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
