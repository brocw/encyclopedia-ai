package jobs

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore keeps jobs and events in process memory.
//
// It is the default store and the one the tests use. Everything it holds is
// lost on restart, which is acceptable for a single-process prototype and is
// why a durable implementation satisfies the same interface.
type MemoryStore struct {
	mu     sync.RWMutex
	jobs   map[string]Job
	events map[string][]Event
	queue  []string
}

// NewMemoryStore builds an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		jobs:   make(map[string]Job),
		events: make(map[string][]Event),
	}
}

func (s *MemoryStore) Create(_ context.Context, job Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.jobs[job.ID]; exists {
		return ErrDuplicate
	}
	s.jobs[job.ID] = job
	if job.Status == StatusQueued {
		s.queue = append(s.queue, job.ID)
	}
	return nil
}

func (s *MemoryStore) Job(_ context.Context, id string) (Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, found := s.jobs[id]
	if !found {
		return Job{}, ErrNotFound
	}
	return job, nil
}

func (s *MemoryStore) Save(_ context.Context, job Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, found := s.jobs[job.ID]; !found {
		return ErrNotFound
	}
	s.jobs[job.ID] = job
	return nil
}

// Claim takes the oldest queued job. A job cancelled while it sat in the
// queue is dropped rather than run.
func (s *MemoryStore) Claim(_ context.Context) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for len(s.queue) > 0 {
		id := s.queue[0]
		s.queue = s.queue[1:]

		job, found := s.jobs[id]
		if !found || job.Status != StatusQueued {
			continue
		}

		started := time.Now().UTC()
		job.Status = StatusRunning
		job.StartedAt = &started
		s.jobs[id] = job
		return job, true, nil
	}
	return Job{}, false, nil
}

func (s *MemoryStore) Append(_ context.Context, jobID string, events []Event) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, found := s.jobs[jobID]; !found {
		return 0, ErrNotFound
	}

	log := s.events[jobID]
	next := int64(len(log))
	for _, entry := range events {
		next++
		entry.Seq = next
		log = append(log, entry)
	}
	s.events[jobID] = log
	return next, nil
}

func (s *MemoryStore) Events(_ context.Context, jobID string, after int64, limit int) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, found := s.jobs[jobID]; !found {
		return nil, ErrNotFound
	}

	log := s.events[jobID]
	index := sort.Search(len(log), func(i int) bool { return log[i].Seq > after })
	remaining := log[index:]
	if limit > 0 && len(remaining) > limit {
		remaining = remaining[:limit]
	}

	out := make([]Event, len(remaining))
	copy(out, remaining)
	return out, nil
}
