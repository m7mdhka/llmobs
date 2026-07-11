package redisstore

import (
	"context"
	"fmt"
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
}

// Dial builds a client. With a MasterName+SentinelAddrs it returns a
// Sentinel-failover client (auth carried to both data nodes and Sentinels,
// exponential retry, automatic master re-resolution on failover); otherwise a
// single-endpoint client from the URL. Both share the keepalive + compat tuning.
func Dial(ctx context.Context, cfg Config) (*redis.Client, error) {
	var rdb *redis.Client
	switch {
	case cfg.MasterName != "" && len(cfg.SentinelAddrs) > 0:
		rdb = redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:       cfg.MasterName,
			SentinelAddrs:    cfg.SentinelAddrs,
			Password:         cfg.Password,
			SentinelPassword: cfg.SentinelPassword,
			MaxRetries:       5,                      // R-EV1: exponential retry across a failover
			MinRetryBackoff:  8 * time.Millisecond,   // go-redis backs off exponentially between
			MaxRetryBackoff:  512 * time.Millisecond, // MinRetryBackoff..MaxRetryBackoff
			DialTimeout:      5 * time.Second,
			ReadTimeout:      3 * time.Second,
			WriteTimeout:     3 * time.Second,
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
			return nil, fmt.Errorf("parse redis url: %w", err)
		}
		opt.MaxRetries = 5
		opt.MinRetryBackoff = 8 * time.Millisecond
		opt.MaxRetryBackoff = 512 * time.Millisecond
		opt.ConnMaxIdleTime = 30 * time.Second
		opt.PoolTimeout = 4 * time.Second
		opt.DisableIndentity = true
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
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return rdb, nil
}
