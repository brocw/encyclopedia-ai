// Package jobs turns article generation into a background job with a
// durable event log.
//
// A reasoning run takes tens of minutes, which is far longer than any load
// balancer, mobile connection, or patient user will hold an HTTP request
// open. So the request only enqueues the work: a worker runs the pipeline and
// appends events to a log, and clients subscribe to that log and can resume
// from where they left off.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"encyclopedia-ai/internal/orchestrator"
)

// Status is the lifecycle of one job.
type Status string

const (
	StatusQueued   Status = "queued"
	StatusRunning  Status = "running"
	StatusComplete Status = "complete"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
)

// Terminal reports whether the job has stopped for good.
func (s Status) Terminal() bool {
	return s == StatusComplete || s == StatusFailed || s == StatusCanceled
}

// Job is one article generation request and its outcome.
type Job struct {
	ID        string    `json:"id"`
	Topic     string    `json:"topic"`
	MaxRounds int       `json:"max_rounds"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"created_at"`

	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// State is the pipeline result. It is present once the job finishes, and
	// may be a partial state when the job failed part way through.
	State *orchestrator.ArticleState `json:"state,omitempty"`
	Error string                     `json:"error,omitempty"`
}

// Event is one entry in a job's log. Seq starts at 1 and is contiguous per
// job, so a client that has seen Seq N resumes by asking for events after N.
type Event struct {
	Seq  int64           `json:"seq"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Store persists jobs and their event logs.
//
// Implementations must make Append atomic with respect to sequence numbers:
// two concurrent appends to the same job may not produce the same Seq.
type Store interface {
	// Create records a newly queued job.
	Create(ctx context.Context, job Job) error

	// Job returns one job, or ErrNotFound.
	Job(ctx context.Context, id string) (Job, error)

	// Save overwrites a job's mutable fields.
	Save(ctx context.Context, job Job) error

	// Claim atomically takes the oldest queued job and marks it running.
	// The boolean reports whether a job was available.
	Claim(ctx context.Context) (Job, bool, error)

	// Append assigns sequence numbers and stores the events, returning the
	// sequence number of the last one.
	Append(ctx context.Context, jobID string, events []Event) (int64, error)

	// Events returns the job's events with Seq greater than after, oldest
	// first, at most limit of them.
	Events(ctx context.Context, jobID string, after int64, limit int) ([]Event, error)
}

// ErrNotFound is returned for an unknown job. ErrDuplicate is returned when
// an identifier is already taken.
var (
	ErrNotFound  = fmt.Errorf("job not found")
	ErrDuplicate = fmt.Errorf("job already exists")
)

// NewID returns a random job identifier.
func NewID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// ValidID reports whether an identifier is one NewID could have produced.
// Handlers check this before touching the store, so a hostile path segment
// never reaches a storage backend.
func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// NewJob builds a queued job for a topic.
func NewJob(topic string, maxRounds int) (Job, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return Job{}, fmt.Errorf("topic cannot be empty")
	}
	if maxRounds <= 0 {
		return Job{}, fmt.Errorf("max rounds must be greater than zero")
	}

	id, err := NewID()
	if err != nil {
		return Job{}, err
	}
	return Job{
		ID:        id,
		Topic:     topic,
		MaxRounds: maxRounds,
		Status:    StatusQueued,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// event builds a log entry from any JSON-serialisable payload.
func event(eventType string, payload any) (Event, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode %s event: %w", eventType, err)
	}
	return Event{Type: eventType, Data: data}, nil
}
