package plan

import (
	"strings"
	"testing"
)

func TestParseBriefFallsBackToTheAskedTopic(t *testing.T) {
	brief, err := ParseBrief(`{"subject":"A cured pork product.","scope":["curing"," "]}`, "  bacon  ")
	if err != nil {
		t.Fatalf("ParseBrief returned error: %v", err)
	}
	if brief.Title != "bacon" {
		t.Errorf("title = %q, want the trimmed topic", brief.Title)
	}
	if len(brief.Scope) != 1 || brief.Scope[0] != "curing" {
		t.Errorf("scope = %q, want the blank entry dropped", brief.Scope)
	}
}

func TestParseBriefRejectsABriefThatDecidesNothing(t *testing.T) {
	cases := map[string]string{
		"no subject": `{"title":"Bacon","scope":["curing"]}`,
		"no scope":   `{"title":"Bacon","subject":"A cured pork product."}`,
		"not JSON":   `Bacon is cured pork.`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBrief(raw, "Bacon"); err == nil {
				t.Fatal("ParseBrief accepted a brief that fixes nothing")
			}
		})
	}
}

func TestParseOutlineRepairsWhatItCan(t *testing.T) {
	outline, err := ParseOutline(`{"sections":[
		{"heading":"Origins","purpose":"where it came from","key_questions":["when?",""],"target_words":5000},
		{"heading":" ","purpose":"nameless"},
		{"heading":"origins","purpose":"the same section again"},
		{"heading":"Production","target_words":0}
	],"open_questions":["is the date right?"]}`)
	if err != nil {
		t.Fatalf("ParseOutline returned error: %v", err)
	}
	if len(outline.Sections) != 2 {
		t.Fatalf("sections = %d, want the unnamed and duplicate ones dropped", len(outline.Sections))
	}
	if outline.Sections[0].TargetWords != MaxTargetWords {
		t.Errorf("target words = %d, want it clamped", outline.Sections[0].TargetWords)
	}
	if outline.Sections[1].TargetWords != DefaultTargetWords {
		t.Errorf("missing target words = %d, want the default", outline.Sections[1].TargetWords)
	}
	if got := outline.Sections[0].KeyQuestions; len(got) != 1 {
		t.Errorf("key questions = %q, want the blank one dropped", got)
	}
}

func TestParseOutlineTruncatesAnOverlongOutline(t *testing.T) {
	var sections []string
	for i := range MaxSections + 4 {
		sections = append(sections, `{"heading":"Section `+string(rune('A'+i))+`"}`)
	}
	outline, err := ParseOutline(`{"sections":[` + strings.Join(sections, ",") + `]}`)
	if err != nil {
		t.Fatalf("ParseOutline returned error: %v", err)
	}
	if len(outline.Sections) != MaxSections {
		t.Fatalf("sections = %d, want %d", len(outline.Sections), MaxSections)
	}
}

func TestParseOutlineRejectsAnOutlineTooShortToBeAnArticle(t *testing.T) {
	if _, err := ParseOutline(`{"sections":[{"heading":"Origins"}]}`); err == nil {
		t.Fatal("ParseOutline accepted a one-section outline")
	}
}

// Retrieval reads the outline through Questions, so the order has to survive.
func TestOutlineQuestionsAreCollectedInSectionOrder(t *testing.T) {
	outline := &Outline{Sections: []Section{
		{Heading: "Origins", KeyQuestions: []string{"when?", "where?"}},
		{Heading: "Production", KeyQuestions: []string{"how?"}},
	}}
	got := strings.Join(outline.Questions(), "|")
	if got != "when?|where?|how?" {
		t.Fatalf("questions = %q", got)
	}
}

func TestPromptsCarryWhatTheNextAgentNeeds(t *testing.T) {
	brief := &Brief{
		Title:      "Bacon",
		Subject:    "A cured pork product.",
		Kind:       "food",
		Scope:      []string{"curing"},
		Exclusions: []string{"Francis Bacon"},
	}
	for _, want := range []string{"Bacon", "A cured pork product.", "food", "curing", "Francis Bacon"} {
		if !strings.Contains(brief.Prompt(), want) {
			t.Errorf("brief prompt is missing %q:\n%s", want, brief.Prompt())
		}
	}

	outline := &Outline{
		Sections:      []Section{{Heading: "Origins", Purpose: "where it came from", TargetWords: 150}},
		OpenQuestions: []string{"is the date right?"},
	}
	for _, want := range []string{"Origins", "where it came from", "150"} {
		if !strings.Contains(outline.Prompt(), want) {
			t.Errorf("outline prompt is missing %q:\n%s", want, outline.Prompt())
		}
	}
	// A writer shown the planner's doubts writes about them: an early run put
	// "current research questions include..." into the lead.
	if strings.Contains(outline.Prompt(), "is the date right?") {
		t.Errorf("outline prompt leaks its open questions to the writer:\n%s", outline.Prompt())
	}

	section := Section{Heading: "Origins", Purpose: "where it came from", KeyQuestions: []string{"when?"}, TargetWords: 150}
	for _, want := range []string{"Origins", "where it came from", "when?", "150"} {
		if !strings.Contains(section.Prompt(), want) {
			t.Errorf("section prompt is missing %q:\n%s", want, section.Prompt())
		}
	}
}

// A nil document is rendered rather than panicking: a prompt is built from
// whatever the pipeline has, including nothing.
func TestNilDocumentsRender(t *testing.T) {
	var brief *Brief
	var outline *Outline
	if brief.Prompt() == "" || outline.Prompt() == "" {
		t.Fatal("a nil document rendered as empty")
	}
	if outline.Questions() != nil {
		t.Fatal("a nil outline produced questions")
	}
}
