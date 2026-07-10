package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/bus"
)

// EventStore is the Postgres bus.Store: a durable event log with per-subscriber
// offsets and a dead-letter table. It holds only the persistence; the replay /
// at-least-once / backlog-cap / DLQ logic is in bus.Bus (shared across backends).
type EventStore struct {
	pool *pgxpool.Pool
}

func NewEventStore(pool *pgxpool.Pool) *EventStore { return &EventStore{pool: pool} }

var _ bus.Store = (*EventStore)(nil)

func (s *EventStore) Append(ctx context.Context, topic, projectID, subjectID string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO plugin_event_log (topic, project_id, subject_id) VALUES ($1,$2,$3) RETURNING id`,
		topic, projectID, subjectID).Scan(&id)
	return id, err
}

func (s *EventStore) LatestID(ctx context.Context, topic, projectID string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(id),0) FROM plugin_event_log WHERE topic=$1 AND project_id=$2`,
		topic, projectID).Scan(&id)
	return id, err
}

func (s *EventStore) After(ctx context.Context, topic, projectID string, afterID int64, limit int) ([]bus.Delivered, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, topic, project_id, subject_id FROM plugin_event_log
		 WHERE topic=$1 AND project_id=$2 AND id > $3 ORDER BY id ASC LIMIT $4`,
		topic, projectID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bus.Delivered
	for rows.Next() {
		var d bus.Delivered
		if err := rows.Scan(&d.ID, &d.Topic, &d.ProjectID, &d.SubjectID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *EventStore) Offset(ctx context.Context, pluginID, projectID, topic string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE((SELECT offset_id FROM plugin_event_offsets WHERE plugin_id=$1 AND project_id=$2 AND topic=$3), 0)`,
		pluginID, projectID, topic).Scan(&id)
	return id, err
}

func (s *EventStore) SetOffset(ctx context.Context, pluginID, projectID, topic string, id int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO plugin_event_offsets (plugin_id, project_id, topic, offset_id, updated_at)
		 VALUES ($1,$2,$3,$4, now())
		 ON CONFLICT (plugin_id, project_id, topic) DO UPDATE SET offset_id=GREATEST(plugin_event_offsets.offset_id, EXCLUDED.offset_id), updated_at=now()`,
		pluginID, projectID, topic, id)
	return err
}

func (s *EventStore) DeadLetter(ctx context.Context, pluginID, projectID, topic string, fromID, toID int64, reason string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO plugin_event_dlq (plugin_id, project_id, topic, from_id, to_id, reason) VALUES ($1,$2,$3,$4,$5,$6)`,
		pluginID, projectID, topic, fromID, toID, reason)
	return err
}
