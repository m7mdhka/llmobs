package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/jobs"
)

// JobStore is the Postgres jobs.Store (the plugin_job_runs audit trail).
type JobStore struct {
	pool *pgxpool.Pool
}

func NewJobStore(pool *pgxpool.Pool) *JobStore { return &JobStore{pool: pool} }

var _ jobs.Store = (*JobStore)(nil)

func (s *JobStore) StartRun(ctx context.Context, r jobs.Run) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO plugin_job_runs (id, plugin_id, job, trigger, actor, status, attempts, started_at)
		 VALUES ($1,$2,$3,$4,$5,'running',0, now())`,
		r.ID, r.PluginID, r.Job, r.Trigger, r.Actor)
	return err
}

func (s *JobStore) FinishRun(ctx context.Context, id, status, errMsg string, attempts int) error {
	var errPtr *string
	if errMsg != "" {
		errPtr = &errMsg
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE plugin_job_runs SET status=$2, error=$3, attempts=$4, finished_at=now() WHERE id=$1`,
		id, status, errPtr, attempts)
	return err
}

func (s *JobStore) IsRunning(ctx context.Context, pluginID, job string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM plugin_job_runs WHERE plugin_id=$1 AND job=$2 AND status='running'`,
		pluginID, job).Scan(&n)
	return n > 0, err
}

func (s *JobStore) LastRun(ctx context.Context, pluginID, job string) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT max(started_at) FROM plugin_job_runs WHERE plugin_id=$1 AND job=$2`,
		pluginID, job).Scan(&t)
	if err != nil || t == nil {
		return time.Time{}, err
	}
	return *t, nil
}

func (s *JobStore) RecentRuns(ctx context.Context, pluginID string, limit int) ([]jobs.Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, plugin_id, job, trigger, actor, status, attempts, COALESCE(error,''), started_at, finished_at
		 FROM plugin_job_runs WHERE plugin_id=$1 ORDER BY started_at DESC LIMIT $2`, pluginID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []jobs.Run
	for rows.Next() {
		var r jobs.Run
		var fin *time.Time
		if err := rows.Scan(&r.ID, &r.PluginID, &r.Job, &r.Trigger, &r.Actor, &r.Status, &r.Attempts, &r.Error, &r.StartedAt, &fin); err != nil {
			return nil, err
		}
		if fin != nil {
			r.FinishedAt = *fin
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
