package handlers

import (
	"context"
	"encoding/json"
	"encyclopedia-ai/internal/ai"
	"encyclopedia-ai/internal/orchestrator"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
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
	if agent == nil {
		agent = ai.DefaultClient()
	}
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
			OnArticleToken:      func(token string) { ss.send("article_token", token) },
			OnEvaluationToken:   func(token string) { ss.send("evaluation_token", token) },
			OnRevisionPlanToken: func(token string) { ss.send("revision_plan_token", token) },
			OnRoundComplete: func(round orchestrator.Round) {
				ss.sendJSON("round_complete", round)
			},
			OnConverged: func() {
				ss.send("converged", "")
			},
			Metadata: orchestrator.MetadataCallbacks{
				OnReferencesToken: func(token string) { ss.send("references_token", token) },
				OnInfoboxToken:    func(token string) { ss.send("infobox_token", token) },
				OnSeeAlsoToken:    func(token string) { ss.send("seealso_token", token) },
				OnCategoryToken:   func(token string) { ss.send("category_token", token) },
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
