package handlers

import (
	"encoding/json"
	"fmt"
	"encyclopedia-ai/internal/orchestrator"
	"log"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// sendSSE writes a single SSE event to the response and flushes.
// Data is JSON-encoded to safely handle newlines and special characters.
func sendSSE(w http.ResponseWriter, flusher http.Flusher, event, data string) {
	encoded, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
	flusher.Flush()
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

	state, err := orchestrator.StartNewArticleStreaming(
		request.Topic,
		func(token string) {
			sendSSE(w, flusher, "article_token", token)
		},
		func(token string) {
			sendSSE(w, flusher, "critique_token", token)
		},
	)
	if err != nil {
		log.Printf("Error in StartArticle: %v", err)
		sendSSE(w, flusher, "error", err.Error())
		return
	}

	sendSSE(w, flusher, "article_done", "")

	finalJSON, _ := json.Marshal(state)
	sendSSE(w, flusher, "done", string(finalJSON))
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

	nextState, err := orchestrator.PerformRevisionCycleStreaming(
		&currentState,
		func(token string) {
			sendSSE(w, flusher, "article_token", token)
		},
		func(token string) {
			sendSSE(w, flusher, "critique_token", token)
		},
	)
	if err != nil {
		log.Printf("Error in ContinueArticle: %v", err)
		sendSSE(w, flusher, "error", err.Error())
		return
	}

	sendSSE(w, flusher, "article_done", "")

	finalJSON, _ := json.Marshal(nextState)
	sendSSE(w, flusher, "done", string(finalJSON))
}
