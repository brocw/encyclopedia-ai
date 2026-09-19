package jobs

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"encyclopedia-ai/internal/orchestrator"
)

// Stream names used in token and reasoning events.
const (
	StreamArticle      = "article"
	StreamEvaluation   = "evaluation"
	StreamRevisionPlan = "revision_plan"
	StreamReferences   = "references"
	StreamInfobox      = "infobox"
	StreamSeeAlso      = "seealso"
	StreamCategories   = "category"
)

// pollInterval bounds how long a worker sleeps when the queue is empty. New
// work is signalled directly, so this only covers a missed signal or a job
// enqueued by another process against a shared store.
const pollInterval = time.Second

// Runner owns the worker pool that drains the job queue.
type Runner struct {
	Store  Store
	Agent  orchestrator.Agent
	Broker *Broker

	// Workers is the number of jobs run at once. Generation is GPU bound
	// locally, so more than one worker usually makes everything slower.
	Workers int

	// FlushInterval and FlushBytes tune token coalescing.
	FlushInterval time.Duration
	FlushBytes    int

	wake chan struct{}

	mu      sync.Mutex
	running map[string]context.CancelFunc
	started bool
	wg      sync.WaitGroup
}

// NewRunner builds a runner over a store and an agent.
func NewRunner(store Store, agent orchestrator.Agent, broker *Broker, workers int) *Runner {
	if broker == nil {
		broker = NewBroker()
	}
	if workers <= 0 {
		workers = 1
	}
	return &Runner{
		Store:   store,
		Agent:   agent,
		Broker:  broker,
		Workers: workers,
		wake:    make(chan struct{}, 1),
		running: make(map[string]context.CancelFunc),
	}
}

// Start launches the workers. They stop when ctx is cancelled.
func (r *Runner) Start(ctx context.Context) {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()

	for worker := 0; worker < r.Workers; worker++ {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.work(ctx)
		}()
	}
}

// Wait blocks until every worker has stopped.
func (r *Runner) Wait() { r.wg.Wait() }

// Enqueue records a new job and wakes a worker.
func (r *Runner) Enqueue(ctx context.Context, topic string, maxRounds int) (Job, error) {
	job, err := NewJob(topic, maxRounds)
	if err != nil {
		return Job{}, err
	}
	if err := r.Store.Create(ctx, job); err != nil {
		return Job{}, err
	}

	select {
	case r.wake <- struct{}{}:
	default:
	}
	return job, nil
}

// Cancel stops a job. A running job has its context cancelled; a queued job
// is marked cancelled so no worker picks it up.
func (r *Runner) Cancel(ctx context.Context, id string) error {
	r.mu.Lock()
	cancel, isRunning := r.running[id]
	r.mu.Unlock()

	if isRunning {
		cancel()
		return nil
	}

	job, err := r.Store.Job(ctx, id)
	if err != nil {
		return err
	}
	if job.Status.Terminal() {
		return nil
	}

	finished := time.Now().UTC()
	job.Status = StatusCanceled
	job.FinishedAt = &finished
	if err := r.Store.Save(ctx, job); err != nil {
		return err
	}
	r.Broker.Notify(id)
	return nil
}

func (r *Runner) work(ctx context.Context) {
	timer := time.NewTimer(pollInterval)
	defer timer.Stop()

	for {
		job, found, err := r.Store.Claim(ctx)
		if err != nil {
			log.Printf("[jobs] claim failed: %v", err)
		}
		if found {
			r.run(ctx, job)
			continue
		}

		// Nothing to do: wait for a signal, a poll, or shutdown.
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(pollInterval)

		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-timer.C:
		}
	}
}

// run executes one job and records everything it produces.
func (r *Runner) run(parent context.Context, job Job) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	r.mu.Lock()
	r.running[job.ID] = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, job.ID)
		r.mu.Unlock()
	}()

	r.Broker.Notify(job.ID)
	log.Printf("[jobs] running %s (%q, max_rounds=%d)", job.ID, job.Topic, job.MaxRounds)

	recorder := NewRecorder(r.FlushInterval, r.FlushBytes, func(batch []Event) {
		if _, err := r.Store.Append(context.WithoutCancel(ctx), job.ID, batch); err != nil {
			log.Printf("[jobs] append to %s failed: %v", job.ID, err)
			return
		}
		r.Broker.Notify(job.ID)
	})

	state, err := orchestrator.RunArticleLoop(ctx, job.Topic, job.MaxRounds, r.Agent, orchestrator.LoopCallbacks{
		OnArticle:      recorder.Sink(StreamArticle),
		OnEvaluation:   recorder.Sink(StreamEvaluation),
		OnRevisionPlan: recorder.Sink(StreamRevisionPlan),
		OnRoundComplete: func(round orchestrator.Round) {
			recorder.Emit(EventRound, round)
		},
		OnConverged: func() {
			recorder.Emit(EventConverged, struct{}{})
		},
		Metadata: orchestrator.MetadataCallbacks{
			OnReferences: recorder.Sink(StreamReferences),
			OnInfobox:    recorder.Sink(StreamInfobox),
			OnSeeAlso:    recorder.Sink(StreamSeeAlso),
			OnCategories: recorder.Sink(StreamCategories),
		},
	})

	// The job outcome must be recorded even when the request context died,
	// so a cancelled or failed run still has a readable final state.
	finalize := context.WithoutCancel(ctx)
	job.State = state
	finished := time.Now().UTC()
	job.FinishedAt = &finished

	switch {
	case err != nil && errors.Is(ctx.Err(), context.Canceled):
		job.Status = StatusCanceled
		job.Error = "generation canceled"
		recorder.Emit(EventError, errorPayload{Message: job.Error, State: state})
	case err != nil:
		job.Status = StatusFailed
		job.Error = err.Error()
		recorder.Emit(EventError, errorPayload{Message: job.Error, State: state})
	default:
		job.Status = StatusComplete
		recorder.Emit(EventDone, state)
	}
	recorder.Close()

	if saveErr := r.Store.Save(finalize, job); saveErr != nil {
		log.Printf("[jobs] saving %s failed: %v", job.ID, saveErr)
	}
	r.Broker.Notify(job.ID)
	log.Printf("[jobs] %s finished: %s", job.ID, job.Status)
}

type errorPayload struct {
	Message string                     `json:"message"`
	State   *orchestrator.ArticleState `json:"state,omitempty"`
}
