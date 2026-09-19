package jobs

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"encyclopedia-ai/internal/llm"
)

type collector struct {
	mu     sync.Mutex
	events []Event
}

func (c *collector) flush(batch []Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, batch...)
}

func (c *collector) snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.events))
	copy(out, c.events)
	return out
}

func text(t *testing.T, e Event) TokenPayload {
	t.Helper()
	var payload TokenPayload
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		t.Fatalf("decode %s payload: %v", e.Type, err)
	}
	return payload
}

// Coalescing is the point of the recorder: one reasoning run produced over
// eleven thousand deltas, and they must not become eleven thousand events.
func TestRecorderCoalescesDeltasIntoChunks(t *testing.T) {
	var sink collector
	// A long interval keeps this deterministic: only the size threshold fires.
	recorder := NewRecorder(time.Hour, 16, sink.flush)

	stream := recorder.Sink(StreamArticle)
	for i := 0; i < 64; i++ {
		stream(llm.Delta{Content: "x"})
	}
	recorder.Close()

	events := sink.snapshot()
	if len(events) != 4 {
		t.Fatalf("events = %d, want 64 deltas coalesced into 4 chunks of 16", len(events))
	}

	var combined string
	for _, e := range events {
		if e.Type != EventToken {
			t.Fatalf("event type = %q", e.Type)
		}
		payload := text(t, e)
		if payload.Stream != StreamArticle {
			t.Fatalf("stream = %q", payload.Stream)
		}
		combined += payload.Text
	}
	if len(combined) != 64 {
		t.Fatalf("recovered %d characters, want all 64", len(combined))
	}
}

// Answer and reasoning must stay in separate events all the way to the log.
func TestRecorderKeepsReasoningInItsOwnEvents(t *testing.T) {
	var sink collector
	recorder := NewRecorder(time.Hour, 1<<20, sink.flush)

	stream := recorder.Sink(StreamEvaluation)
	stream(llm.Delta{Reasoning: "weighing "})
	stream(llm.Delta{Reasoning: "the draft"})
	stream(llm.Delta{Content: `{"ok":`})
	stream(llm.Delta{Content: `true}`})
	recorder.Close()

	byType := map[string]string{}
	for _, e := range sink.snapshot() {
		byType[e.Type] += text(t, e).Text
	}
	if byType[EventReasoning] != "weighing the draft" {
		t.Errorf("reasoning = %q", byType[EventReasoning])
	}
	if byType[EventToken] != `{"ok":true}` {
		t.Errorf("answer = %q", byType[EventToken])
	}
}

// A milestone must not overtake the tokens that preceded it.
func TestRecorderFlushesPendingTokensBeforeAMilestone(t *testing.T) {
	var sink collector
	recorder := NewRecorder(time.Hour, 1<<20, sink.flush)

	recorder.Sink(StreamArticle)(llm.Delta{Content: "draft text"})
	recorder.Emit(EventConverged, struct{}{})
	recorder.Close()

	events := sink.snapshot()
	if len(events) != 2 {
		t.Fatalf("events = %d, want the token chunk then the milestone", len(events))
	}
	if events[0].Type != EventToken || events[1].Type != EventConverged {
		t.Fatalf("order = %q, %q", events[0].Type, events[1].Type)
	}
}

// A repair retry invalidates what was buffered, so it must never be recorded.
func TestRecorderDiscardsBufferedTokensOnRestart(t *testing.T) {
	var sink collector
	recorder := NewRecorder(time.Hour, 1<<20, sink.flush)

	stream := recorder.Sink(StreamEvaluation)
	stream(llm.Delta{Content: "stale partial answer"})
	stream(llm.Delta{Restart: true})
	stream(llm.Delta{Content: "corrected"})
	recorder.Close()

	var combined string
	var sawRestart bool
	for _, e := range sink.snapshot() {
		switch e.Type {
		case EventRestart:
			sawRestart = true
		case EventToken:
			combined += text(t, e).Text
		}
	}
	if !sawRestart {
		t.Fatal("restart was not recorded")
	}
	if combined != "corrected" {
		t.Fatalf("recorded %q, want the stale partial answer dropped", combined)
	}
}

func TestRecorderFlushesOnItsInterval(t *testing.T) {
	var sink collector
	recorder := NewRecorder(10*time.Millisecond, 1<<20, sink.flush)
	defer recorder.Close()

	recorder.Sink(StreamArticle)(llm.Delta{Content: "a short chunk"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sink.snapshot()) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("a partially filled buffer was never flushed by the timer")
}

// The four metadata agents stream concurrently.
func TestRecorderIsSafeForConcurrentStreams(t *testing.T) {
	var sink collector
	recorder := NewRecorder(time.Millisecond, 8, sink.flush)

	streams := []string{StreamReferences, StreamInfobox, StreamSeeAlso, StreamCategories}
	var wg sync.WaitGroup
	for _, name := range streams {
		wg.Add(1)
		go func(stream llm.Sink) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				stream(llm.Delta{Content: "y"})
			}
		}(recorder.Sink(name))
	}
	wg.Wait()
	recorder.Close()

	totals := map[string]int{}
	for _, e := range sink.snapshot() {
		payload := text(t, e)
		totals[payload.Stream] += len(payload.Text)
	}
	for _, name := range streams {
		if totals[name] != 200 {
			t.Errorf("stream %s recorded %d characters, want 200", name, totals[name])
		}
	}
}

// Writes from a straggling goroutine must not land after the job is final.
func TestRecorderDiscardsWritesAfterClose(t *testing.T) {
	var sink collector
	recorder := NewRecorder(time.Hour, 1<<20, sink.flush)
	recorder.Close()

	recorder.Sink(StreamArticle)(llm.Delta{Content: "late"})
	recorder.Emit(EventDone, struct{}{})

	if events := sink.snapshot(); len(events) != 0 {
		t.Fatalf("events after close = %d, want none", len(events))
	}
}
