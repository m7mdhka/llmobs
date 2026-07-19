// Package platform holds cross-cutting kernel infrastructure: configuration,
// logging, health, and storage connection management.
package platform

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/m7mdhka/llmobs/kernel/pkg/brand"
)

// Config is the kernel daemon configuration. 12-factor: defaults, then an
// optional JSON file (LLMOBS_CONFIG_FILE), then environment overrides. Every env
// var is LLMOBS_-prefixed via the brand constant.
type Config struct {
	DatabaseURL       string `json:"database_url"`
	OTLPHTTPAddr      string `json:"otlp_http_addr"`
	OTLPGRPCAddr      string `json:"otlp_grpc_addr"`
	APIAddr           string `json:"api_addr"`
	LogLevel          string `json:"log_level"`
	LogFormat         string `json:"log_format"`
	MigrateOnBoot     bool   `json:"migrate_on_boot"`
	BootstrapProject  string `json:"bootstrap_project"`
	BootstrapAPIKey   string `json:"bootstrap_api_key"`
	BootstrapAdminEml string `json:"bootstrap_admin_email"`
	BootstrapAdminPwd string `json:"bootstrap_admin_password"`
	QueryMaxWindow    string `json:"query_max_window"`   // e.g. "720h"; LLMOBS_QUERY_MAX_WINDOW
	QueryStmtTimeout  string `json:"query_stmt_timeout"` // server-side statement_timeout for DSL reads; e.g. "30s"
	// QueryMaxResponseBytes caps the SERIALIZED size of a single query/trace-tree response.
	// Row count is already bounded, but a page of wide-payload rows is not — without
	// this the kernel buffers an unbounded response and OOMs (a BOTH-profile DoS). Exceeding
	// it returns a typed response_too_large 413. LLMOBS_QUERY_MAX_RESPONSE_BYTES; 0 => the
	// built-in 32 MiB default (query.DefaultMaxResponseBytes).
	QueryMaxResponseBytes int64  `json:"query_max_response_bytes"`
	CookieSecure          bool   `json:"cookie_secure"`        // set Secure on session cookies
	PublicURL             string `json:"public_url"`           // external origin for OIDC redirect_uri; empty => derive from request Host
	SecretboxKey          string `json:"secretbox_key"`        // base64 32-byte key sealing SSO client secrets; empty => ephemeral (config lost on restart)
	WebUIDir              string `json:"webui_dir"`            // dir of the built shell; empty => placeholder
	PluginDir             string `json:"plugin_dir"`           // dir of dev-mode plugins; empty => none
	DevPluginRemotes      string `json:"dev_plugin_remotes"`   // `make dev`: "id=url,id=url" — advertise live dev-server remoteEntry URLs; empty in prod
	MetricsAddr           string `json:"metrics_addr"`         // Prometheus /metrics bind; empty => mounted on API server
	ClockSkewThreshold    string `json:"clock_skew_threshold"` // e.g. "5m"; drift beyond stamps dq
	ServeShell            bool   `json:"serve_shell"`          // serve the web shell at /; false => headless (API only)
	RedactPresets         string `json:"redact_presets"`       // CSV of preset detectors; "none" disables; default the standard set
	RedactCustomJSON      string `json:"redact_custom"`        // JSON array of {name,pattern,token} custom rules
	IngestQueueSize       int    `json:"ingest_queue_size"`    // in-process ingest queue capacity (async-ack buffer)
	// IngestSpoolDir, when set, selects the durable WAL ingest spool (scale
	// profile): the ack becomes durable when bytes hit this local WAL, and
	// undrained records replay on boot. Empty => the in-memory spool (lite).
	IngestSpoolDir string `json:"ingest_spool_dir"`
	// ShutdownDrainTimeout bounds how long shutdown waits for the ingest queue to
	// persist before giving up. It MUST be shorter than the orchestrator's
	// terminationGracePeriodSeconds (default 30s in K8s) so the drain completes
	// before SIGKILL — leave headroom for the ~5s server shutdown too.
	ShutdownDrainTimeout string `json:"shutdown_drain_timeout"` // e.g. "20s"
	// PersistUnhealthyThreshold: consecutive persist failures before /readyz goes
	// not-ready and the receivers shed with 503. >1 avoids flapping on a
	// single transient error; recovery is immediate on the first success.
	PersistUnhealthyThreshold int `json:"persist_unhealthy_threshold"`
	// ErasureSuppressionTTL: how long a GDPR-erasure tombstone blocks re-delivery
	// of an erased span. Need only outlast plausible redelivery, not forever.
	ErasureSuppressionTTL string `json:"erasure_suppression_ttl"` // e.g. "720h"
	// PluginReconcileInterval: how often the plugin supervisor reconciles backend
	// plugins toward running (handshake + health probe). Only the leader replica
	// supervises (pg advisory lock).
	PluginReconcileInterval string `json:"plugin_reconcile_interval"` // e.g. "15s"
	// EventBacklogCap: max unacked events a plugin subscriber may fall behind on a
	// (project, topic) before the overflow is dead-lettered — bounds a dead
	// subscriber so it cannot pin the event log.
	EventBacklogCap int `json:"event_backlog_cap"`
	// Scale event bus: when EventRedisURL or EventRedisSentinelAddrs is
	// set, the event bus uses the Redis/Valkey Streams backend instead of Postgres.
	// Sentinel (MasterName + SentinelAddrs) gives HA failover; empty => lite.
	EventRedisURL              string   `json:"event_redis_url"`
	EventRedisMasterName       string   `json:"event_redis_master_name"`
	EventRedisSentinelAddrs    []string `json:"event_redis_sentinel_addrs"`
	EventRedisPassword         string   `json:"event_redis_password"`
	EventRedisSentinelPassword string   `json:"event_redis_sentinel_password"`
	// Managed-cloud Redis auth. EventRedisUsername is the ACL/IAM user (AWS
	// ElastiCache IAM user, GCP Memorystore ACL user). EventRedisPasswordFile, when set,
	// is re-read on every (re)connect for the CURRENT rotating token, so an external
	// sidecar can refresh short-lived credentials without a kernel restart; it takes
	// precedence over the static EventRedisPassword.
	EventRedisUsername     string `json:"event_redis_username"`
	EventRedisPasswordFile string `json:"event_redis_password_file"`
	// Scale dual-read: when ClickHouseURL is set, the kernel
	// runs the PERMANENT dual-read layer — every read unifies Postgres-lite
	// (historical) and ClickHouse-scale (new), and writes go to scale with
	// seed-on-migrate. This is how a self-hoster crosses the lite→scale boundary
	// without a migration cliff: no read window where data is missing. Empty => lite.
	ClickHouseURL string `json:"clickhouse_url"`
	// CHCluster is the ClickHouse cluster name for ON CLUSTER DDL on boot-migrate
	// (empty => standalone/Replicated-database; "default" is refused).
	CHCluster string `json:"ch_cluster"`
	// ClickHouse per-query resource caps. The scale adapter fails closed
	// unless all four are set; these defaults are generous for interactive reads.
	CHMaxExecutionTime string `json:"ch_max_execution_time"` // e.g. "30s"
	CHMaxMemoryBytes   int64  `json:"ch_max_memory_bytes"`
	CHMaxRowsToRead    int64  `json:"ch_max_rows_to_read"`
	CHMaxBytesToRead   int64  `json:"ch_max_bytes_to_read"`
	// CHReadYourWrites opts a MULTI-REPLICA ClickHouse behind a distributing LB
	// into read-after-write consistency (insert_quorum + select_sequential_consistency),
	// at the cost of quorum write latency. OFF by default: single-node / sticky-endpoint
	// deployments already have RYW for free. LLMOBS_CH_READ_YOUR_WRITES.
	CHReadYourWrites bool `json:"ch_read_your_writes"`
	// Lite→scale backfill. OPTIONAL and convenience-only — dual-read
	// already makes lite data readable, so this just moves cold rows onto scale in the
	// background. Decoupled from boot readiness; resumable across restarts. Only runs
	// when dual-read (ClickHouseURL) is enabled.
	BackfillOnBoot    bool   `json:"backfill_on_boot"`
	BackfillChunkSize int    `json:"backfill_chunk_size"` // rows per batch; default 500
	BackfillBudget    string `json:"backfill_budget"`     // SEPARATE execution budget (NOT the read timeout); e.g. "30m"
	// Blobs primitive (large-artifact storage). The backend is chosen by config: if
	// BlobS3Endpoint is set the scale S3 adapter is used, else the lite local filesystem
	// adapter rooted at BlobDir. Both are behind the one blob.Store seam.
	BlobDir          string `json:"blob_dir"`            // local backend root (lite); default under the data dir
	BlobMaxBytes     int64  `json:"blob_max_bytes"`      // per-object upload ceiling; default 64 MiB
	BlobS3Endpoint   string `json:"blob_s3_endpoint"`    // host[:port]; presence selects the S3 backend
	BlobS3AccessKey  string `json:"blob_s3_access_key"`  //
	BlobS3SecretKey  string `json:"blob_s3_secret_key"`  //
	BlobS3Bucket     string `json:"blob_s3_bucket"`      //
	BlobS3Region     string `json:"blob_s3_region"`      // may be empty for MinIO
	BlobS3UseSSL     bool   `json:"blob_s3_use_ssl"`     //
	BlobS3RequestSSE bool   `json:"blob_s3_request_sse"` // opt into a per-object SSE header (KMS backends)
}

func defaults() Config {
	return Config{
		DatabaseURL:               "postgres://llmobs:llmobs@localhost:5432/llmobs?sslmode=disable",
		OTLPHTTPAddr:              ":4318",
		OTLPGRPCAddr:              ":4317",
		APIAddr:                   ":8080",
		LogLevel:                  "info",
		LogFormat:                 "json",
		MigrateOnBoot:             true,
		BootstrapProject:          "default",
		QueryMaxWindow:            "720h",
		QueryStmtTimeout:          "30s",
		ServeShell:                true,
		MetricsAddr:               ":9090",
		ClockSkewThreshold:        "5m",
		RedactPresets:             "email,secret,iban,credit_card,phone",
		IngestQueueSize:           4096,
		ShutdownDrainTimeout:      "20s",
		PersistUnhealthyThreshold: 5,
		ErasureSuppressionTTL:     "720h",
		PluginReconcileInterval:   "15s",
		EventBacklogCap:           10000,
		CHMaxExecutionTime:        "30s",
		CHMaxMemoryBytes:          2 << 30, // 2 GiB
		CHMaxRowsToRead:           50_000_000,
		CHMaxBytesToRead:          5 << 30, // 5 GiB
		BackfillChunkSize:         500,
		BackfillBudget:            "30m",
		BlobDir:                   "/var/lib/" + strings.ToLower(brand.Name) + "/blobs",
		BlobMaxBytes:              64 << 20, // 64 MiB
	}
}

// LoadConfig builds the configuration from defaults, an optional JSON file, and
// environment overrides.
func LoadConfig() (Config, error) {
	c := defaults()
	if p := os.Getenv(brand.Env("CONFIG_FILE")); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return c, err
		}
		if err := json.Unmarshal(b, &c); err != nil {
			return c, err
		}
	}
	envStr(brand.Env("DATABASE_URL"), &c.DatabaseURL)
	envStr(brand.Env("OTLP_HTTP_ADDR"), &c.OTLPHTTPAddr)
	envStr(brand.Env("OTLP_GRPC_ADDR"), &c.OTLPGRPCAddr)
	envStr(brand.Env("API_ADDR"), &c.APIAddr)
	envStr(brand.Env("LOG_LEVEL"), &c.LogLevel)
	envStr(brand.Env("LOG_FORMAT"), &c.LogFormat)
	envStr(brand.Env("BOOTSTRAP_PROJECT"), &c.BootstrapProject)
	envStr(brand.Env("BOOTSTRAP_API_KEY"), &c.BootstrapAPIKey)
	envStr(brand.Env("BOOTSTRAP_ADMIN_EMAIL"), &c.BootstrapAdminEml)
	envStr(brand.Env("BOOTSTRAP_ADMIN_PASSWORD"), &c.BootstrapAdminPwd)
	envStr(brand.Env("QUERY_MAX_WINDOW"), &c.QueryMaxWindow)
	envStr(brand.Env("QUERY_STMT_TIMEOUT"), &c.QueryStmtTimeout)
	envStr(brand.Env("WEBUI_DIR"), &c.WebUIDir)
	envStr(brand.Env("PLUGIN_DIR"), &c.PluginDir)
	envStr(brand.Env("DEV_PLUGIN_REMOTES"), &c.DevPluginRemotes)
	envStr(brand.Env("METRICS_ADDR"), &c.MetricsAddr)
	envStr(brand.Env("CLOCK_SKEW_THRESHOLD"), &c.ClockSkewThreshold)
	envStr(brand.Env("REDACT_PRESETS"), &c.RedactPresets)
	envStr(brand.Env("REDACT_CUSTOM"), &c.RedactCustomJSON)
	envStr(brand.Env("SHUTDOWN_DRAIN_TIMEOUT"), &c.ShutdownDrainTimeout)
	envInt(brand.Env("INGEST_QUEUE_SIZE"), &c.IngestQueueSize)
	envStr(brand.Env("INGEST_SPOOL_DIR"), &c.IngestSpoolDir)
	envInt(brand.Env("PERSIST_UNHEALTHY_THRESHOLD"), &c.PersistUnhealthyThreshold)
	envStr(brand.Env("ERASURE_SUPPRESSION_TTL"), &c.ErasureSuppressionTTL)
	envStr(brand.Env("PLUGIN_RECONCILE_INTERVAL"), &c.PluginReconcileInterval)
	envInt(brand.Env("EVENT_BACKLOG_CAP"), &c.EventBacklogCap)
	envStr(brand.Env("EVENT_REDIS_URL"), &c.EventRedisURL)
	envStr(brand.Env("EVENT_REDIS_MASTER_NAME"), &c.EventRedisMasterName)
	envStrList(brand.Env("EVENT_REDIS_SENTINEL_ADDRS"), &c.EventRedisSentinelAddrs)
	envStr(brand.Env("EVENT_REDIS_PASSWORD"), &c.EventRedisPassword)
	envStr(brand.Env("EVENT_REDIS_SENTINEL_PASSWORD"), &c.EventRedisSentinelPassword)
	envStr(brand.Env("EVENT_REDIS_USERNAME"), &c.EventRedisUsername)
	envStr(brand.Env("EVENT_REDIS_PASSWORD_FILE"), &c.EventRedisPasswordFile)
	envStr(brand.Env("CLICKHOUSE_URL"), &c.ClickHouseURL)
	envStr(brand.Env("CH_CLUSTER"), &c.CHCluster)
	envStr(brand.Env("CH_MAX_EXECUTION_TIME"), &c.CHMaxExecutionTime)
	envInt64(brand.Env("CH_MAX_MEMORY_BYTES"), &c.CHMaxMemoryBytes)
	envInt64(brand.Env("CH_MAX_ROWS_TO_READ"), &c.CHMaxRowsToRead)
	envInt64(brand.Env("CH_MAX_BYTES_TO_READ"), &c.CHMaxBytesToRead)
	envInt64(brand.Env("QUERY_MAX_RESPONSE_BYTES"), &c.QueryMaxResponseBytes)
	envBool(brand.Env("CH_READ_YOUR_WRITES"), &c.CHReadYourWrites)
	envBool(brand.Env("BACKFILL_ON_BOOT"), &c.BackfillOnBoot)
	envInt(brand.Env("BACKFILL_CHUNK_SIZE"), &c.BackfillChunkSize)
	envStr(brand.Env("BACKFILL_BUDGET"), &c.BackfillBudget)
	envBool(brand.Env("MIGRATE_ON_BOOT"), &c.MigrateOnBoot)
	envBool(brand.Env("COOKIE_SECURE"), &c.CookieSecure)
	envStr(brand.Env("PUBLIC_URL"), &c.PublicURL)
	envStr(brand.Env("SECRETBOX_KEY"), &c.SecretboxKey)
	envBool(brand.Env("SERVE_SHELL"), &c.ServeShell)
	envStr(brand.Env("BLOB_DIR"), &c.BlobDir)
	envInt64(brand.Env("BLOB_MAX_BYTES"), &c.BlobMaxBytes)
	envStr(brand.Env("BLOB_S3_ENDPOINT"), &c.BlobS3Endpoint)
	envStr(brand.Env("BLOB_S3_ACCESS_KEY"), &c.BlobS3AccessKey)
	envStr(brand.Env("BLOB_S3_SECRET_KEY"), &c.BlobS3SecretKey)
	envStr(brand.Env("BLOB_S3_BUCKET"), &c.BlobS3Bucket)
	envStr(brand.Env("BLOB_S3_REGION"), &c.BlobS3Region)
	envBool(brand.Env("BLOB_S3_USE_SSL"), &c.BlobS3UseSSL)
	envBool(brand.Env("BLOB_S3_REQUEST_SSE"), &c.BlobS3RequestSSE)
	return c, nil
}

func envStr(key string, dst *string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func envBool(key string, dst *bool) {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			*dst = b
		}
	}
}

// envStrList reads a comma-separated env var into a string slice (trimmed, empties
// dropped) — used for the Sentinel address list.
func envStrList(key string, dst *[]string) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	*dst = out
}

func envInt(key string, dst *int) {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

func envInt64(key string, dst *int64) {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			*dst = n
		}
	}
}
