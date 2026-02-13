package ai

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

//go:embed generate_prompt.txt
var generatePrompt string

//go:embed critique_prompt.txt
var critiquePrompt string

//go:embed revise_prompt.txt
var revisePrompt string

// JSON structure for a request to Ollama API
type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

// JSON structure for a response from the Ollama API
type ollamaResponse struct {
	Response string `json:"response"`
}

// Sends prompts to a specific model
func callOllama(model, prompt string) (string, error) {
	log.Printf("[ollama] Starting request to model '%s'...", model)
	apiURL := "http://localhost:11434/api/generate"

	// Prepare the request data
	reqData := ollamaRequest{
		Model:  model,
		Prompt: prompt,
		Stream: false, // Full response
	}

	jsonData, err := json.Marshal(reqData)
	if err != nil {
		return "", fmt.Errorf("Error marshalling json: %w", err)
	}

	// Send HTTP POST request
	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("Error sending request to ollama: %w", err)
	}

	// Read, parse response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("Error reading response body: %w", err)
	}

	var ollamaResp ollamaResponse
	if err := json.Unmarshal(body, &ollamaResp); err != nil {
		return "", fmt.Errorf("Error unmarshalling ollama response: %w", err)
	}

	response := ollamaResp.Response
	preview := response
	if len(preview) > 20 {
		preview = preview[:20]
	}
	log.Printf("[ollama] Model '%s' finished. Response length: %d chars. Preview: %q", model, len(response), preview)

	return response, nil
}

// WriterAI creates first draft
func GenerateArticle(topic string) (string, error) {
	prompt := fmt.Sprintf(generatePrompt, topic)
	return callOllama("llama2", prompt)
}

// ReaderAI critiques draft
func CritiqueArticle(article string) (string, error) {
	prompt := fmt.Sprintf(critiquePrompt, article)
	return callOllama("mistral", prompt)
}

// WriterAI revises draft
func ReviseArticle(topic, article, critique string) (string, error) {
	prompt := fmt.Sprintf(revisePrompt, topic, article, critique)
	return callOllama("llama2", prompt)
}
