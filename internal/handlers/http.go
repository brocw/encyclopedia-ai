package handlers

import (
	"encoding/json"
	"fmt"
	"encyclopedia-ai/internal/orchestrator"
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

func StartArticle(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Topic string `json:"topic"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if request.Topic == "" {
		http.Error(w, "Topic cannot be empty", http.StatusBadRequest)
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

	ss := &safeSender{w: w, flusher: flusher}

	state, err := orchestrator.StartNewArticleStreaming(
		request.Topic,
		func(token string) { ss.send("article_token", token) },
		orchestrator.PostArticleCallbacks{
			OnFactCheckToken:  func(token string) { ss.send("factcheck_token", token) },
			OnReferencesToken: func(token string) { ss.send("references_token", token) },
			OnInfoboxToken:    func(token string) { ss.send("infobox_token", token) },
			OnSeeAlsoToken:    func(token string) { ss.send("seealso_token", token) },
			OnCategoryToken:   func(token string) { ss.send("category_token", token) },
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

func ContinueArticle(w http.ResponseWriter, r *http.Request) {
	var currentState orchestrator.ArticleState

	if err := json.NewDecoder(r.Body).Decode(&currentState); err != nil {
		http.Error(w, "Invalid article state provided", http.StatusBadRequest)
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

	ss := &safeSender{w: w, flusher: flusher}

	nextState, err := orchestrator.PerformRevisionCycleStreaming(
		&currentState,
		func(token string) { ss.send("article_token", token) },
		orchestrator.PostArticleCallbacks{
			OnFactCheckToken:  func(token string) { ss.send("factcheck_token", token) },
			OnReferencesToken: func(token string) { ss.send("references_token", token) },
			OnInfoboxToken:    func(token string) { ss.send("infobox_token", token) },
			OnSeeAlsoToken:    func(token string) { ss.send("seealso_token", token) },
			OnCategoryToken:   func(token string) { ss.send("category_token", token) },
		},
	)
	if err != nil {
		log.Printf("Error in ContinueArticle: %v", err)
		ss.send("error", err.Error())
		return
	}

	ss.send("article_done", "")

	finalJSON, _ := json.Marshal(nextState)
	ss.send("done", string(finalJSON))
}
