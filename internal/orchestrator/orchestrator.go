package orchestrator

import (
	"encyclopedia-ai/internal/ai"
)

type ArticleState struct {
	Topic           string   `json:"topic"`
	CurrentArticle  string   `json:"current_article"`
	LastCritique    string   `json:"last_critique"`
	RevisionHistory []string `json:"revision_history"`
}

func StartNewArticle(topic string) (*ArticleState, error) {
	// Generate initial article draft
	initialContent, err := ai.GenerateArticle(topic)
	if err != nil {
		return nil, err
	}

	// Get first critique of draft
	critique, err := ai.CritiqueArticle(initialContent)
	if err != nil {
		return nil, err
	}

	// Create initial state
	state := &ArticleState{
		Topic:           topic,
		CurrentArticle:  initialContent,
		LastCritique:    critique,
		RevisionHistory: []string{}, // No history yet
	}

	return state, nil
}

// Takes an existing state and runs a loop
func PerformRevisionCycle(currentState *ArticleState) (*ArticleState, error) {
	// Revise article based on last critique
	revisedContent, err := ai.ReviseArticle(currentState.Topic, currentState.CurrentArticle, currentState.LastCritique)
	if err != nil {
		return nil, err
	}

	// New critique of the revised article
	newCritique, err := ai.CritiqueArticle(revisedContent)
	if err != nil {
		return nil, err
	}

	newState := &ArticleState{
		Topic:           currentState.Topic,
		CurrentArticle:  revisedContent,
		LastCritique:    newCritique,
		RevisionHistory: append(currentState.RevisionHistory, currentState.LastCritique),
	}

	return newState, nil
}
