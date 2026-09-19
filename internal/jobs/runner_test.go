package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"encyclopedia-ai/internal/llm"
	"encyclopedia-ai/internal/orchestrator"
)

// fakeAgent produces a deterministic article without a model.
type fakeAgent struct {
	generateErr error
	block       chan struct{} // when set, GenerateArticle waits on it or the context
}

const passingEvaluation = `{"scores":{"factual_accuracy":9,"completeness":9,"neutrality":9,"clarity":9,"structure":9},"overall":9,"critical_issues":[]}`

func (a *fakeAgent) GenerateArticle(ctx context.Context, _ string, sink llm.Sink) (string, error) {
	if a.generateErr != nil {
		return "", a.generateErr
	}
	if a.block != nil {
		select {
		case <-a.block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	sink.Emit(llm.Delta{Reasoning: "deciding the scope"})
	sink.Emit(llm.Delta{Content: "the article"})
	return "the article", nil
}

func (a *fakeAgent) EvaluateArticle(_ context.Context, _ string, sink llm.Sink) (string, error) {
	sink.Emit(llm.Delta{Content: passingEvaluation})
	return passingEvaluation, nil
}

func (a *fakeAgent) PlanRevision(context.Context, string, string, llm.Sink) (string, error) {
	return `{"instructions":["improve"]}`, nil
}

func (a *fakeAgent) ReviseArticle(context.Context, string, string, string, llm.Sink) (string, error) {
	return "revised", nil
}

func (a *fakeAgent) References(context.Context, string, llm.Sink) (string, error) {
	return `{"references":[]}`, nil
}

func (a *fakeAgent) Infobox(context.Context, string, string, llm.Sink) (string, error) {
	return `{"rows":[]}`, nil
}

func (a *fakeAgent) SeeAlso(context.Context, string, llm.Sink) (string, error) {
	return `{"topics":[]}`, nil
}

func (a *fakeAgent) CategorizeArticle(context.Context, string, llm.Sink) (string, error) {
	return `{"categories":[]}`, nil
}

// startRunner launches a runner that stops when the test ends.
func startRunner(t *testing.T, agent orchestrator.Agent) (*Runner, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	runner := NewRunner(store, agent, NewBroker(), 1)
	runner.FlushInterval = time.Millisecond
	runner.FlushBytes = 8

	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx)
	t.Cleanup(func() {
		cancel()
		runner.Wait()
	})
	return runner, store
}

// awaitStatus waits for a job to reach a terminal state.
func awaitStatus(t *testing.T, store Store, id string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.Job(context.Background(), id)
		if err != nil {
			t.Fatalf("Job returned error: %v", err)
		}
		if job.Status.Terminal() {
			return job
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("job %s never reached a terminal status", id)
	return Job{}
}

func TestRunnerCompletesAJobAndLogsItsEvents(t *testing.T) {
	runner, store := startRunner(t, &fakeAgent{})

	job, err := runner.Enqueue(context.Background(), "Bacon", 1)
	if err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	if job.Status != StatusQueued {
		t.Fatalf("new job status = %q", job.Status)
	}

	finished := awaitStatus(t, store, job.ID)
	if finished.Status != StatusComplete {
		t.Fatalf("status = %q, error = %q", finished.Status, finished.Error)
	}
	if finished.State == nil || finished.State.CurrentArticle != "the article" {
		t.Fatalf("state = %+v", finished.State)
	}
	if finished.StartedAt == nil || finished.FinishedAt == nil {
		t.Fatal("timestamps were not recorded")
	}

	events, err := store.Events(context.Background(), job.ID, 0, 0)
	if err != nil {
		t.Fatalf("Events returned error: %v", err)
	}

	byType := map[string]int{}
	var answer, reasoning string
	for i, e := range events {
		// Sequence numbers must be contiguous from one: a client resumes by
		// asking for everything after the last one it saw.
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has Seq %d", i, e.Seq)
		}
		byType[e.Type]++
		if e.Type == EventToken || e.Type == EventReasoning {
			var payload TokenPayload
			if err := json.Unmarshal(e.Data, &payload); err != nil {
				t.Fatalf("decode token payload: %v", err)
			}
			if payload.Stream == StreamArticle {
				if e.Type == EventToken {
					answer += payload.Text
				} else {
					reasoning += payload.Text
				}
			}
		}
	}

	if byType[EventDone] != 1 || byType[EventRound] != 1 {
		t.Fatalf("event types = %v", byType)
	}
	if answer != "the article" {
		t.Errorf("article rebuilt from the log = %q", answer)
	}
	if reasoning != "deciding the scope" {
		t.Errorf("reasoning rebuilt from the log = %q", reasoning)
	}

	// The last event carries the same state the job record holds.
	last := events[len(events)-1]
	if last.Type != EventDone {
		t.Fatalf("last event = %q, want done", last.Type)
	}
	var state orchestrator.ArticleState
	if err := json.Unmarshal(last.Data, &state); err != nil {
		t.Fatalf("decode done payload: %v", err)
	}
	if state.CurrentArticle != finished.State.CurrentArticle {
		t.Error("the done event and the job record disagree")
	}
}

func TestRunnerRecordsAFailure(t *testing.T) {
	runner, store := startRunner(t, &fakeAgent{generateErr: errors.New("provider unavailable")})

	job, err := runner.Enqueue(context.Background(), "Bacon", 1)
	if err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}

	finished := awaitStatus(t, store, job.ID)
	if finished.Status != StatusFailed {
		t.Fatalf("status = %q", finished.Status)
	}
	if !strings.Contains(finished.Error, "provider unavailable") {
		t.Fatalf("error = %q", finished.Error)
	}

	events, _ := store.Events(context.Background(), job.ID, 0, 0)
	if len(events) == 0 || events[len(events)-1].Type != EventError {
		t.Fatal("the failure was not recorded as the final event")
	}
}

// Cancelling a running job must stop it and still leave a readable record.
func TestRunnerCancelsARunningJob(t *testing.T) {
	agent := &fakeAgent{block: make(chan struct{})}
	runner, store := startRunner(t, agent)

	job, err := runner.Enqueue(context.Background(), "Bacon", 1)
	if err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := store.Job(context.Background(), job.ID)
		if current.Status == StatusRunning {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	if err := runner.Cancel(context.Background(), job.ID); err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	finished := awaitStatus(t, store, job.ID)
	if finished.Status != StatusCanceled {
		t.Fatalf("status = %q", finished.Status)
	}
	close(agent.block)
}

// A job cancelled while queued must never run.
func TestRunnerCancelsAQueuedJobWithoutRunningIt(t *testing.T) {
	store := NewMemoryStore()
	runner := NewRunner(store, &fakeAgent{}, NewBroker(), 1)

	job, err := runner.Enqueue(context.Background(), "Bacon", 1)
	if err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	if err := runner.Cancel(context.Background(), job.ID); err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}

	// Only now are workers allowed to look at the queue.
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); runner.Wait() }()
	runner.Start(ctx)

	time.Sleep(50 * time.Millisecond)
	current, _ := store.Job(context.Background(), job.ID)
	if current.Status != StatusCanceled {
		t.Fatalf("status = %q, want the queued job left cancelled", current.Status)
	}
	if current.StartedAt != nil {
		t.Fatal("a cancelled job was started anyway")
	}
}

func TestBrokerNotifiesSubscribers(t *testing.T) {
	broker := NewBroker()
	signal, release := broker.Subscribe("job-1")
	defer release()

	broker.Notify("job-1")
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("subscriber was not notified")
	}

	// A subscriber for a different job must not be woken.
	other, releaseOther := broker.Subscribe("job-2")
	defer releaseOther()
	broker.Notify("job-1")
	select {
	case <-other:
		t.Fatal("the wrong subscriber was notified")
	case <-time.After(20 * time.Millisecond):
	}
}

// A slow subscriber must never stall the pipeline.
func TestBrokerNotifyDoesNotBlockOnAFullChannel(t *testing.T) {
	broker := NewBroker()
	_, release := broker.Subscribe("job-1")
	defer release()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			broker.Notify("job-1")
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocked on an undrained subscriber")
	}
}

func TestBrokerReleaseStopsNotifications(t *testing.T) {
	broker := NewBroker()
	signal, release := broker.Subscribe("job-1")
	release()

	broker.Notify("job-1")
	select {
	case <-signal:
		t.Fatal("a released subscriber was still notified")
	case <-time.After(20 * time.Millisecond):
	}

	broker.mu.Lock()
	remaining := len(broker.subscribers)
	broker.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("broker still holds %d subscriber groups", remaining)
	}
}

// Two workers must never be handed the same job.
func TestStoreClaimHandsEachJobToOneWorker(t *testing.T) {
	store := NewMemoryStore()
	for i := 0; i < 50; i++ {
		job, err := NewJob("Topic", 1)
		if err != nil {
			t.Fatalf("NewJob returned error: %v", err)
		}
		if err := store.Create(context.Background(), job); err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
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
				if err != nil || !found {
					return
				}
				mu.Lock()
				claimed[job.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(claimed) != 50 {
		t.Fatalf("claimed %d distinct jobs, want 50", len(claimed))
	}
	for id, times := range claimed {
		if times != 1 {
			t.Fatalf("job %s was claimed %d times", id, times)
		}
	}
}

func TestStoreEventsResumeFromASequenceNumber(t *testing.T) {
	store := NewMemoryStore()
	job, _ := NewJob("Topic", 1)
	if err := store.Create(context.Background(), job); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	for i := 0; i < 5; i++ {
		if _, err := store.Append(context.Background(), job.ID, []Event{{Type: EventToken}}); err != nil {
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
}

func TestStoreReportsUnknownJobs(t *testing.T) {
	store := NewMemoryStore()
	if _, err := store.Job(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Job error = %v, want ErrNotFound", err)
	}
	if _, err := store.Events(context.Background(), "missing", 0, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Events error = %v, want ErrNotFound", err)
	}
	if _, err := store.Append(context.Background(), "missing", []Event{{Type: EventToken}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Append error = %v, want ErrNotFound", err)
	}
}

func TestValidID(t *testing.T) {
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID returned error: %v", err)
	}
	if !ValidID(id) {
		t.Fatalf("ValidID(%q) = false", id)
	}
	for _, bad := range []string{"", "../../etc/passwd", strings.Repeat("z", 32), id + "0"} {
		if ValidID(bad) {
			t.Errorf("ValidID(%q) = true", bad)
		}
	}
}
