package orchestrator

import (
	"encoding/json"
	"encyclopedia-ai/internal/ai"
	"log"
	"math"
	"sync"
)

type Scores struct {
	FactualAccuracy int `json:"factual_accuracy"`
	Completeness    int `json:"completeness"`
	Neutrality      int `json:"neutrality"`
	Clarity         int `json:"clarity"`
	Structure       int `json:"structure"`
}

type Evaluation struct {
	Scores         Scores   `json:"scores"`
	Overall        float64  `json:"overall"`
	CriticalIssues []string `json:"critical_issues"`
}

type Round struct {
	Number       int        `json:"number"`
	Article      string     `json:"article"`
	Evaluation   Evaluation `json:"evaluation"`
	RevisionPlan string     `json:"revision_plan,omitempty"`
}

type ArticleState struct {
	Topic          string  `json:"topic"`
	CurrentArticle string  `json:"current_article"`
	References     string  `json:"references"`
	Infobox        string  `json:"infobox"`
	SeeAlso        string  `json:"see_also"`
	Categories     string  `json:"categories"`
	Rounds         []Round `json:"rounds"`
	Converged      bool    `json:"converged"`
}

// MetadataCallbacks holds token callbacks for the metadata agents that run after the loop.
type MetadataCallbacks struct {
	OnReferencesToken func(string)
	OnInfoboxToken    func(string)
	OnSeeAlsoToken    func(string)
	OnCategoryToken   func(string)
}

// LoopCallbacks holds token callbacks for each phase of the cybernetic loop.
type LoopCallbacks struct {
	OnArticleToken      func(string)
	OnEvaluationToken   func(string)
	OnRevisionPlanToken func(string)
	OnRoundComplete     func(Round)
	OnConverged         func()
	Metadata            MetadataCallbacks
}

type agentResult struct {
	name  string
	value string
	err   error
}

const (
	qualityThreshold  = 8.0
	stagnationEpsilon = 0.3
)

// hasConverged returns true if the evaluation meets the quality threshold
// and there are no critical issues.
func hasConverged(eval Evaluation) bool {
	return eval.Overall >= qualityThreshold && len(eval.CriticalIssues) == 0
}

// isStagnant returns true if the current overall score has not improved
// meaningfully compared to the previous round.
func isStagnant(current, previous Evaluation) bool {
	return math.Abs(current.Overall-previous.Overall) < stagnationEpsilon
}

// parseEvaluation unmarshals the JSON evaluation string into an Evaluation struct.
func parseEvaluation(raw string) (Evaluation, error) {
	var eval Evaluation
	if err := json.Unmarshal([]byte(raw), &eval); err != nil {
		return eval, err
	}
	return eval, nil
}

// runMetadataAgents launches the 4 metadata agents in parallel after the loop completes.
func runMetadataAgents(topic, article string, cb MetadataCallbacks) (references, infobox, seeAlso, categories string, errs []error) {
	results := make(chan agentResult, 4)
	var wg sync.WaitGroup

	wg.Add(4)

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

// RunArticleLoop executes the full cybernetic feedback loop:
//
//	Generate → [Evaluate → Compare → Plan → Revise]* → Metadata agents
//
// The loop runs for at most maxRounds revision cycles, stopping early if
// the article converges (meets quality threshold) or scores stagnate.
func RunArticleLoop(topic string, maxRounds int, cb LoopCallbacks) (*ArticleState, error) {
	// --- Actuator: generate initial article ---
	article, err := ai.GenerateArticleStreaming(topic, cb.OnArticleToken)
	if err != nil {
		return nil, err
	}
	log.Printf("Finished generating article '%s'", topic)

	rounds := []Round{}
	converged := false

	for i := 1; i <= maxRounds; i++ {
		log.Printf("Starting evaluation round %d for '%s'", i, topic)

		// --- Sensor: evaluate current article ---
		evalRaw, err := ai.EvaluateArticleStreaming(article, cb.OnEvaluationToken)
		if err != nil {
			log.Printf("Error evaluating article round %d: %v", i, err)
			break
		}

		eval, err := parseEvaluation(evalRaw)
		if err != nil {
			log.Printf("Error parsing evaluation round %d: %v", i, err)
			break
		}

		round := Round{
			Number:     i,
			Article:    article,
			Evaluation: eval,
		}

		// --- Comparator: check convergence ---
		if hasConverged(eval) {
			log.Printf("Article '%s' converged at round %d (overall: %.1f)", topic, i, eval.Overall)
			rounds = append(rounds, round)
			if cb.OnRoundComplete != nil {
				cb.OnRoundComplete(round)
			}
			converged = true
			break
		}

		if i > 1 && isStagnant(eval, rounds[len(rounds)-1].Evaluation) {
			log.Printf("Article '%s' stagnated at round %d (overall: %.1f)", topic, i, eval.Overall)
			rounds = append(rounds, round)
			if cb.OnRoundComplete != nil {
				cb.OnRoundComplete(round)
			}
			break
		}

		// --- Controller: plan revision ---
		planRaw, err := ai.PlanRevisionStreaming(article, evalRaw, cb.OnRevisionPlanToken)
		if err != nil {
			log.Printf("Error planning revision round %d: %v", i, err)
			rounds = append(rounds, round)
			break
		}
		round.RevisionPlan = planRaw

		rounds = append(rounds, round)
		if cb.OnRoundComplete != nil {
			cb.OnRoundComplete(round)
		}

		// --- Actuator: revise article ---
		revised, err := ai.ReviseArticleStreaming(topic, article, planRaw, cb.OnArticleToken)
		if err != nil {
			log.Printf("Error revising article round %d: %v", i, err)
			break
		}
		article = revised
		log.Printf("Finished revision round %d for '%s'", i, topic)
	}

	if converged && cb.OnConverged != nil {
		cb.OnConverged()
	}

	// --- Post-loop: run metadata agents once on the final article ---
	references, infobox, seeAlso, categories, errs := runMetadataAgents(topic, article, cb.Metadata)
	if len(errs) > 0 {
		log.Printf("Warning: %d metadata agent(s) had errors for '%s'", len(errs), topic)
	}

	state := &ArticleState{
		Topic:          topic,
		CurrentArticle: article,
		References:     references,
		Infobox:        infobox,
		SeeAlso:        seeAlso,
		Categories:     categories,
		Rounds:         rounds,
		Converged:      converged,
	}

	return state, nil
}
