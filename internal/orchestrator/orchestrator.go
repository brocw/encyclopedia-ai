package orchestrator

import (
	"encyclopedia-ai/internal/ai"
	"log"
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

	log.Printf("Finished generating article '%s'\n", topic)

	// Get first critique of draft
	critique, err := ai.CritiqueArticle(initialContent)
	if err != nil {
		return nil, err
	}

	log.Printf("Finished generating first critique of article '%s'\n", topic)

	// Create initial state
	state := &ArticleState{
		Topic:           topic,
		CurrentArticle:  initialContent,
		LastCritique:    critique,
		RevisionHistory: []string{}, // No history yet
	}

	return state, nil
}

// StartNewArticleStreaming generates an article and critique with token callbacks.
func StartNewArticleStreaming(topic string, onArticleToken, onCritiqueToken func(string)) (*ArticleState, error) {
	initialContent, err := ai.GenerateArticleStreaming(topic, onArticleToken)
	if err != nil {
		return nil, err
	}

	log.Printf("Finished generating article '%s'\n", topic)

	critique, err := ai.CritiqueArticleStreaming(initialContent, onCritiqueToken)
	if err != nil {
		return nil, err
	}

	log.Printf("Finished generating first critique of article '%s'\n", topic)

	state := &ArticleState{
		Topic:           topic,
		CurrentArticle:  initialContent,
		LastCritique:    critique,
		RevisionHistory: []string{},
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

	log.Printf("Finished generating article '%s'\n", currentState.Topic)

	// New critique of the revised article
	newCritique, err := ai.CritiqueArticle(revisedContent)
	if err != nil {
		return nil, err
	}

	log.Printf("Finished generating critique of article '%s'\n", currentState.Topic)

	newState := &ArticleState{
		Topic:           currentState.Topic,
		CurrentArticle:  revisedContent,
		LastCritique:    newCritique,
		RevisionHistory: append(currentState.RevisionHistory, currentState.LastCritique),
	}

	return newState, nil
}

// PerformRevisionCycleStreaming revises and critiques with token callbacks.
func PerformRevisionCycleStreaming(currentState *ArticleState, onArticleToken, onCritiqueToken func(string)) (*ArticleState, error) {
	revisedContent, err := ai.ReviseArticleStreaming(currentState.Topic, currentState.CurrentArticle, currentState.LastCritique, onArticleToken)
	if err != nil {
		return nil, err
	}

	log.Printf("Finished generating article '%s'\n", currentState.Topic)

	newCritique, err := ai.CritiqueArticleStreaming(revisedContent, onCritiqueToken)
	if err != nil {
		return nil, err
	}

	log.Printf("Finished generating critique of article '%s'\n", currentState.Topic)

	newState := &ArticleState{
		Topic:           currentState.Topic,
		CurrentArticle:  revisedContent,
		LastCritique:    newCritique,
		RevisionHistory: append(currentState.RevisionHistory, currentState.LastCritique),
	}

	return newState, nil
}
