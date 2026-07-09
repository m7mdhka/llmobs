package platform

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPGPool opens a pgx connection pool.
func NewPGPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConnIdleTime = 5 * time.Minute
	return pgxpool.NewWithConfig(ctx, cfg)
}
