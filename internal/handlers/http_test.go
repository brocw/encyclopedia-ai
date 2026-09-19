package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type handlerTestAgent struct {
	generateErr error
}

func (a *handlerTestAgent) GenerateArticle(context.Context, string, func(string)) (string, error) {
	if a.generateErr != nil {
		return "", a.generateErr
	}
	return "article", nil
}

func (a *handlerTestAgent) EvaluateArticle(context.Context, string, func(string)) (string, error) {
	return `{"scores":{"factual_accuracy":8,"completeness":8,"neutrality":8,"clarity":8,"structure":8},"overall":8,"critical_issues":[]}`, nil
}

func (a *handlerTestAgent) PlanRevision(context.Context, string, string, func(string)) (string, error) {
	return `{"instructions":["improve"]}`, nil
}

func (a *handlerTestAgent) ReviseArticle(context.Context, string, string, string, func(string)) (string, error) {
	return "revised article", nil
}

func (a *handlerTestAgent) References(context.Context, string, func(string)) (string, error) {
	return `{"references":[]}`, nil
}

func (a *handlerTestAgent) Infobox(context.Context, string, string, func(string)) (string, error) {
	return `{"rows":[]}`, nil
}

func (a *handlerTestAgent) SeeAlso(context.Context, string, func(string)) (string, error) {
	return `{"topics":[]}`, nil
}

func (a *handlerTestAgent) CategorizeArticle(context.Context, string, func(string)) (string, error) {
	return `{"categories":[]}`, nil
}

func TestStartArticleEmitsStructuredDoneEvent(t *testing.T) {
	handler := New(&handlerTestAgent{})
	request := httptest.NewRequest(http.MethodPost, "/api/start", strings.NewReader(`{"topic":"Bacon","max_rounds":1}`))
	response := httptest.NewRecorder()

	handler.StartArticle(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: article_done") || !strings.Contains(body, "event: done") {
		t.Fatalf("SSE body missing completion events: %s", body)
	}
	if !strings.Contains(body, `"status":"complete"`) {
		t.Fatalf("SSE done event did not contain state: %s", body)
	}
	if strings.Contains(body, `data: "{\"topic\"`) {
		t.Fatalf("done event is still double-encoded: %s", body)
	}
}

func TestStartArticlePropagatesGenerationFailure(t *testing.T) {
	handler := New(&handlerTestAgent{generateErr: errors.New("Ollama unavailable")})
	request := httptest.NewRequest(http.MethodPost, "/api/start", strings.NewReader(`{"topic":"Bacon"}`))
	response := httptest.NewRecorder()

	handler.StartArticle(response, request)

	body := response.Body.String()
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "Ollama unavailable") {
		t.Fatalf("SSE body missing structured error: %s", body)
	}
	if !strings.Contains(body, `"status":"failed"`) {
		t.Fatalf("error event missing failed state: %s", body)
	}
}

func TestStartArticleValidatesMethodAndRoundLimit(t *testing.T) {
	handler := New(&handlerTestAgent{})

	methodResponse := httptest.NewRecorder()
	handler.StartArticle(methodResponse, httptest.NewRequest(http.MethodGet, "/api/start", nil))
	if methodResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", methodResponse.Code)
	}

	roundResponse := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/start", strings.NewReader(`{"topic":"Bacon","max_rounds":11}`))
	handler.StartArticle(roundResponse, request)
	if roundResponse.Code != http.StatusBadRequest {
		t.Fatalf("max round status = %d, want 400", roundResponse.Code)
	}
}
