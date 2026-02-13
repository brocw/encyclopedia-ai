package orchestrator

import (
	"encyclopedia-ai/internal/ai"
	"log"
	"sync"
)

type ArticleState struct {
	Topic           string   `json:"topic"`
	CurrentArticle  string   `json:"current_article"`
	FactCheck       string   `json:"fact_check"`
	References      string   `json:"references"`
	Infobox         string   `json:"infobox"`
	SeeAlso         string   `json:"see_also"`
	Categories      string   `json:"categories"`
	RevisionHistory []string `json:"revision_history"`
}

type PostArticleCallbacks struct {
	OnFactCheckToken  func(string)
	OnReferencesToken func(string)
	OnInfoboxToken    func(string)
	OnSeeAlsoToken    func(string)
	OnCategoryToken   func(string)
}

type agentResult struct {
	name  string
	value string
	err   error
}

// runPostArticleAgents launches all 5 agents in parallel and collects results.
func runPostArticleAgents(topic, article string, cb PostArticleCallbacks) (factCheck, references, infobox, seeAlso, categories string, errs []error) {
	results := make(chan agentResult, 5)
	var wg sync.WaitGroup

	wg.Add(5)

	go func() {
		defer wg.Done()
		val, err := ai.FactCheckStreaming(article, cb.OnFactCheckToken)
		results <- agentResult{"factcheck", val, err}
	}()

	go func() {
		defer wg.Done()
		val, err := ai.ReferencesStreaming(article, cb.OnReferencesToken)
		results <- agentResult{"references", val, err}
	}()

	go func() {
		defer wg.Done()
		val, err := ai.InfoboxStreaming(topic, article, cb.OnInfoboxToken)
		results <- agentResult{"infobox", val, err}
	}()

	go func() {
		defer wg.Done()
		val, err := ai.SeeAlsoStreaming(article, cb.OnSeeAlsoToken)
		results <- agentResult{"seealso", val, err}
	}()

	go func() {
		defer wg.Done()
		val, err := ai.CategorizeArticleStreaming(article, cb.OnCategoryToken)
		results <- agentResult{"categories", val, err}
	}()

	wg.Wait()
	close(results)

	for r := range results {
		if r.err != nil {
			log.Printf("Error from %s agent: %v", r.name, r.err)
			errs = append(errs, r.err)
		}
		switch r.name {
		case "factcheck":
			factCheck = r.value
		case "references":
			references = r.value
		case "infobox":
			infobox = r.value
		case "seealso":
			seeAlso = r.value
		case "categories":
			categories = r.value
		}
	}

	return
}

// StartNewArticleStreaming generates an article, then runs all 5 post-article agents in parallel.
func StartNewArticleStreaming(topic string, onArticleToken func(string), cb PostArticleCallbacks) (*ArticleState, error) {
	initialContent, err := ai.GenerateArticleStreaming(topic, onArticleToken)
	if err != nil {
		return nil, err
	}
	log.Printf("Finished generating article '%s'\n", topic)

	factCheck, references, infobox, seeAlso, categories, errs := runPostArticleAgents(topic, initialContent, cb)
	if len(errs) > 0 {
		log.Printf("Warning: %d post-article agent(s) had errors for '%s'", len(errs), topic)
	}
	log.Printf("Finished all post-article agents for '%s'\n", topic)

	state := &ArticleState{
		Topic:           topic,
		CurrentArticle:  initialContent,
		FactCheck:       factCheck,
		References:      references,
		Infobox:         infobox,
		SeeAlso:         seeAlso,
		Categories:      categories,
		RevisionHistory: []string{},
	}

	return state, nil
}

// PerformRevisionCycleStreaming revises article using fact-check, then re-runs all agents.
func PerformRevisionCycleStreaming(currentState *ArticleState, onArticleToken func(string), cb PostArticleCallbacks) (*ArticleState, error) {
	revisedContent, err := ai.ReviseArticleStreaming(currentState.Topic, currentState.CurrentArticle, currentState.FactCheck, onArticleToken)
	if err != nil {
		return nil, err
	}
	log.Printf("Finished revising article '%s'\n", currentState.Topic)

	factCheck, references, infobox, seeAlso, categories, errs := runPostArticleAgents(currentState.Topic, revisedContent, cb)
	if len(errs) > 0 {
		log.Printf("Warning: %d post-article agent(s) had errors for '%s'", len(errs), currentState.Topic)
	}
	log.Printf("Finished all post-article agents for revised '%s'\n", currentState.Topic)

	newState := &ArticleState{
		Topic:           currentState.Topic,
		CurrentArticle:  revisedContent,
		FactCheck:       factCheck,
		References:      references,
		Infobox:         infobox,
		SeeAlso:         seeAlso,
		Categories:      categories,
		RevisionHistory: append(currentState.RevisionHistory, currentState.FactCheck),
	}

	return newState, nil
}
