package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"encyclopedia-ai/internal/llm"
	"encyclopedia-ai/internal/plan"
)

// Phase names reported through LoopCallbacks.OnPhase. They are the pipeline's
// vocabulary for "what is happening now", and the client drives its progress
// display from them rather than guessing from which stream is producing text.
const (
	PhaseIntake   = "intake"
	PhaseOutline  = "outline"
	PhaseLead     = "lead"
	PhaseSection  = "section"
	PhaseEvaluate = "evaluate"
	PhasePlan     = "plan"
	PhaseRevise   = "revise"
	PhaseMetadata = "metadata"
)

// Phase reports a step of the pipeline. Index and Total are set where the step
// is one of a known number, such as drafting the fourth of six sections.
type Phase struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	Index  int    `json:"index,omitempty"`
	Total  int    `json:"total,omitempty"`
}

// draftArticle replaces one-shot generation. The topic is framed into a brief,
// the brief is planned into an outline, and each section is written separately
// against that plan and against the draft already written.
//
// It returns whatever it managed to produce even when it fails, so a run that
// dies at the fourth section still shows the reader the first three.
func draftArticle(ctx context.Context, agent Agent, topic string, cb LoopCallbacks) (*plan.Brief, *plan.Outline, string, error) {
	cb.phase(Phase{Name: PhaseIntake, Detail: topic})
	briefRaw, err := agent.Intake(ctx, topic, cb.OnBrief)
	if err != nil {
		return nil, nil, "", fmt.Errorf("intake: %w", err)
	}
	brief, err := plan.ParseBrief(briefRaw, topic)
	if err != nil {
		return nil, nil, "", fmt.Errorf("intake: %w", err)
	}
	if cb.OnBriefReady != nil {
		cb.OnBriefReady(brief)
	}
	log.Printf("Brief for %q: %q (%d in scope)", topic, brief.Title, len(brief.Scope))

	cb.phase(Phase{Name: PhaseOutline, Detail: brief.Title})
	outlineRaw, err := agent.Outline(ctx, brief.Prompt(), cb.OnOutline)
	if err != nil {
		return brief, nil, "", fmt.Errorf("outline: %w", err)
	}
	outline, err := plan.ParseOutline(outlineRaw)
	if err != nil {
		return brief, nil, "", fmt.Errorf("outline: %w", err)
	}
	if cb.OnOutlineReady != nil {
		cb.OnOutlineReady(outline)
	}
	log.Printf("Outline for %q: %d sections, %d open questions", brief.Title, len(outline.Sections), len(outline.OpenQuestions))

	briefText, outlineText := brief.Prompt(), outline.Prompt()
	steps := len(outline.Sections) + 1
	writer := &articleWriter{sink: cb.OnArticle}

	cb.phase(Phase{Name: PhaseLead, Detail: brief.Title, Index: 1, Total: steps})
	lead, err := agent.DraftLead(ctx, briefText, outlineText, writer.Draft())
	if err != nil {
		return brief, outline, writer.Text(), fmt.Errorf("draft lead: %w", err)
	}
	if lead = cleanProse(lead); lead == "" {
		return brief, outline, "", fmt.Errorf("draft lead: the model returned no prose")
	}
	writer.Commit(lead)

	for i, section := range outline.Sections {
		cb.phase(Phase{Name: PhaseSection, Detail: section.Heading, Index: i + 2, Total: steps})

		// The section is written against the article so far but not against
		// its own heading, which the loop writes rather than the model.
		soFar := writer.Text()
		writer.Write("\n\n## " + section.Heading + "\n\n")

		body, err := agent.DraftSection(ctx, briefText, outlineText, section, soFar, writer.Draft())
		if err != nil {
			return brief, outline, writer.Text(), fmt.Errorf("draft section %q: %w", section.Heading, err)
		}
		if body = cleanProse(body); body == "" {
			return brief, outline, writer.Text(), fmt.Errorf("draft section %q: the model returned no prose", section.Heading)
		}
		writer.Commit(body)
		log.Printf("Drafted section %d/%d of %q: %s", i+1, len(outline.Sections), brief.Title, section.Heading)
	}

	return brief, outline, writer.Text(), nil
}

// articleWriter assembles the article while mirroring it to the article
// stream. Its own copy is authoritative: whenever the stream and the assembled
// text can no longer agree — a repair retry that answers again from the start,
// or a section whose answer needed cleaning — the writer repaints the stream
// from what it holds, and the client paints the article rather than a guess at
// it.
type articleWriter struct {
	sink      llm.Sink
	assembled strings.Builder

	// streamed is what the agent call in flight has sent so far, kept only to
	// notice when the committed text differs from it.
	streamed strings.Builder
}

// Text is the article assembled so far.
func (w *articleWriter) Text() string { return w.assembled.String() }

// Write adds text the loop produced itself, such as a section heading.
func (w *articleWriter) Write(text string) {
	w.assembled.WriteString(text)
	w.sink.Emit(llm.Delta{Content: text})
}

// Draft returns the sink for one agent call. Call it after any heading that
// belongs before the call's output.
func (w *articleWriter) Draft() llm.Sink {
	w.streamed.Reset()
	return func(delta llm.Delta) {
		if delta.Restart {
			w.streamed.Reset()
			w.repaint()
		}
		w.streamed.WriteString(delta.Content)
		w.sink.Emit(llm.Delta{Content: delta.Content, Reasoning: delta.Reasoning})
	}
}

// Commit records an agent's finished answer.
func (w *articleWriter) Commit(text string) {
	diverged := text != w.streamed.String()
	w.assembled.WriteString(text)
	w.streamed.Reset()
	if diverged {
		w.repaint()
	}
}

func (w *articleWriter) repaint() { repaintArticle(w.sink, w.assembled.String()) }

// repaintArticle replaces whatever the article stream is showing with text.
// The server holds the article; a client is only ever shown a copy of it.
func repaintArticle(sink llm.Sink, text string) {
	sink.Emit(llm.Delta{Restart: true})
	if text != "" {
		sink.Emit(llm.Delta{Content: text})
	}
}

// minRevisionRetained is the fraction of an article a revision must keep.
//
// The reviser rewrites the article whole, and a model asked to re-emit a long
// article with a few edits will sometimes summarize it instead: one run came
// back at 45% of its input, having dropped half the coverage. The evaluator
// does not catch this — it scored the shortened article 9.2 against the
// original's 8.4, because tighter prose reads better and nothing in the rubric
// notices that a third of the subject went missing. So the gate sits here,
// before the evaluation that would reward the loss.
const minRevisionRetained = 0.75

// revisionIsUsable reports whether a revision is an edit of the article rather
// than a summary of it.
func revisionIsUsable(original, revised string) error {
	if len(original) == 0 {
		return nil
	}
	retained := float64(len(revised)) / float64(len(original))
	if retained < minRevisionRetained {
		return fmt.Errorf("the revision kept only %.0f%% of the article, so it summarized rather than revised it", retained*100)
	}
	return nil
}

// cleanProse removes the wrappings a model adds to prose it was asked to emit
// bare: a code fence around the whole answer, and a heading the loop already
// wrote for it.
func cleanProse(text string) string {
	text = stripCodeFence(strings.TrimSpace(text))
	for range 2 {
		stripped := stripLeadingHeading(text)
		if stripped == text {
			break
		}
		text = stripped
	}
	return strings.TrimSpace(text)
}

func stripCodeFence(text string) string {
	if !strings.HasPrefix(text, "```") || !strings.HasSuffix(text, "```") {
		return text
	}
	_, rest, found := strings.Cut(text, "\n")
	if !found {
		return text
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "```"))
}

func stripLeadingHeading(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "#") {
		return text
	}
	_, rest, _ := strings.Cut(text, "\n")
	return strings.TrimSpace(rest)
}
