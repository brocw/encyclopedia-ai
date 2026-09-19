// Package plan holds the two documents that come before the prose: the brief
// that fixes what an article is about, and the outline that fixes what it
// covers.
//
// They live in their own package because both sides of the agent boundary read
// their fields — the agents to build prompts, the loop to iterate sections —
// and because retrieval will hang evidence off an outline's key questions.
package plan

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Bounds on an outline. A model asked for "a few sections" will occasionally
// return thirty, and every section is a separate generation, so the ceiling is
// a cost control as much as an editorial one.
const (
	MinSections        = 2
	MaxSections        = 8
	MaxKeyQuestions    = 5
	MaxScopeItems      = 8
	MaxListItems       = 6
	MinTargetWords     = 80
	MaxTargetWords     = 500
	DefaultTargetWords = 180
)

// Brief is the intake agent's reading of a topic: what the article is about,
// what belongs in it, and which other readings of the same words were set
// aside. Everything that writes or judges the article is shown this document,
// so that "complete" and "off-topic" mean something specific.
type Brief struct {
	Title       string   `json:"title"`
	Subject     string   `json:"subject"`
	Kind        string   `json:"kind,omitempty"`
	Scope       []string `json:"scope"`
	Exclusions  []string `json:"exclusions,omitempty"`
	Ambiguities []string `json:"ambiguities,omitempty"`
}

// Section is one planned part of the article.
type Section struct {
	Heading      string   `json:"heading"`
	Purpose      string   `json:"purpose"`
	KeyQuestions []string `json:"key_questions"`
	TargetWords  int      `json:"target_words"`
}

// Outline is the section plan for an article body. The lead is not a section:
// it is written separately as a summary of everything below it.
type Outline struct {
	Sections      []Section `json:"sections"`
	OpenQuestions []string  `json:"open_questions,omitempty"`
}

// ParseBrief decodes and normalizes an intake response. topic is the user's
// original words, used when the model declines to restate a title.
func ParseBrief(raw, topic string) (*Brief, error) {
	var brief Brief
	if err := json.Unmarshal([]byte(raw), &brief); err != nil {
		return nil, fmt.Errorf("decode brief JSON: %w", err)
	}

	brief.Title = strings.TrimSpace(brief.Title)
	if brief.Title == "" {
		brief.Title = strings.TrimSpace(topic)
	}
	brief.Subject = strings.TrimSpace(brief.Subject)
	brief.Kind = strings.TrimSpace(brief.Kind)
	brief.Scope = cleanList(brief.Scope, MaxScopeItems)
	brief.Exclusions = cleanList(brief.Exclusions, MaxListItems)
	brief.Ambiguities = cleanList(brief.Ambiguities, MaxListItems)

	if brief.Title == "" {
		return nil, fmt.Errorf("brief has no title")
	}
	if brief.Subject == "" {
		return nil, fmt.Errorf("brief has no subject sentence")
	}
	if len(brief.Scope) == 0 {
		return nil, fmt.Errorf("brief lists nothing in scope")
	}
	return &brief, nil
}

// ParseOutline decodes and normalizes an outline response.
//
// Repairable problems are repaired rather than rejected: an unnamed or
// duplicated section is dropped, an absurd word target is clamped, and an
// over-long outline is truncated. Only an outline too short to be an article
// is an error, because there is nothing to fall back to.
func ParseOutline(raw string) (*Outline, error) {
	var outline Outline
	if err := json.Unmarshal([]byte(raw), &outline); err != nil {
		return nil, fmt.Errorf("decode outline JSON: %w", err)
	}

	seen := make(map[string]bool, len(outline.Sections))
	sections := make([]Section, 0, len(outline.Sections))
	for _, section := range outline.Sections {
		section.Heading = strings.TrimSpace(section.Heading)
		if section.Heading == "" {
			continue
		}
		key := strings.ToLower(section.Heading)
		if seen[key] {
			continue
		}
		seen[key] = true

		section.Purpose = strings.TrimSpace(section.Purpose)
		section.KeyQuestions = cleanList(section.KeyQuestions, MaxKeyQuestions)
		section.TargetWords = clampWords(section.TargetWords)
		sections = append(sections, section)

		if len(sections) == MaxSections {
			break
		}
	}

	if len(sections) < MinSections {
		return nil, fmt.Errorf("outline has %d usable sections, need at least %d", len(sections), MinSections)
	}
	outline.Sections = sections
	outline.OpenQuestions = cleanList(outline.OpenQuestions, MaxListItems)
	return &outline, nil
}

// Questions returns every key question in the outline, in section order. It is
// the retrieval layer's entry point: these are the things the article has
// committed to answering.
func (o *Outline) Questions() []string {
	if o == nil {
		return nil
	}
	var questions []string
	for _, section := range o.Sections {
		questions = append(questions, section.KeyQuestions...)
	}
	return questions
}

// Prompt renders the brief for inclusion in another agent's prompt.
func (b *Brief) Prompt() string {
	if b == nil {
		return "(no brief)"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Title: %s\n", b.Title)
	fmt.Fprintf(&out, "Subject: %s\n", b.Subject)
	if b.Kind != "" {
		fmt.Fprintf(&out, "Kind of subject: %s\n", b.Kind)
	}
	writeList(&out, "In scope", b.Scope)
	writeList(&out, "Out of scope", b.Exclusions)
	writeList(&out, "Not this article", b.Ambiguities)
	return strings.TrimRight(out.String(), "\n")
}

// Prompt renders the outline for inclusion in another agent's prompt.
func (o *Outline) Prompt() string {
	if o == nil || len(o.Sections) == 0 {
		return "(no outline)"
	}
	var out strings.Builder
	out.WriteString("Lead: an untitled summary paragraph, written first.\n")
	for i, section := range o.Sections {
		fmt.Fprintf(&out, "%d. %s (~%d words)", i+1, section.Heading, section.TargetWords)
		if section.Purpose != "" {
			fmt.Fprintf(&out, " — %s", section.Purpose)
		}
		out.WriteString("\n")
	}
	writeList(&out, "Open questions", o.OpenQuestions)
	return strings.TrimRight(out.String(), "\n")
}

// Prompt renders one section's assignment.
func (s Section) Prompt() string {
	var out strings.Builder
	fmt.Fprintf(&out, "Heading: %s\n", s.Heading)
	if s.Purpose != "" {
		fmt.Fprintf(&out, "Purpose: %s\n", s.Purpose)
	}
	fmt.Fprintf(&out, "Target length: about %d words\n", s.TargetWords)
	writeList(&out, "Questions this section must answer", s.KeyQuestions)
	return strings.TrimRight(out.String(), "\n")
}

func writeList(out *strings.Builder, label string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(out, "%s:\n", label)
	for _, item := range items {
		fmt.Fprintf(out, "- %s\n", item)
	}
}

func cleanList(items []string, limit int) []string {
	cleaned := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		cleaned = append(cleaned, trimmed)
		if len(cleaned) == limit {
			break
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func clampWords(words int) int {
	switch {
	case words <= 0:
		return DefaultTargetWords
	case words < MinTargetWords:
		return MinTargetWords
	case words > MaxTargetWords:
		return MaxTargetWords
	default:
		return words
	}
}
