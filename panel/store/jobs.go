package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Job statuses.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
)

type Job struct {
	ID         int64           `json:"id"`
	ServerID   int64           `json:"server_id"`
	SiteID     *int64          `json:"site_id"`
	Action     string          `json:"action"`
	Params     json.RawMessage `json:"params"`
	Status     string          `json:"status"`
	Error      string          `json:"error"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
}

const jobCols = `id, server_id, site_id, action, params, status, error, created_at, started_at, finished_at`

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.ServerID, &j.SiteID, &j.Action, &j.Params, &j.Status, &j.Error, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

// CreateJob queues a job. siteID is nil for jobs that aren't about a site.
func (s *Store) CreateJob(ctx context.Context, userID, serverID int64, siteID *int64, action string, params json.RawMessage) (Job, error) {
	if params == nil {
		params = json.RawMessage(`{}`)
	}
	return scanJob(s.db.QueryRow(ctx, `INSERT INTO jobs(user_id, server_id, site_id, action, params)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+jobCols, userID, serverID, siteID, action, params))
}

// ClaimNextJob marks the oldest queued job of a server as running and returns
// it, or ErrNotFound when nothing is queued.
func (s *Store) ClaimNextJob(ctx context.Context, serverID int64) (Job, error) {
	return scanJob(s.db.QueryRow(ctx, `UPDATE jobs SET status='running', started_at=now()
		WHERE id = (SELECT id FROM jobs WHERE server_id=$1 AND status='queued' ORDER BY id LIMIT 1)
		RETURNING `+jobCols, serverID))
}

// RequeueJob puts a running job back in the queue, for when it could not be
// sent to the agent.
func (s *Store) RequeueJob(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `UPDATE jobs SET status='queued', started_at=NULL WHERE id=$1 AND status='running'`, id)
	return err
}

// FinishJob records the outcome of a running job and returns it, or
// ErrNotFound when the job was not running.
func (s *Store) FinishJob(ctx context.Context, id int64, ok bool, errMsg string) (Job, error) {
	status := JobSucceeded
	if !ok {
		status = JobFailed
	}
	return scanJob(s.db.QueryRow(ctx, `UPDATE jobs SET status=$2, error=$3, finished_at=now()
		WHERE id=$1 AND status='running' RETURNING `+jobCols, id, status, errMsg))
}

// FailRunningJobs fails every running job and returns them, for when the
// panel starts and no agent can still be working on one it knows about.
func (s *Store) FailRunningJobs(ctx context.Context, errMsg string) ([]Job, error) {
	return s.queryJobs(ctx, `UPDATE jobs SET status='failed', error=$1, finished_at=now()
		WHERE status='running' RETURNING `+jobCols, errMsg)
}

func (s *Store) queryJobs(ctx context.Context, sql string, args ...any) ([]Job, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *Store) AppendJobLog(ctx context.Context, jobID int64, text string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO job_logs(job_id, text) VALUES ($1, $2)`, jobID, text)
	return err
}

func (s *Store) ListJobs(ctx context.Context, userID, serverID int64, limit int) ([]Job, error) {
	return s.queryJobs(ctx, `SELECT `+jobCols+` FROM jobs WHERE user_id=$1 AND server_id=$2
		ORDER BY id DESC LIMIT $3`, userID, serverID, limit)
}

func (s *Store) ListSiteJobs(ctx context.Context, userID, siteID int64, limit int) ([]Job, error) {
	return s.queryJobs(ctx, `SELECT `+jobCols+` FROM jobs WHERE user_id=$1 AND site_id=$2
		ORDER BY id DESC LIMIT $3`, userID, siteID, limit)
}

func (s *Store) GetJob(ctx context.Context, userID, id int64) (Job, error) {
	return scanJob(s.db.QueryRow(ctx, `SELECT `+jobCols+` FROM jobs WHERE id=$1 AND user_id=$2`, id, userID))
}

type JobLogLine struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// JobLogs returns log chunks of a job with IDs above after, stopping once
// about maxBytes of text have been collected.
func (s *Store) JobLogs(ctx context.Context, jobID, after int64, maxBytes int) ([]JobLogLine, error) {
	rows, err := s.db.Query(ctx, `SELECT id, text FROM job_logs WHERE job_id=$1 AND id>$2 ORDER BY id LIMIT 500`, jobID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := []JobLogLine{}
	size := 0
	for rows.Next() && size < maxBytes {
		var l JobLogLine
		if err := rows.Scan(&l.ID, &l.Text); err != nil {
			return nil, err
		}
		lines = append(lines, l)
		size += len(l.Text)
	}
	return lines, rows.Err()
}
