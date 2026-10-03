package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// jobCols is the column list every read shares, so a scan can never drift from a
// query.
const jobCols = "job_id, user_id, name, job_type, schedule, payload, enabled, next_run_at, last_run_at, last_status, last_error, run_count"

// claimHorizon is how far a claim pushes next_run_at. It has to exceed the
// longest handler's runtime (the billing reset walks every tenant), because a
// crash between Claim and Finish leaves the row at this mark: too short and two
// processes run the same occurrence, too long and a crashed job is stalled.
const claimHorizon = 15 * time.Minute

// PostgresStore is the production Store, backed by scheduled_jobs (migration 066).
type PostgresStore struct{ DB *pgxpool.Pool }

func NewPostgresStore(db *pgxpool.Pool) *PostgresStore { return &PostgresStore{DB: db} }

func scanJob(row pgx.Row) (Job, error) {
	var (
		job     Job
		payload []byte
	)
	err := row.Scan(&job.ID, &job.UserID, &job.Name, &job.Type, &job.Schedule, &payload,
		&job.Enabled, &job.NextRun, &job.LastRun, &job.LastStatus, &job.LastError, &job.RunCount)
	if err != nil {
		return Job{}, err
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &job.Payload)
	}
	return job, nil
}

func (s *PostgresStore) Due(ctx context.Context, now time.Time, limit int) ([]Job, error) {
	if s.DB == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.DB.Query(ctx,
		"SELECT "+jobCols+" FROM scheduled_jobs WHERE enabled AND next_run_at <= $1 ORDER BY next_run_at LIMIT $2",
		now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// Claim takes one occurrence by pushing next_run_at past the horizon. The row
// stops being due, so a second runner cannot pick up the same occurrence — the
// claim IS the lock, with no extra column to keep consistent.
func (s *PostgresStore) Claim(ctx context.Context, jobID int64, now time.Time) (bool, error) {
	if s.DB == nil {
		return false, nil
	}
	tag, err := s.DB.Exec(ctx,
		"UPDATE scheduled_jobs SET next_run_at = $2, updated_at = NOW() WHERE job_id = $1 AND enabled AND next_run_at <= $3",
		jobID, now.Add(claimHorizon), now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PostgresStore) Finish(ctx context.Context, job Job, status, lastErr string, next time.Time) error {
	if s.DB == nil {
		return fmt.Errorf("scheduler: no database")
	}
	_, err := s.DB.Exec(ctx,
		"UPDATE scheduled_jobs SET last_run_at = NOW(), last_status = $2, last_error = $3, "+
			"run_count = run_count + 1, next_run_at = $4, updated_at = NOW() WHERE job_id = $1",
		job.ID, status, lastErr, next)
	return err
}

func (s *PostgresStore) Insert(ctx context.Context, job Job) (int64, error) {
	if s.DB == nil {
		return 0, fmt.Errorf("scheduler: no database")
	}
	payload, _ := json.Marshal(job.Payload)
	var id int64
	err := s.DB.QueryRow(ctx,
		"INSERT INTO scheduled_jobs (user_id, name, job_type, schedule, payload, enabled, next_run_at) "+
			"VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING job_id",
		job.UserID, job.Name, job.Type, job.Schedule, payload, job.Enabled, job.NextRun).Scan(&id)
	return id, err
}

func (s *PostgresStore) Update(ctx context.Context, job Job) error {
	if s.DB == nil {
		return fmt.Errorf("scheduler: no database")
	}
	payload, _ := json.Marshal(job.Payload)
	_, err := s.DB.Exec(ctx,
		"UPDATE scheduled_jobs SET name=$2, job_type=$3, schedule=$4, payload=$5, enabled=$6, "+
			"next_run_at=$7, updated_at=NOW() WHERE job_id=$1",
		job.ID, job.Name, job.Type, job.Schedule, payload, job.Enabled, job.NextRun)
	return err
}

func (s *PostgresStore) Delete(ctx context.Context, jobID int64) error {
	if s.DB == nil {
		return fmt.Errorf("scheduler: no database")
	}
	_, err := s.DB.Exec(ctx, "DELETE FROM scheduled_jobs WHERE job_id = $1", jobID)
	return err
}

// List returns platform-level jobs when userID is nil, otherwise that tenant's.
func (s *PostgresStore) List(ctx context.Context, userID *int32) ([]Job, error) {
	if s.DB == nil {
		return nil, nil
	}
	var (
		rows pgx.Rows
		err  error
	)
	if userID == nil {
		rows, err = s.DB.Query(ctx, "SELECT "+jobCols+" FROM scheduled_jobs WHERE user_id IS NULL ORDER BY job_id")
	} else {
		rows, err = s.DB.Query(ctx, "SELECT "+jobCols+" FROM scheduled_jobs WHERE user_id = $1 ORDER BY job_id", *userID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}
