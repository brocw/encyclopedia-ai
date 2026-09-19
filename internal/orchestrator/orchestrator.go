package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"encyclopedia-ai/internal/llm"
	"encyclopedia-ai/internal/plan"
)

// Agent is the AI boundary used by the loop. The production implementation is
// ai.Client; tests can provide a deterministic fake without starting Ollama.
//
// Each method returns the agent's answer and streams increments to an
// llm.Sink. The sink separates reasoning tokens from answer tokens, so a
// model's deliberation never reaches the article text or the JSON parsers.
type Agent interface {
	Intake(context.Context, string, llm.Sink) (string, error)
	Outline(context.Context, string, llm.Sink) (string, error)
	DraftLead(context.Context, string, string, llm.Sink) (string, error)
	DraftSection(context.Context, string, string, plan.Section, string, llm.Sink) (string, error)
	EvaluateArticle(context.Context, string, string, llm.Sink) (string, error)
	PlanRevision(context.Context, string, string, llm.Sink) (string, error)
	ReviseArticle(context.Context, string, string, string, llm.Sink) (string, error)
	References(context.Context, string, llm.Sink) (string, error)
	Infobox(context.Context, string, string, llm.Sink) (string, error)
	SeeAlso(context.Context, string, llm.Sink) (string, error)
	CategorizeArticle(context.Context, string, llm.Sink) (string, error)
}

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

const (
	StatusComplete = "complete"
	StatusPartial  = "partial"
	StatusFailed   = "failed"

	TerminationConverged = "converged"
	TerminationStagnated = "stagnated"
	TerminationMaxRounds = "max_rounds"
	TerminationError     = "error"
)

type ArticleState struct {
	Topic             string        `json:"topic"`
	Brief             *plan.Brief   `json:"brief,omitempty"`
	Outline           *plan.Outline `json:"outline,omitempty"`
	CurrentArticle    string        `json:"current_article"`
	References        string        `json:"references"`
	Infobox           string        `json:"infobox"`
	SeeAlso           string        `json:"see_also"`
	Categories        string        `json:"categories"`
	Rounds            []Round       `json:"rounds"`
	Converged         bool          `json:"converged"`
	Status            string        `json:"status"`
	TerminationReason string        `json:"termination_reason"`
	Error             string        `json:"error,omitempty"`
	Warnings          []string      `json:"warnings,omitempty"`
}

// MetadataCallbacks holds stream sinks for the metadata agents that run after the loop.
type MetadataCallbacks struct {
	OnReferences llm.Sink
	OnInfobox    llm.Sink
	OnSeeAlso    llm.Sink
	OnCategories llm.Sink
}

// LoopCallbacks holds a stream sink for each phase of the cybernetic loop,
// plus the notifications for documents and milestones the loop produces.
//
// The sinks carry raw model output, including the reasoning trace. The
// OnBriefReady and OnOutlineReady callbacks carry the parsed documents, which
// is what a client should render: nothing downstream should be reading a
// half-arrived JSON object off a token stream.
type LoopCallbacks struct {
	OnBrief        llm.Sink
	OnOutline      llm.Sink
	OnArticle      llm.Sink
	OnEvaluation   llm.Sink
	OnRevisionPlan llm.Sink

	OnPhase         func(Phase)
	OnBriefReady    func(*plan.Brief)
	OnOutlineReady  func(*plan.Outline)
	OnRoundComplete func(Round)
	OnConverged     func()
	Metadata        MetadataCallbacks
}

func (cb LoopCallbacks) phase(p Phase) {
	if cb.OnPhase != nil {
		cb.OnPhase(p)
	}
}

func (cb LoopCallbacks) roundComplete(round Round) {
	if cb.OnRoundComplete != nil {
		cb.OnRoundComplete(round)
	}
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

// hasConverged returns true if the evaluation meets the quality threshold and
// contains no critical issues.
func hasConverged(eval Evaluation) bool {
	return eval.Overall >= qualityThreshold && len(eval.CriticalIssues) == 0
}

// isStagnant returns true when the current score is not meaningfully better
// than the previous score. A decline is stagnation too: continuing to revise a
// worsening article is not useful without a different intervention.
func isStagnant(current, previous Evaluation) bool {
	return current.Overall <= previous.Overall+stagnationEpsilon
}

func isBetterEvaluation(candidate, best Evaluation) bool {
	if hasConverged(candidate) != hasConverged(best) {
		return hasConverged(candidate)
	}
	if candidate.Overall != best.Overall {
		return candidate.Overall > best.Overall
	}
	return len(candidate.CriticalIssues) < len(best.CriticalIssues)
}

func averageScores(scores Scores) float64 {
	return float64(scores.FactualAccuracy+scores.Completeness+scores.Neutrality+scores.Clarity+scores.Structure) / 5
}

func validateScores(scores Scores) error {
	values := []int{
		scores.FactualAccuracy,
		scores.Completeness,
		scores.Neutrality,
		scores.Clarity,
		scores.Structure,
	}
	for _, value := range values {
		if value < 1 || value > 10 {
			return fmt.Errorf("evaluation score %d is outside the allowed range 1-10", value)
		}
	}
	return nil
}

// parseEvaluation validates the model output and derives overall from the
// component scores. The model's redundant overall field is deliberately not
// trusted.
func parseEvaluation(raw string) (Evaluation, error) {
	var eval Evaluation
	if err := json.Unmarshal([]byte(raw), &eval); err != nil {
		return eval, fmt.Errorf("decode evaluation JSON: %w", err)
	}
	if err := validateScores(eval.Scores); err != nil {
		return eval, err
	}

	issues := eval.CriticalIssues[:0]
	for _, issue := range eval.CriticalIssues {
		if trimmed := strings.TrimSpace(issue); trimmed != "" {
			issues = append(issues, trimmed)
		}
	}
	eval.CriticalIssues = issues
	eval.Overall = averageScores(eval.Scores)
	return eval, nil
}

func metadataError(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s agent returned an empty response", name)
	}
	if !json.Valid([]byte(value)) {
		return fmt.Errorf("%s agent returned invalid JSON", name)
	}
	switch name {
	case "references":
		var payload struct {
			References []struct {
				Author    string `json:"author"`
				Title     string `json:"title"`
				Publisher string `json:"publisher"`
				Year      string `json:"year"`
			} `json:"references"`
		}
		if err := json.Unmarshal([]byte(value), &payload); err != nil {
			return fmt.Errorf("references agent returned invalid shape: %w", err)
		}
		if payload.References == nil {
			return fmt.Errorf("references agent response is missing references")
		}
	case "infobox":
		var payload struct {
			Rows []struct {
				Field string `json:"field"`
				Value string `json:"value"`
			} `json:"rows"`
		}
		if err := json.Unmarshal([]byte(value), &payload); err != nil {
			return fmt.Errorf("infobox agent returned invalid shape: %w", err)
		}
		if payload.Rows == nil {
			return fmt.Errorf("infobox agent response is missing rows")
		}
	case "see-also":
		var payload struct {
			Topics []string `json:"topics"`
		}
		if err := json.Unmarshal([]byte(value), &payload); err != nil {
			return fmt.Errorf("see-also agent returned invalid shape: %w", err)
		}
		if payload.Topics == nil {
			return fmt.Errorf("see-also agent response is missing topics")
		}
	case "categories":
		var payload struct {
			Categories []string `json:"categories"`
		}
		if err := json.Unmarshal([]byte(value), &payload); err != nil {
			return fmt.Errorf("categories agent returned invalid shape: %w", err)
		}
		if payload.Categories == nil {
			return fmt.Errorf("categories agent response is missing categories")
		}
	}
	return nil
}

func validateRevisionPlan(raw string) error {
	var plan struct {
		Instructions []string `json:"instructions"`
	}
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return fmt.Errorf("decode revision plan JSON: %w", err)
	}
	if len(plan.Instructions) == 0 || len(plan.Instructions) > 5 {
		return fmt.Errorf("revision plan must contain 1 to 5 instructions")
	}
	for _, instruction := range plan.Instructions {
		if strings.TrimSpace(instruction) == "" {
			return fmt.Errorf("revision plan contains an empty instruction")
		}
	}
	return nil
}

// runMetadataAgents launches the four metadata agents in parallel after the loop completes.
func runMetadataAgents(ctx context.Context, agent Agent, topic, article string, cb MetadataCallbacks) (references, infobox, seeAlso, categories string, errs []error) {
	results := make(chan agentResult, 4)
	var wg sync.WaitGroup

	wg.Add(4)

	go func() {
		defer wg.Done()
		val, err := agent.References(ctx, article, cb.OnReferences)
		if err == nil {
			err = metadataError("references", val)
		}
		results <- agentResult{"references", val, err}
	}()

	go func() {
		defer wg.Done()
		val, err := agent.Infobox(ctx, topic, article, cb.OnInfobox)
		if err == nil {
			err = metadataError("infobox", val)
		}
		results <- agentResult{"infobox", val, err}
	}()

	go func() {
		defer wg.Done()
		val, err := agent.SeeAlso(ctx, article, cb.OnSeeAlso)
		if err == nil {
			err = metadataError("see-also", val)
		}
		results <- agentResult{"seealso", val, err}
	}()

	go func() {
		defer wg.Done()
		val, err := agent.CategorizeArticle(ctx, article, cb.OnCategories)
		if err == nil {
			err = metadataError("categories", val)
		}
		results <- agentResult{"categories", val, err}
	}()

	wg.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			log.Printf("Error from %s agent: %v", result.name, result.err)
			errs = append(errs, fmt.Errorf("%s: %w", result.name, result.err))
			result.value = ""
		}
		switch result.name {
		case "references":
			references = result.value
		case "infobox":
			infobox = result.value
		case "seealso":
			seeAlso = result.value
		case "categories":
			categories = result.value
		}
	}

	return
}

func failureState(topic, article string, rounds []Round, reason string, err error) *ArticleState {
	state := &ArticleState{
		Topic:             topic,
		CurrentArticle:    article,
		Rounds:            rounds,
		Status:            StatusPartial,
		TerminationReason: reason,
	}
	if article == "" {
		state.Status = StatusFailed
	}
	if err != nil {
		state.Error = err.Error()
	}
	return state
}

// RunArticleLoop executes Intake → Outline → Draft → [Evaluate → Compare →
// Plan → Revise]* → Metadata. maxRounds is the maximum number of evaluated
// rounds. The last round is never revised without a subsequent evaluation.
func RunArticleLoop(ctx context.Context, topic string, maxRounds int, agent Agent, cb LoopCallbacks) (*ArticleState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if agent == nil {
		return failureState(topic, "", nil, TerminationError, fmt.Errorf("AI agent is not configured")), fmt.Errorf("AI agent is not configured")
	}
	if maxRounds <= 0 {
		return failureState(topic, "", nil, TerminationError, fmt.Errorf("max rounds must be greater than zero")), fmt.Errorf("max rounds must be greater than zero")
	}

	// brief and outline are captured by fail, so a failure at any later point
	// still returns the plan the article was being written to.
	var brief *plan.Brief
	var outline *plan.Outline
	fail := func(article string, rounds []Round, reason string, err error) *ArticleState {
		state := failureState(topic, article, rounds, reason, err)
		state.Brief, state.Outline = brief, outline
		if brief != nil {
			state.Topic = brief.Title
		}
		return state
	}

	brief, outline, article, err := draftArticle(ctx, agent, topic, cb)
	if err != nil {
		wrapped := fmt.Errorf("draft article: %w", err)
		return fail(article, nil, TerminationError, wrapped), wrapped
	}
	title := brief.Title
	briefText := brief.Prompt()
	log.Printf("Finished drafting article %q", title)

	rounds := make([]Round, 0, maxRounds)
	converged := false
	terminationReason := TerminationMaxRounds
	bestArticle := article
	var bestEvaluation Evaluation
	hasBestEvaluation := false

	for roundNumber := 1; roundNumber <= maxRounds; roundNumber++ {
		log.Printf("Starting evaluation round %d for %q", roundNumber, title)
		cb.phase(Phase{Name: PhaseEvaluate, Detail: title, Index: roundNumber, Total: maxRounds})

		evaluationRaw, err := agent.EvaluateArticle(ctx, briefText, article, cb.OnEvaluation)
		if err != nil {
			wrapped := fmt.Errorf("evaluate article round %d: %w", roundNumber, err)
			log.Printf("%v", wrapped)
			return fail(bestArticle, rounds, TerminationError, wrapped), wrapped
		}

		evaluation, err := parseEvaluation(evaluationRaw)
		if err != nil {
			wrapped := fmt.Errorf("parse evaluation round %d: %w", roundNumber, err)
			log.Printf("%v", wrapped)
			return fail(bestArticle, rounds, TerminationError, wrapped), wrapped
		}
		if !hasBestEvaluation || isBetterEvaluation(evaluation, bestEvaluation) {
			bestArticle = article
			bestEvaluation = evaluation
			hasBestEvaluation = true
		}

		round := Round{Number: roundNumber, Article: article, Evaluation: evaluation}

		if hasConverged(evaluation) {
			rounds = append(rounds, round)
			cb.roundComplete(round)
			converged = true
			terminationReason = TerminationConverged
			log.Printf("Article %q converged at round %d (overall: %.1f)", title, roundNumber, evaluation.Overall)
			break
		}

		if roundNumber > 1 && isStagnant(evaluation, rounds[len(rounds)-1].Evaluation) {
			rounds = append(rounds, round)
			cb.roundComplete(round)
			terminationReason = TerminationStagnated
			log.Printf("Article %q stagnated at round %d (overall: %.1f)", title, roundNumber, evaluation.Overall)
			break
		}

		// Do not create an unassessed final revision. The article in the last
		// recorded round is the article returned to the user.
		if roundNumber == maxRounds {
			rounds = append(rounds, round)
			cb.roundComplete(round)
			break
		}

		cb.phase(Phase{Name: PhasePlan, Detail: title, Index: roundNumber, Total: maxRounds})
		revisionPlan, err := agent.PlanRevision(ctx, article, evaluationRaw, cb.OnRevisionPlan)
		if err != nil {
			wrapped := fmt.Errorf("plan revision round %d: %w", roundNumber, err)
			log.Printf("%v", wrapped)
			rounds = append(rounds, round)
			cb.roundComplete(round)
			return fail(bestArticle, rounds, TerminationError, wrapped), wrapped
		}
		if err := validateRevisionPlan(revisionPlan); err != nil {
			wrapped := fmt.Errorf("validate revision plan round %d: %w", roundNumber, err)
			log.Printf("%v", wrapped)
			rounds = append(rounds, round)
			cb.roundComplete(round)
			return fail(bestArticle, rounds, TerminationError, wrapped), wrapped
		}
		round.RevisionPlan = revisionPlan
		rounds = append(rounds, round)
		cb.roundComplete(round)

		cb.phase(Phase{Name: PhaseRevise, Detail: title, Index: roundNumber, Total: maxRounds})

		// The reviser rewrites the article whole, so it starts from an empty
		// writer: the stream is repainted with the revision, not appended to.
		reviser := &articleWriter{sink: cb.OnArticle}
		revised, err := agent.ReviseArticle(ctx, briefText, article, revisionPlan, reviser.Draft())
		if err != nil {
			wrapped := fmt.Errorf("revise article round %d: %w", roundNumber, err)
			log.Printf("%v", wrapped)
			return fail(bestArticle, rounds, TerminationError, wrapped), wrapped
		}
		if revised = cleanProse(revised); revised == "" {
			wrapped := fmt.Errorf("revise article round %d: the model returned no prose", roundNumber)
			log.Printf("%v", wrapped)
			return fail(bestArticle, rounds, TerminationError, wrapped), wrapped
		}
		reviser.Commit(revised)
		article = revised
		log.Printf("Finished revision round %d for %q", roundNumber, title)
	}

	if converged && cb.OnConverged != nil {
		cb.OnConverged()
	}

	article = bestArticle
	cb.phase(Phase{Name: PhaseMetadata, Detail: title})
	references, infobox, seeAlso, categories, metadataErrs := runMetadataAgents(ctx, agent, title, article, cb.Metadata)
	state := &ArticleState{
		Topic:             title,
		Brief:             brief,
		Outline:           outline,
		CurrentArticle:    article,
		References:        references,
		Infobox:           infobox,
		SeeAlso:           seeAlso,
		Categories:        categories,
		Rounds:            rounds,
		Converged:         converged,
		Status:            StatusComplete,
		TerminationReason: terminationReason,
	}
	if len(metadataErrs) > 0 {
		state.Status = StatusPartial
		state.Warnings = make([]string, 0, len(metadataErrs))
		for _, metadataErr := range metadataErrs {
			state.Warnings = append(state.Warnings, metadataErr.Error())
		}
		log.Printf("Warning: %d metadata agent(s) had errors for %q", len(metadataErrs), title)
	}

	return state, nil
}
