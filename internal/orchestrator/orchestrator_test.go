package orchestrator

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"encyclopedia-ai/internal/llm"
	"encyclopedia-ai/internal/plan"
)

type fakeAgent struct {
	mu            sync.Mutex
	evaluations   []string
	evaluationAt  int
	planErr       error
	reviseErr     error
	planCalls     int
	reviseCalls   int
	metadataCalls int

	// briefs records what the judging and rewriting agents were told the
	// article was supposed to be.
	briefs []string
}

func evaluationJSON(scores [5]int) string {
	return `{"scores":{"factual_accuracy":` + strconv.Itoa(scores[0]) +
		`,"completeness":` + strconv.Itoa(scores[1]) +
		`,"neutrality":` + strconv.Itoa(scores[2]) +
		`,"clarity":` + strconv.Itoa(scores[3]) +
		`,"structure":` + strconv.Itoa(scores[4]) +
		`},"overall":1,"critical_issues":[]}`
}

// The fake plans two sections, so a drafted article is a lead plus the two
// headings the loop writes itself plus two bodies.
const (
	fakeBrief      = `{"title":"Topic","subject":"A subject.","kind":"concept","scope":["origins","use"]}`
	fakeOutline    = `{"sections":[{"heading":"Alpha","purpose":"a","key_questions":["q"],"target_words":100},{"heading":"Beta","purpose":"b","key_questions":["q"],"target_words":100}]}`
	draftedArticle = "initial article\n\n## Alpha\n\nAlpha body.\n\n## Beta\n\nBeta body."
)

func (f *fakeAgent) Intake(context.Context, string, llm.Sink) (string, error) {
	return fakeBrief, nil
}

func (f *fakeAgent) Outline(context.Context, string, llm.Sink) (string, error) {
	return fakeOutline, nil
}

func (f *fakeAgent) DraftLead(context.Context, string, string, llm.Sink) (string, error) {
	return "initial article", nil
}

func (f *fakeAgent) DraftSection(_ context.Context, _, _ string, section plan.Section, _ string, _ llm.Sink) (string, error) {
	return section.Heading + " body.", nil
}

func (f *fakeAgent) EvaluateArticle(_ context.Context, brief, _ string, _ llm.Sink) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.briefs = append(f.briefs, brief)
	index := f.evaluationAt
	if index >= len(f.evaluations) {
		index = len(f.evaluations) - 1
	}
	f.evaluationAt++
	return f.evaluations[index], nil
}

func (f *fakeAgent) PlanRevision(context.Context, string, string, llm.Sink) (string, error) {
	f.mu.Lock()
	f.planCalls++
	f.mu.Unlock()
	if f.planErr != nil {
		return "", f.planErr
	}
	return `{"instructions":["improve the article"]}`, nil
}

func (f *fakeAgent) ReviseArticle(_ context.Context, brief, _, _ string, _ llm.Sink) (string, error) {
	f.mu.Lock()
	f.reviseCalls++
	f.briefs = append(f.briefs, brief)
	f.mu.Unlock()
	if f.reviseErr != nil {
		return "", f.reviseErr
	}
	return "revised article", nil
}

func (f *fakeAgent) References(context.Context, string, llm.Sink) (string, error) {
	f.mu.Lock()
	f.metadataCalls++
	f.mu.Unlock()
	return `{"references":[]}`, nil
}

func (f *fakeAgent) Infobox(context.Context, string, string, llm.Sink) (string, error) {
	f.mu.Lock()
	f.metadataCalls++
	f.mu.Unlock()
	return `{"rows":[]}`, nil
}

func (f *fakeAgent) SeeAlso(context.Context, string, llm.Sink) (string, error) {
	f.mu.Lock()
	f.metadataCalls++
	f.mu.Unlock()
	return `{"topics":[]}`, nil
}

func (f *fakeAgent) CategorizeArticle(context.Context, string, llm.Sink) (string, error) {
	f.mu.Lock()
	f.metadataCalls++
	f.mu.Unlock()
	return `{"categories":[]}`, nil
}

func (f *fakeAgent) counts() (planCalls, reviseCalls, metadataCalls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.planCalls, f.reviseCalls, f.metadataCalls
}

func TestParseEvaluationDerivesOverallFromScores(t *testing.T) {
	eval, err := parseEvaluation(evaluationJSON([5]int{10, 8, 6, 4, 2}))
	if err != nil {
		t.Fatalf("parseEvaluation returned error: %v", err)
	}
	if eval.Overall != 6 {
		t.Fatalf("overall = %v, want 6", eval.Overall)
	}
}

func TestParseEvaluationRejectsOutOfRangeScores(t *testing.T) {
	_, err := parseEvaluation(evaluationJSON([5]int{11, 8, 6, 4, 2}))
	if err == nil {
		t.Fatal("parseEvaluation accepted an out-of-range score")
	}
}

func TestRunArticleLoopDoesNotReviseAfterLastRound(t *testing.T) {
	fake := &fakeAgent{evaluations: []string{evaluationJSON([5]int{7, 7, 7, 7, 7})}}
	state, err := RunArticleLoop(context.Background(), "Topic", 1, fake, LoopCallbacks{})
	if err != nil {
		t.Fatalf("RunArticleLoop returned error: %v", err)
	}
	planCalls, reviseCalls, metadataCalls := fake.counts()
	if planCalls != 0 || reviseCalls != 0 {
		t.Fatalf("final round was revised: plan=%d revise=%d", planCalls, reviseCalls)
	}
	if metadataCalls != 4 {
		t.Fatalf("metadata calls = %d, want 4", metadataCalls)
	}
	if state.TerminationReason != TerminationMaxRounds || len(state.Rounds) != 1 {
		t.Fatalf("unexpected termination state: reason=%q rounds=%d", state.TerminationReason, len(state.Rounds))
	}
	if state.CurrentArticle != draftedArticle {
		t.Fatalf("current article = %q, want the drafted article", state.CurrentArticle)
	}
}

func TestRunArticleLoopStopsOnStagnation(t *testing.T) {
	fake := &fakeAgent{evaluations: []string{
		evaluationJSON([5]int{7, 7, 7, 7, 7}),
		evaluationJSON([5]int{7, 8, 7, 7, 7}),
	}}
	state, err := RunArticleLoop(context.Background(), "Topic", 3, fake, LoopCallbacks{})
	if err != nil {
		t.Fatalf("RunArticleLoop returned error: %v", err)
	}
	planCalls, reviseCalls, _ := fake.counts()
	if planCalls != 1 || reviseCalls != 1 {
		t.Fatalf("unexpected revision calls: plan=%d revise=%d", planCalls, reviseCalls)
	}
	if state.TerminationReason != TerminationStagnated || len(state.Rounds) != 2 {
		t.Fatalf("unexpected stagnation state: reason=%q rounds=%d", state.TerminationReason, len(state.Rounds))
	}
}

func TestRunArticleLoopReturnsBestEvaluatedArticleAfterRegression(t *testing.T) {
	fake := &fakeAgent{evaluations: []string{
		evaluationJSON([5]int{7, 7, 7, 8, 8}),
		evaluationJSON([5]int{6, 6, 6, 6, 6}),
	}}
	state, err := RunArticleLoop(context.Background(), "Topic", 3, fake, LoopCallbacks{})
	if err != nil {
		t.Fatalf("RunArticleLoop returned error: %v", err)
	}
	if state.CurrentArticle != draftedArticle {
		t.Fatalf("current article = %q, want best evaluated article", state.CurrentArticle)
	}
}

func TestRunArticleLoopReturnsPartialStateOnRevisionPlanError(t *testing.T) {
	fake := &fakeAgent{
		evaluations: []string{evaluationJSON([5]int{7, 7, 7, 7, 7})},
		planErr:     errors.New("planner unavailable"),
	}
	state, err := RunArticleLoop(context.Background(), "Topic", 2, fake, LoopCallbacks{})
	if err == nil {
		t.Fatal("RunArticleLoop returned nil error")
	}
	if state.Status != StatusPartial || state.TerminationReason != TerminationError {
		t.Fatalf("unexpected failure state: status=%q reason=%q", state.Status, state.TerminationReason)
	}
	if !strings.Contains(state.Error, "planner unavailable") {
		t.Fatalf("state error = %q", state.Error)
	}
	_, _, metadataCalls := fake.counts()
	if metadataCalls != 0 {
		t.Fatalf("metadata ran after loop failure: %d calls", metadataCalls)
	}
}

// The brief is the article's specification: the agents that judge and rewrite
// the article are shown it, so "complete" and "off-topic" mean something.
func TestRunArticleLoopCarriesThePlanThroughTheLoop(t *testing.T) {
	fake := &fakeAgent{evaluations: []string{
		evaluationJSON([5]int{7, 7, 7, 7, 7}),
		evaluationJSON([5]int{9, 9, 9, 9, 9}),
	}}
	state, err := RunArticleLoop(context.Background(), "topic", 2, fake, LoopCallbacks{})
	if err != nil {
		t.Fatalf("RunArticleLoop returned error: %v", err)
	}

	if state.Brief == nil || state.Outline == nil {
		t.Fatal("the finished state does not carry the plan the article was written to")
	}
	if state.Topic != "Topic" {
		t.Errorf("topic = %q, want the title the brief settled on", state.Topic)
	}
	if len(state.Outline.Sections) != 2 {
		t.Errorf("outline sections = %d, want 2", len(state.Outline.Sections))
	}

	fake.mu.Lock()
	briefs := fake.briefs
	fake.mu.Unlock()
	if len(briefs) < 3 {
		t.Fatalf("briefs passed = %d, want one per evaluation and revision", len(briefs))
	}
	for _, brief := range briefs {
		if !strings.Contains(brief, "A subject.") {
			t.Fatalf("an agent was given %q instead of the brief", brief)
		}
	}
}
