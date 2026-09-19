package jobs

import (
	"sync"
	"time"

	"encyclopedia-ai/internal/llm"
)

// Event types written to a job's log.
const (
	EventToken     = "token"     // a chunk of a stream's answer
	EventReasoning = "reasoning" // a chunk of a stream's reasoning trace
	EventRestart   = "restart"   // discard this stream; a repair retry is answering again
	EventPhase     = "phase"     // a pipeline phase started
	EventRound     = "round"     // a round completed
	EventConverged = "converged"
	EventDone      = "done"
	EventError     = "error"
)

// Coalescing defaults. A single reasoning run produced over eleven thousand
// token deltas, so they are batched before they reach the log: the client
// still sees smooth streaming, and the store sees a few hundred rows.
const (
	DefaultFlushInterval = 200 * time.Millisecond
	DefaultFlushBytes    = 512
)

// TokenPayload is the body of a token or reasoning event.
type TokenPayload struct {
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// StreamPayload names a stream with no other content.
type StreamPayload struct {
	Stream string `json:"stream"`
}

// Recorder batches streamed deltas into events and hands them to a flush
// function in causal order.
//
// It is safe for concurrent use: the metadata agents stream from four
// goroutines at once.
type Recorder struct {
	mu       sync.Mutex
	flush    func([]Event)
	interval time.Duration
	maxBytes int

	// pending holds one buffer per (stream, event type), and order records
	// the sequence in which those buffers were first written, so a flush
	// preserves the order the tokens actually arrived in.
	pending map[bufferKey]*[]byte
	order   []bufferKey
	bytes   int
	timer   *time.Timer
	closed  bool
}

type bufferKey struct {
	stream    string
	eventType string
}

// NewRecorder builds a recorder. Zero interval or size uses the defaults.
func NewRecorder(interval time.Duration, maxBytes int, flush func([]Event)) *Recorder {
	if interval <= 0 {
		interval = DefaultFlushInterval
	}
	if maxBytes <= 0 {
		maxBytes = DefaultFlushBytes
	}
	return &Recorder{
		flush:    flush,
		interval: interval,
		maxBytes: maxBytes,
		pending:  make(map[bufferKey]*[]byte),
	}
}

// Sink returns an llm.Sink that records one named stream.
func (r *Recorder) Sink(stream string) llm.Sink {
	return func(delta llm.Delta) {
		if delta.Restart {
			r.restart(stream)
		}
		if delta.Reasoning != "" {
			r.append(stream, EventReasoning, delta.Reasoning)
		}
		if delta.Content != "" {
			r.append(stream, EventToken, delta.Content)
		}
	}
}

func (r *Recorder) append(stream, eventType, text string) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}

	key := bufferKey{stream, eventType}
	buffer, found := r.pending[key]
	if !found {
		fresh := make([]byte, 0, r.maxBytes)
		buffer = &fresh
		r.pending[key] = buffer
		r.order = append(r.order, key)
	}
	*buffer = append(*buffer, text...)
	r.bytes += len(text)

	if r.bytes >= r.maxBytes {
		batch := r.drainLocked()
		r.mu.Unlock()
		r.deliver(batch)
		return
	}

	r.armLocked()
	r.mu.Unlock()
}

// restart drops whatever this stream has buffered and records the signal,
// because a repair retry invalidates everything shown so far.
func (r *Recorder) restart(stream string) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	for _, eventType := range []string{EventToken, EventReasoning} {
		key := bufferKey{stream, eventType}
		if buffer, found := r.pending[key]; found {
			r.bytes -= len(*buffer)
			delete(r.pending, key)
			r.order = removeKey(r.order, key)
		}
	}
	batch := r.drainLocked()
	r.mu.Unlock()

	if restart, err := event(EventRestart, StreamPayload{Stream: stream}); err == nil {
		batch = append(batch, restart)
	}
	r.deliver(batch)
}

// Emit records a milestone. Pending token batches are flushed first, so the
// log reads in the order things actually happened.
func (r *Recorder) Emit(eventType string, payload any) {
	entry, err := event(eventType, payload)
	if err != nil {
		return
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	batch := r.drainLocked()
	r.mu.Unlock()

	r.deliver(append(batch, entry))
}

// Flush delivers everything buffered.
func (r *Recorder) Flush() {
	r.mu.Lock()
	batch := r.drainLocked()
	r.mu.Unlock()
	r.deliver(batch)
}

// Close flushes and stops the recorder. Later writes are discarded, so a
// straggling goroutine cannot append after the job is finalised.
func (r *Recorder) Close() {
	r.mu.Lock()
	batch := r.drainLocked()
	r.closed = true
	r.mu.Unlock()
	r.deliver(batch)
}

// drainLocked converts the buffers into events. The caller must hold the lock.
func (r *Recorder) drainLocked() []Event {
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	if len(r.order) == 0 {
		return nil
	}

	batch := make([]Event, 0, len(r.order))
	for _, key := range r.order {
		buffer, found := r.pending[key]
		if !found || len(*buffer) == 0 {
			continue
		}
		entry, err := event(key.eventType, TokenPayload{Stream: key.stream, Text: string(*buffer)})
		if err == nil {
			batch = append(batch, entry)
		}
		delete(r.pending, key)
	}
	r.order = r.order[:0]
	r.bytes = 0
	return batch
}

// armLocked schedules a flush for partially filled buffers.
func (r *Recorder) armLocked() {
	if r.timer != nil {
		return
	}
	r.timer = time.AfterFunc(r.interval, r.Flush)
}

func (r *Recorder) deliver(batch []Event) {
	if len(batch) > 0 && r.flush != nil {
		r.flush(batch)
	}
}

func removeKey(keys []bufferKey, target bufferKey) []bufferKey {
	kept := keys[:0]
	for _, key := range keys {
		if key != target {
			kept = append(kept, key)
		}
	}
	return kept
}
