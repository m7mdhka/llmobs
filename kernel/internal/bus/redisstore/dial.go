package redisstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Config selects and tunes the Redis/Valkey connection for the scale event bus.
type Config struct {
	// URL is a redis:// / rediss:// DSN for a single endpoint (non-Sentinel).
	URL string
	// MasterName + SentinelAddrs enable Sentinel high-availability (R-EV1): the
	// client resolves the current master through Sentinel and RE-RESOLVES on
	// failover, so a promotion self-heals WITHOUT a process restart.
	MasterName    string
	SentinelAddrs []string
	// Password authenticates to the data nodes; SentinelPassword to the Sentinels.
	Password         string
	SentinelPassword string
	// Username is the ACL / IAM user for AUTH. Managed cloud Redis usually requires a
	// username: an AWS ElastiCache IAM user, or a GCP Memorystore ACL user. Empty keeps
	// the legacy default user (password-only AUTH).
	Username string
	// PasswordFile, when set, is read on EVERY authentication to obtain the CURRENT
	// password — the refreshing-credential path for managed cloud Redis whose token
	// rotates (AWS ElastiCache IAM auth tokens ~15min TTL; GCP Memorystore rotating
	// AUTH). An external agent (sidecar/init) refreshes the file; go-redis re-invokes
	// the credentials provider on each (re)connect, so a rotated token is picked up
	// WITHOUT a kernel restart. Takes precedence over the static Password. This keeps
	// the kernel dependency-free (no cloud SDK on the hot path): the cloud-specific
	// token minting lives in the sidecar, the kernel just re-reads the file.
	PasswordFile string
}

// credentialsProvider returns a go-redis refreshing-credentials hook, or nil to use the
// static Username/Password. When PasswordFile is set, the CURRENT password is read on
// every (re)connect so a rotated managed-cloud token is honored without a restart (#126).
func (cfg Config) credentialsProvider() func(ctx context.Context) (string, string, error) {
	if cfg.PasswordFile == "" {
		return nil
	}
	return func(ctx context.Context) (string, string, error) {
		b, err := os.ReadFile(cfg.PasswordFile)
		if err != nil {
			// A read failure is intentionally left TRANSIENT (classifyAuth does not mark
			// it permanent): a sidecar rotating the token may be mid-write, so go-redis
			// should retry the next connect rather than fail closed. The error wraps only
			// the OS error (path + errno), never the file contents.
			return "", "", fmt.Errorf("reading event-bus password file: %w", err)
		}
		pass := strings.TrimSpace(string(b))
		if pass == "" {
			// An empty / whitespace-only read is a mid-rotation truncation (a sidecar that
			// rewrites in place rather than atomically renaming): treat it as a transient
			// read error so we NEVER submit an empty AUTH, and the next non-empty read
			// self-heals. Distinct from a missing file (an OS error above).
			return "", "", fmt.Errorf("event-bus password file is empty (mid-rotation?)")
		}
		return cfg.Username, pass, nil
	}
}

// ErrPermanentAuth marks an authentication failure that retrying cannot fix — bad
// credentials, wrong ACL user, or a missing-AUTH server (invariant #12: classify
// permanent vs transient). The caller must surface/alert on it rather than re-poll
// forever, which would pin a connection hammering a server that will keep rejecting it.
// A transient network error is NOT this — it stays retriable.
var ErrPermanentAuth = errors.New("event-bus authentication failed (permanent — check credentials/ACL user)")

// classifyAuth wraps a permanent Redis AUTH rejection as ErrPermanentAuth, leaving every
// other error (network, timeout, MOVED) untouched so it remains transient/retriable.
// Redis replies with WRONGPASS / NOAUTH / "invalid username-password" on a credential
// failure — none of which a retry resolves.
func classifyAuth(err error) error {
	if err == nil {
		return nil
	}
	m := err.Error()
	if strings.Contains(m, "WRONGPASS") || strings.Contains(m, "NOAUTH") ||
		strings.Contains(m, "invalid username-password") || strings.Contains(m, "Client sent AUTH") {
		return fmt.Errorf("%w: %v", ErrPermanentAuth, err)
	}
	return err
}

// Dial builds a client. With a MasterName+SentinelAddrs it returns a
// Sentinel-failover client (auth carried to both data nodes and Sentinels,
// exponential retry, automatic master re-resolution on failover); otherwise a
// single-endpoint client from the URL. Both share the keepalive + compat tuning.
func Dial(ctx context.Context, cfg Config) (*redis.Client, error) {
	provider := cfg.credentialsProvider()
	var rdb *redis.Client
	switch {
	case cfg.MasterName != "" && len(cfg.SentinelAddrs) > 0:
		rdb = redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:    cfg.MasterName,
			SentinelAddrs: cfg.SentinelAddrs,
			// Static creds (ignored when a refreshing provider is set); the provider
			// carries the rotating managed-cloud token instead (#126).
			Username:                   cfg.Username,
			Password:                   cfg.Password,
			SentinelPassword:           cfg.SentinelPassword,
			CredentialsProviderContext: provider,
			MaxRetries:                 5,                      // R-EV1: exponential retry across a failover
			MinRetryBackoff:            8 * time.Millisecond,   // go-redis backs off exponentially between
			MaxRetryBackoff:            512 * time.Millisecond, // MinRetryBackoff..MaxRetryBackoff
			DialTimeout:                5 * time.Second,
			ReadTimeout:                3 * time.Second,
			WriteTimeout:               3 * time.Second,
			// R-EV2: keep idle pooled connections healthy so a blocking/slow read on an
			// idle stream is not mistaken for a dead socket — go-redis health-checks a
			// connection idle longer than this before reuse and transparently redials.
			ConnMaxIdleTime: 30 * time.Second,
			PoolTimeout:     4 * time.Second,
			// R-EV3: skip the CLIENT SETINFO identity handshake that some Valkey/
			// Dragonfly/KeyDB versions reject — no hard Redis-version gate.
			DisableIndentity: true,
		})
	case cfg.URL != "":
		opt, err := redis.ParseURL(cfg.URL)
		if err != nil {
			// Do NOT wrap the parse error: url.Parse's error echoes the full DSN,
			// which carries the password inline (redis://:pass@host). Return a
			// credential-free message instead.
			return nil, fmt.Errorf("invalid EVENT_REDIS_URL (redacted): malformed redis DSN")
		}
		opt.MaxRetries = 5
		opt.MinRetryBackoff = 8 * time.Millisecond
		opt.MaxRetryBackoff = 512 * time.Millisecond
		opt.ConnMaxIdleTime = 30 * time.Second
		opt.PoolTimeout = 4 * time.Second
		opt.DisableIndentity = true
		// Managed-cloud auth (#126): a username (ACL/IAM user) and/or a refreshing
		// credentials provider override the DSN's inline password. The provider is
		// re-invoked on each (re)connect, so a rotated token is honored live.
		if cfg.Username != "" {
			opt.Username = cfg.Username
		}
		if provider != nil {
			opt.CredentialsProviderContext = provider
		}
		rdb = redis.NewClient(opt)
	default:
		return nil, fmt.Errorf("redis config: set URL or MasterName+SentinelAddrs")
	}

	// Fail closed if the endpoint is unreachable at boot (feature-detect via PING,
	// not a version gate).
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pctx).Err(); err != nil {
		_ = rdb.Close()
		// Classify at the boot boundary (#12): bad credentials fail loud as a permanent
		// error (retrying the boot won't help), while an unreachable endpoint stays a
		// plain transient dial error the operator can retry.
		return nil, fmt.Errorf("redis ping: %w", classifyAuth(err))
	}
	return rdb, nil
}
