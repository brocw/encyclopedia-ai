package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"encyclopedia-ai/internal/orchestrator"

	_ "modernc.org/sqlite"
)

// SQLiteStore keeps jobs and their event logs in a SQLite file, so a restart
// or a deploy does not lose work that is already finished or in flight.
//
// The driver is pure Go, which keeps CGO_ENABLED=0 and the single static
// binary intact.
type SQLiteStore struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
    id          TEXT PRIMARY KEY,
    topic       TEXT    NOT NULL,
    max_rounds  INTEGER NOT NULL,
    status      TEXT    NOT NULL,
    created_at  TEXT    NOT NULL,
    started_at  TEXT,
    finished_at TEXT,
    state       TEXT,
    error       TEXT    NOT NULL DEFAULT ''
);

-- Claim orders by created_at within a status, so the queue is one index scan.
CREATE INDEX IF NOT EXISTS jobs_queue ON jobs (status, created_at, id);

CREATE TABLE IF NOT EXISTS events (
    job_id TEXT    NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    seq    INTEGER NOT NULL,
    type   TEXT    NOT NULL,
    data   BLOB    NOT NULL,
    PRIMARY KEY (job_id, seq)
) WITHOUT ROWID;
`

// OpenSQLite opens or creates the database at path and applies the schema.
func OpenSQLite(path string) (*SQLiteStore, error) {
	// WAL lets readers run while a job is being written. busy_timeout covers
	// the brief moments when two writers do collide.
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite at %s: %w", path, err)
	}

	// SQLite serialises writes anyway. One connection removes SQLITE_BUSY as
	// a failure mode entirely, and every query here is small and indexed.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

// Close releases the database.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// Recover marks jobs that were running when the process died. Their worker no
// longer exists, so without this they would sit in "running" for ever.
func (s *SQLiteStore) Recover(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, error = ?, finished_at = ?
		 WHERE status = ?`,
		StatusFailed, "interrupted by a server restart", formatTime(time.Now().UTC()), StatusRunning)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted jobs: %w", err)
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func parseTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, fmt.Errorf("parse timestamp %q: %w", value.String, err)
	}
	return &parsed, nil
}

const jobColumns = `id, topic, max_rounds, status, created_at, started_at, finished_at, state, error`

// scanJob reads one row in jobColumns order.
func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var (
		job        Job
		createdAt  string
		startedAt  sql.NullString
		finishedAt sql.NullString
		state      sql.NullString
	)
	if err := row.Scan(&job.ID, &job.Topic, &job.MaxRounds, &job.Status,
		&createdAt, &startedAt, &finishedAt, &state, &job.Error); err != nil {
		return Job{}, err
	}

	parsedCreated, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Job{}, fmt.Errorf("parse created_at %q: %w", createdAt, err)
	}
	job.CreatedAt = parsedCreated

	if job.StartedAt, err = parseTime(startedAt); err != nil {
		return Job{}, err
	}
	if job.FinishedAt, err = parseTime(finishedAt); err != nil {
		return Job{}, err
	}
	if state.Valid && state.String != "" {
		var decoded orchestrator.ArticleState
		if err := json.Unmarshal([]byte(state.String), &decoded); err != nil {
			return Job{}, fmt.Errorf("decode stored state: %w", err)
		}
		job.State = &decoded
	}
	return job, nil
}

func encodeState(state *orchestrator.ArticleState) (any, error) {
	if state == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode state: %w", err)
	}
	return string(encoded), nil
}

func (s *SQLiteStore) Create(ctx context.Context, job Job) error {
	state, err := encodeState(job.State)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO jobs (`+jobColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Topic, job.MaxRounds, job.Status, formatTime(job.CreatedAt),
		nullableTime(job.StartedAt), nullableTime(job.FinishedAt), state, job.Error)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("insert job: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Job(ctx context.Context, id string) (Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("read job: %w", err)
	}
	return job, nil
}

func (s *SQLiteStore) Save(ctx context.Context, job Job) error {
	state, err := encodeState(job.State)
	if err != nil {
		return err
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET topic = ?, max_rounds = ?, status = ?, started_at = ?,
		                 finished_at = ?, state = ?, error = ?
		 WHERE id = ?`,
		job.Topic, job.MaxRounds, job.Status, nullableTime(job.StartedAt),
		nullableTime(job.FinishedAt), state, job.Error, job.ID)
	if err != nil {
		return fmt.Errorf("update job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update job: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Claim takes the oldest queued job and marks it running in one statement, so
// two workers can never be handed the same job.
func (s *SQLiteStore) Claim(ctx context.Context) (Job, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`UPDATE jobs SET status = ?, started_at = ?
		 WHERE id = (
		     SELECT id FROM jobs WHERE status = ? ORDER BY created_at, id LIMIT 1
		 )
		 RETURNING `+jobColumns,
		StatusRunning, formatTime(time.Now().UTC()), StatusQueued)

	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("claim job: %w", err)
	}
	return job, true, nil
}

// Append assigns contiguous sequence numbers inside one transaction, so a
// concurrent append cannot reuse a number.
func (s *SQLiteStore) Append(ctx context.Context, jobID string, events []Event) (int64, error) {
	if len(events) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin append: %w", err)
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE id = ?`, jobID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("append: %w", err)
	}

	var last int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM events WHERE job_id = ?`, jobID).Scan(&last); err != nil {
		return 0, fmt.Errorf("read last sequence: %w", err)
	}

	statement, err := tx.PrepareContext(ctx,
		`INSERT INTO events (job_id, seq, type, data) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare append: %w", err)
	}
	defer statement.Close()

	for _, entry := range events {
		last++
		if _, err := statement.ExecContext(ctx, jobID, last, entry.Type, []byte(entry.Data)); err != nil {
			return 0, fmt.Errorf("insert event: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit append: %w", err)
	}
	return last, nil
}

func (s *SQLiteStore) Events(ctx context.Context, jobID string, after int64, limit int) ([]Event, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE id = ?`, jobID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read events: %w", err)
	}

	if limit <= 0 {
		limit = -1 // SQLite treats a negative LIMIT as unbounded.
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, type, data FROM events
		 WHERE job_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
		jobID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var entry Event
		var data []byte
		if err := rows.Scan(&entry.Seq, &entry.Type, &data); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		entry.Data = json.RawMessage(data)
		events = append(events, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	return events, nil
}

func isUniqueViolation(err error) bool {
	// The pure-Go driver reports constraint failures in the message; matching
	// on it avoids depending on the driver's error type.
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
