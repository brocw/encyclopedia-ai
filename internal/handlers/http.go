package handlers

import (
	"encoding/json"
	"encyclopedia-ai/internal/orchestrator"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
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

	initialState, err := orchestrator.StartNewArticle(request.Topic)
	if err != nil {
		http.Error(w, "Failed to generate article: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, initialState)
}

func ContinueArticle(w http.ResponseWriter, r *http.Request) {
	var currentState orchestrator.ArticleState

	if err := json.NewDecoder(r.Body).Decode(&currentState); err != nil {
		http.Error(w, "Invalid article state provided", http.StatusBadRequest)
		return
	}

	nextState, err := orchestrator.PerformRevisionCycle(&currentState)
	if err != nil {
		http.Error(w, "Failed to revise article: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, nextState)
}
