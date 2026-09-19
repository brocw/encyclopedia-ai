package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"encyclopedia-ai/internal/orchestrator"
)

// Every Store implementation must satisfy the same contract, so the suite is
// written once and run against each of them.
func forEachStore(t *testing.T, test func(*testing.T, Store)) {
	t.Helper()

	t.Run("memory", func(t *testing.T) {
		test(t, NewMemoryStore())
	})

	t.Run("sqlite", func(t *testing.T) {
		store, err := OpenSQLite(filepath.Join(t.TempDir(), "jobs.db"))
		if err != nil {
			t.Fatalf("OpenSQLite returned error: %v", err)
		}
		t.Cleanup(func() { store.Close() })
		test(t, store)
	})
}

// articleState is a representative pipeline result used to prove that a
// non-trivial state survives storage.
var articleState = orchestrator.ArticleState{
	Topic:          "Bacon",
	CurrentArticle: "Bacon is cured pork.",
	References:     `{"references":[]}`,
	Status:         orchestrator.StatusComplete,
	Converged:      true,
	Rounds: []orchestrator.Round{{
		Number:     1,
		Article:    "Bacon is cured pork.",
		Evaluation: orchestrator.Evaluation{Overall: 9},
	}},
}

func mustCreate(t *testing.T, store Store, topic string) Job {
	t.Helper()
	job, err := NewJob(topic, 1)
	if err != nil {
		t.Fatalf("NewJob returned error: %v", err)
	}
	if err := store.Create(context.Background(), job); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	return job
}

func TestStoreRoundTripsAJob(t *testing.T) {
	forEachStore(t, func(t *testing.T, store Store) {
		created := mustCreate(t, store, "Bacon")

		loaded, err := store.Job(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("Job returned error: %v", err)
		}
		if loaded.Topic != "Bacon" || loaded.Status != StatusQueued || loaded.MaxRounds != 1 {
			t.Fatalf("loaded = %+v", loaded)
		}
		if !loaded.CreatedAt.Equal(created.CreatedAt) {
			t.Errorf("created_at = %v, want %v", loaded.CreatedAt, created.CreatedAt)
		}
		if loaded.StartedAt != nil || loaded.FinishedAt != nil || loaded.State != nil {
			t.Errorf("unset fields came back populated: %+v", loaded)
		}
	})
}

// The article state must survive a round trip, since it is what a client
// reading a finished job receives.
func TestStoreRoundTripsArticleState(t *testing.T) {
	forEachStore(t, func(t *testing.T, store Store) {
		job := mustCreate(t, store, "Bacon")

		claimed, found, err := store.Claim(context.Background())
		if err != nil || !found {
			t.Fatalf("Claim returned %v, %v", found, err)
		}

		claimed.Status = StatusComplete
		claimed.State = &articleState
		if err := store.Save(context.Background(), claimed); err != nil {
			t.Fatalf("Save returned error: %v", err)
		}

		loaded, err := store.Job(context.Background(), job.ID)
		if err != nil {
			t.Fatalf("Job returned error: %v", err)
		}
		if loaded.State == nil {
			t.Fatal("state was not persisted")
		}
		if loaded.State.CurrentArticle != articleState.CurrentArticle {
			t.Errorf("article = %q", loaded.State.CurrentArticle)
		}
		if len(loaded.State.Rounds) != 1 || loaded.State.Rounds[0].Evaluation.Overall != 9 {
			t.Errorf("rounds = %+v", loaded.State.Rounds)
		}
		if loaded.StartedAt == nil {
			t.Error("Claim did not record a start time")
		}
	})
}

// Two workers must never be handed the same job.
func TestStoreClaimHandsEachJobToOneWorker(t *testing.T) {
	forEachStore(t, func(t *testing.T, store Store) {
		const total = 30
		for i := 0; i < total; i++ {
			mustCreate(t, store, "Topic")
		}

		var mu sync.Mutex
		claimed := map[string]int{}
		var wg sync.WaitGroup
		for worker := 0; worker < 8; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					job, found, err := store.Claim(context.Background())
					if err != nil {
						t.Errorf("Claim returned error: %v", err)
						return
					}
					if !found {
						return
					}
					mu.Lock()
					claimed[job.ID]++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		if len(claimed) != total {
			t.Fatalf("claimed %d distinct jobs, want %d", len(claimed), total)
		}
		for id, times := range claimed {
			if times != 1 {
				t.Fatalf("job %s was claimed %d times", id, times)
			}
		}
	})
}

func TestStoreClaimReportsAnEmptyQueue(t *testing.T) {
	forEachStore(t, func(t *testing.T, store Store) {
		if _, found, err := store.Claim(context.Background()); err != nil || found {
			t.Fatalf("Claim on an empty queue returned %v, %v", found, err)
		}
	})
}

// Sequence numbers must be contiguous from one: that is what a resuming
// client relies on to know it has missed nothing.
func TestStoreAssignsContiguousSequenceNumbers(t *testing.T) {
	forEachStore(t, func(t *testing.T, store Store) {
		job := mustCreate(t, store, "Bacon")

		last, err := store.Append(context.Background(), job.ID, []Event{
			{Type: EventToken, Data: []byte(`{"stream":"article","text":"a"}`)},
			{Type: EventToken, Data: []byte(`{"stream":"article","text":"b"}`)},
		})
		if err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
		if last != 2 {
			t.Fatalf("last sequence = %d, want 2", last)
		}

		if last, err = store.Append(context.Background(), job.ID, []Event{{Type: EventDone, Data: []byte(`{}`)}}); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
		if last != 3 {
			t.Fatalf("last sequence = %d, want 3", last)
		}

		events, err := store.Events(context.Background(), job.ID, 0, 0)
		if err != nil {
			t.Fatalf("Events returned error: %v", err)
		}
		if len(events) != 3 {
			t.Fatalf("events = %d, want 3", len(events))
		}
		for i, entry := range events {
			if entry.Seq != int64(i+1) {
				t.Fatalf("event %d has Seq %d", i, entry.Seq)
			}
		}
		if string(events[0].Data) != `{"stream":"article","text":"a"}` {
			t.Errorf("payload = %s", events[0].Data)
		}
	})
}

// Two jobs number their events independently.
func TestStoreSequencesAreScopedToAJob(t *testing.T) {
	forEachStore(t, func(t *testing.T, store Store) {
		first := mustCreate(t, store, "First")
		second := mustCreate(t, store, "Second")

		if _, err := store.Append(context.Background(), first.ID, []Event{{Type: EventToken, Data: []byte(`{}`)}}); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
		last, err := store.Append(context.Background(), second.ID, []Event{{Type: EventToken, Data: []byte(`{}`)}})
		if err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
		if last != 1 {
			t.Fatalf("second job's first event has Seq %d, want 1", last)
		}
	})
}

func TestStoreEventsResumeFromASequenceNumber(t *testing.T) {
	forEachStore(t, func(t *testing.T, store Store) {
		job := mustCreate(t, store, "Bacon")
		for i := 0; i < 5; i++ {
			if _, err := store.Append(context.Background(), job.ID, []Event{{Type: EventToken, Data: []byte(`{}`)}}); err != nil {
				t.Fatalf("Append returned error: %v", err)
			}
		}

		events, err := store.Events(context.Background(), job.ID, 3, 0)
		if err != nil {
			t.Fatalf("Events returned error: %v", err)
		}
		if len(events) != 2 || events[0].Seq != 4 || events[1].Seq != 5 {
			t.Fatalf("resumed events = %+v", events)
		}

		limited, err := store.Events(context.Background(), job.ID, 0, 2)
		if err != nil {
			t.Fatalf("Events returned error: %v", err)
		}
		if len(limited) != 2 || limited[0].Seq != 1 {
			t.Fatalf("limited events = %+v", limited)
		}

		none, err := store.Events(context.Background(), job.ID, 5, 0)
		if err != nil {
			t.Fatalf("Events returned error: %v", err)
		}
		if len(none) != 0 {
			t.Fatalf("reading past the end returned %d events", len(none))
		}
	})
}

func TestStoreReportsUnknownJobs(t *testing.T) {
	forEachStore(t, func(t *testing.T, store Store) {
		missing := "ffffffffffffffffffffffffffffffff"

		if _, err := store.Job(context.Background(), missing); !errors.Is(err, ErrNotFound) {
			t.Errorf("Job error = %v, want ErrNotFound", err)
		}
		if _, err := store.Events(context.Background(), missing, 0, 0); !errors.Is(err, ErrNotFound) {
			t.Errorf("Events error = %v, want ErrNotFound", err)
		}
		if _, err := store.Append(context.Background(), missing, []Event{{Type: EventToken, Data: []byte(`{}`)}}); !errors.Is(err, ErrNotFound) {
			t.Errorf("Append error = %v, want ErrNotFound", err)
		}
		if err := store.Save(context.Background(), Job{ID: missing, Status: StatusQueued}); !errors.Is(err, ErrNotFound) {
			t.Errorf("Save error = %v, want ErrNotFound", err)
		}
	})
}

func TestStoreRejectsADuplicateJob(t *testing.T) {
	forEachStore(t, func(t *testing.T, store Store) {
		job := mustCreate(t, store, "Bacon")
		if err := store.Create(context.Background(), job); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("Create error = %v, want ErrDuplicate", err)
		}
	})
}

// The point of a durable store: a job survives the process that made it.
func TestSQLiteSurvivesReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")

	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite returned error: %v", err)
	}
	job := mustCreate(t, store, "Bacon")
	if _, err := store.Append(context.Background(), job.ID, []Event{
		{Type: EventToken, Data: []byte(`{"stream":"article","text":"persisted"}`)},
	}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	claimed, _, _ := store.Claim(context.Background())
	claimed.Status = StatusComplete
	claimed.State = &articleState
	if err := store.Save(context.Background(), claimed); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	reopened, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopening returned error: %v", err)
	}
	defer reopened.Close()

	loaded, err := reopened.Job(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Job after reopen returned error: %v", err)
	}
	if loaded.Status != StatusComplete || loaded.State == nil {
		t.Fatalf("job did not survive: %+v", loaded)
	}
	if loaded.State.CurrentArticle != articleState.CurrentArticle {
		t.Errorf("article = %q", loaded.State.CurrentArticle)
	}

	events, err := reopened.Events(context.Background(), job.ID, 0, 0)
	if err != nil {
		t.Fatalf("Events after reopen returned error: %v", err)
	}
	if len(events) != 1 || string(events[0].Data) != `{"stream":"article","text":"persisted"}` {
		t.Fatalf("events did not survive: %+v", events)
	}
}

// A job that was running when the process died has no worker any more, so it
// must not be left claiming to be running.
func TestSQLiteRecoverFailsInterruptedJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")

	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite returned error: %v", err)
	}
	interrupted := mustCreate(t, store, "Interrupted")
	queued := mustCreate(t, store, "Queued")
	if _, _, err := store.Claim(context.Background()); err != nil {
		t.Fatalf("Claim returned error: %v", err)
	}
	store.Close()

	reopened, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopening returned error: %v", err)
	}
	defer reopened.Close()

	recovered, err := reopened.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover returned error: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered %d jobs, want 1", recovered)
	}

	wasRunning, _ := reopened.Job(context.Background(), interrupted.ID)
	if wasRunning.Status != StatusFailed || wasRunning.FinishedAt == nil {
		t.Fatalf("interrupted job = %+v", wasRunning)
	}

	// A job still waiting in the queue must be left alone to run.
	stillQueued, _ := reopened.Job(context.Background(), queued.ID)
	if stillQueued.Status != StatusQueued {
		t.Fatalf("queued job = %+v", stillQueued)
	}
}
