package handlers

import (
	"encoding/json"
	"encyclopedia-ai/internal/orchestrator"
	"fmt"
	"log"
	"net/http"
	"sync"
)

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// safeSender provides thread-safe SSE writes for concurrent goroutines.
type safeSender struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	flusher http.Flusher
}

func (s *safeSender) send(event, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	encoded, _ := json.Marshal(data)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, encoded)
	s.flusher.Flush()
}

func (s *safeSender) sendJSON(event string, data interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	encoded, _ := json.Marshal(data)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, string(encoded))
	s.flusher.Flush()
}

func StartArticle(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Topic     string `json:"topic"`
		MaxRounds int    `json:"max_rounds"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if request.Topic == "" {
		http.Error(w, "Topic cannot be empty", http.StatusBadRequest)
		return
	}

	if request.MaxRounds <= 0 {
		request.MaxRounds = 3
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ss := &safeSender{w: w, flusher: flusher}

	state, err := orchestrator.RunArticleLoop(
		request.Topic,
		request.MaxRounds,
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
		ss.send("error", err.Error())
		return
	}

	ss.send("article_done", "")

	finalJSON, _ := json.Marshal(state)
	ss.send("done", string(finalJSON))
}
