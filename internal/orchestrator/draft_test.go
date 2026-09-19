package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"encyclopedia-ai/internal/llm"
	"encyclopedia-ai/internal/plan"
)

// draftFake answers only the drafting agents; the loop's other methods are
// never reached by these tests.
type draftFake struct {
	fakeAgent
	brief   string
	outline string
	lead    func(llm.Sink) (string, error)
	section func(plan.Section, llm.Sink) (string, error)
}

func (f *draftFake) Intake(context.Context, string, llm.Sink) (string, error) {
	return f.brief, nil
}

func (f *draftFake) Outline(context.Context, string, llm.Sink) (string, error) {
	return f.outline, nil
}

func (f *draftFake) DraftLead(_ context.Context, _, _ string, sink llm.Sink) (string, error) {
	if f.lead != nil {
		return f.lead(sink)
	}
	return stream(sink, "The lead."), nil
}

func (f *draftFake) DraftSection(_ context.Context, _, _ string, section plan.Section, _ string, sink llm.Sink) (string, error) {
	if f.section != nil {
		return f.section(section, sink)
	}
	return stream(sink, section.Heading+" body."), nil
}

// stream emits an answer the way a provider does and returns it.
func stream(sink llm.Sink, text string) string {
	sink.Emit(llm.Delta{Content: text})
	return text
}

func newDraftFake() *draftFake {
	return &draftFake{
		brief:   `{"title":"Bacon","subject":"A cured pork product.","scope":["curing"]}`,
		outline: `{"sections":[{"heading":"Origins"},{"heading":"Production"}]}`,
	}
}

// painter reproduces what a client does with the article stream, so a test can
// assert on the text a reader actually ends up looking at.
type painter struct{ text strings.Builder }

func (p *painter) sink() llm.Sink {
	return func(delta llm.Delta) {
		if delta.Restart {
			p.text.Reset()
		}
		p.text.WriteString(delta.Content)
	}
}

const wantDraft = "The lead.\n\n## Origins\n\nOrigins body.\n\n## Production\n\nProduction body."

func TestDraftArticleAssemblesTheOutlineIntoAnArticle(t *testing.T) {
	var painted painter
	brief, outline, article, err := draftArticle(context.Background(), newDraftFake(), "bacon", LoopCallbacks{OnArticle: painted.sink()})
	if err != nil {
		t.Fatalf("draftArticle returned error: %v", err)
	}
	if brief.Title != "Bacon" || len(outline.Sections) != 2 {
		t.Fatalf("brief = %+v, outline = %+v", brief, outline)
	}
	if article != wantDraft {
		t.Fatalf("article = %q\nwant %q", article, wantDraft)
	}
	// The stream is the article, not an approximation of it.
	if painted.text.String() != article {
		t.Fatalf("painted = %q\nwant %q", painted.text.String(), article)
	}
}

func TestDraftArticleReportsItsPhasesInOrder(t *testing.T) {
	var phases []string
	_, _, _, err := draftArticle(context.Background(), newDraftFake(), "bacon", LoopCallbacks{
		OnPhase: func(p Phase) { phases = append(phases, p.Name+":"+p.Detail) },
	})
	if err != nil {
		t.Fatalf("draftArticle returned error: %v", err)
	}
	want := "intake:bacon|outline:Bacon|lead:Bacon|section:Origins|section:Production"
	if got := strings.Join(phases, "|"); got != want {
		t.Fatalf("phases = %q\nwant %q", got, want)
	}
}

// A repair retry answers again from the beginning. Only the section in flight
// is stale, so the stream must come back showing the sections already written.
func TestDraftArticleRepaintsTheArticleAfterARepairRestart(t *testing.T) {
	fake := newDraftFake()
	fake.section = func(section plan.Section, sink llm.Sink) (string, error) {
		if section.Heading == "Production" {
			sink.Emit(llm.Delta{Content: "a truncated answ"})
			sink.Emit(llm.Delta{Restart: true})
		}
		return stream(sink, section.Heading+" body."), nil
	}

	var painted painter
	_, _, article, err := draftArticle(context.Background(), fake, "bacon", LoopCallbacks{OnArticle: painted.sink()})
	if err != nil {
		t.Fatalf("draftArticle returned error: %v", err)
	}
	if article != wantDraft {
		t.Fatalf("article = %q", article)
	}
	if painted.text.String() != article {
		t.Fatalf("painted = %q\nwant %q", painted.text.String(), article)
	}
}

// The loop writes the headings, so a section that writes its own is cleaned —
// and the stream is repainted, because the client already showed the duplicate.
func TestDraftArticleStripsASectionThatWritesItsOwnHeading(t *testing.T) {
	fake := newDraftFake()
	fake.section = func(section plan.Section, sink llm.Sink) (string, error) {
		return stream(sink, "```markdown\n## "+section.Heading+"\n\n"+section.Heading+" body.\n```"), nil
	}

	var painted painter
	_, _, article, err := draftArticle(context.Background(), fake, "bacon", LoopCallbacks{OnArticle: painted.sink()})
	if err != nil {
		t.Fatalf("draftArticle returned error: %v", err)
	}
	if article != wantDraft {
		t.Fatalf("article = %q\nwant %q", article, wantDraft)
	}
	if painted.text.String() != article {
		t.Fatalf("painted = %q\nwant %q", painted.text.String(), article)
	}
}

func TestDraftArticleReturnsWhatItHadWhenASectionFails(t *testing.T) {
	fake := newDraftFake()
	fake.section = func(section plan.Section, sink llm.Sink) (string, error) {
		if section.Heading == "Production" {
			return "", errors.New("provider unavailable")
		}
		return stream(sink, section.Heading+" body."), nil
	}

	brief, outline, article, err := draftArticle(context.Background(), fake, "bacon", LoopCallbacks{})
	if err == nil {
		t.Fatal("draftArticle returned nil error")
	}
	if brief == nil || outline == nil {
		t.Fatal("the plan was discarded along with the failure")
	}
	if !strings.Contains(article, "Origins body.") {
		t.Fatalf("partial article = %q, want the sections that were written", article)
	}
}

func TestDraftArticleFailsOnAnUnusablePlan(t *testing.T) {
	t.Run("brief", func(t *testing.T) {
		fake := newDraftFake()
		fake.brief = `{"title":"Bacon"}`
		if _, _, _, err := draftArticle(context.Background(), fake, "bacon", LoopCallbacks{}); err == nil {
			t.Fatal("draftArticle accepted a brief with no scope")
		}
	})
	t.Run("outline", func(t *testing.T) {
		fake := newDraftFake()
		fake.outline = `{"sections":[]}`
		brief, _, _, err := draftArticle(context.Background(), fake, "bacon", LoopCallbacks{})
		if err == nil {
			t.Fatal("draftArticle accepted an empty outline")
		}
		if brief == nil {
			t.Fatal("the brief was discarded with the outline failure")
		}
	})
}

func TestCleanProseRemovesModelWrappings(t *testing.T) {
	cases := map[string]string{
		"```markdown\n# Bacon\n\nBacon is cured pork.\n```": "Bacon is cured pork.",
		"## Origins\n\nBacon is cured pork.":                "Bacon is cured pork.",
		"  Bacon is cured pork.  ":                          "Bacon is cured pork.",
		"Bacon is cured pork.\n\n## Curing\n\nSalt.":        "Bacon is cured pork.\n\n## Curing\n\nSalt.",
	}
	for input, want := range cases {
		if got := cleanProse(input); got != want {
			t.Errorf("cleanProse(%q) = %q, want %q", input, got, want)
		}
	}
}
