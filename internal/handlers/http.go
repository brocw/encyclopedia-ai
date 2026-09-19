package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"encyclopedia-ai/internal/llm"
	"encyclopedia-ai/internal/orchestrator"
)

const (
	maxRequestBodyBytes = 16 * 1024
	defaultMaxRounds    = 3
	maxAllowedRounds    = 10
)

// Handler owns the AI dependency so HTTP tests can use a deterministic fake.
type Handler struct {
	Agent orchestrator.Agent
}

func New(agent orchestrator.Agent) *Handler {
	return &Handler{Agent: agent}
}

// safeSender serializes concurrent metadata token events into one SSE stream.
// If the browser disconnects, onWriteError cancels the request context and the
// in-flight Ollama requests are stopped as well.
type safeSender struct {
	mu           sync.Mutex
	w            http.ResponseWriter
	flusher      http.Flusher
	onWriteError func(error)
	err          error
}

func (s *safeSender) write(event string, encoded []byte) {
	s.mu.Lock()
	if s.err != nil {
		s.mu.Unlock()
		return
	}
	_, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, encoded)
	if err == nil {
		s.flusher.Flush()
	} else {
		s.err = err
	}
	s.mu.Unlock()

	if err != nil && s.onWriteError != nil {
		s.onWriteError(err)
	}
}

func (s *safeSender) send(event, data string) {
	encoded, err := json.Marshal(data)
	if err != nil {
		if s.onWriteError != nil {
			s.onWriteError(err)
		}
		return
	}
	s.write(event, encoded)
}

func (s *safeSender) sendJSON(event string, data interface{}) {
	encoded, err := json.Marshal(data)
	if err != nil {
		if s.onWriteError != nil {
			s.onWriteError(err)
		}
		return
	}
	s.write(event, encoded)
}

func (s *safeSender) error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// sseSink turns one phase of the pipeline into SSE events.
//
// Answer tokens and reasoning tokens travel on separate events, so the client
// can render a model's deliberation beside the article without either stream
// contaminating the other. A restart tells the client to discard what it has
// shown for this phase, because a repair retry is answering again.
func sseSink(ss *safeSender, stream string) llm.Sink {
	var (
		tokenEvent     = stream + "_token"
		reasoningEvent = stream + "_reasoning"
		restartEvent   = stream + "_restart"
	)
	return func(delta llm.Delta) {
		if delta.Restart {
			ss.send(restartEvent, "")
		}
		if delta.Reasoning != "" {
			ss.send(reasoningEvent, delta.Reasoning)
		}
		if delta.Content != "" {
			ss.send(tokenEvent, delta.Content)
		}
	}
}

type errorEvent struct {
	Message string                     `json:"message"`
	State   *orchestrator.ArticleState `json:"state,omitempty"`
}

func (h *Handler) StartArticle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	var request struct {
		Topic     string `json:"topic"`
		MaxRounds int    `json:"max_rounds"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	request.Topic = strings.TrimSpace(request.Topic)
	if request.Topic == "" {
		http.Error(w, "Topic cannot be empty", http.StatusBadRequest)
		return
	}
	if request.MaxRounds <= 0 {
		request.MaxRounds = defaultMaxRounds
	}
	if request.MaxRounds > maxAllowedRounds {
		http.Error(w, fmt.Sprintf("max_rounds cannot exceed %d", maxAllowedRounds), http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	ss := &safeSender{
		w:            w,
		flusher:      flusher,
		onWriteError: func(error) { cancel() },
	}

	state, err := orchestrator.RunArticleLoop(
		ctx,
		request.Topic,
		request.MaxRounds,
		h.Agent,
		orchestrator.LoopCallbacks{
			OnArticle:      sseSink(ss, "article"),
			OnEvaluation:   sseSink(ss, "evaluation"),
			OnRevisionPlan: sseSink(ss, "revision_plan"),
			OnRoundComplete: func(round orchestrator.Round) {
				ss.sendJSON("round_complete", round)
			},
			OnConverged: func() {
				ss.send("converged", "")
			},
			Metadata: orchestrator.MetadataCallbacks{
				OnReferences: sseSink(ss, "references"),
				OnInfobox:    sseSink(ss, "infobox"),
				OnSeeAlso:    sseSink(ss, "seealso"),
				OnCategories: sseSink(ss, "category"),
			},
		},
	)
	if err != nil {
		log.Printf("Error in StartArticle: %v", err)
		ss.sendJSON("error", errorEvent{Message: err.Error(), State: state})
		return
	}
	if ss.error() != nil {
		return
	}

	ss.send("article_done", "")
	ss.sendJSON("done", state)
}
