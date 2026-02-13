package ai

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

var apiURL string = "http://localhost:11434/api/generate"

//go:embed generate_prompt.txt
var generatePrompt string

//go:embed revise_prompt.txt
var revisePrompt string

//go:embed categorize_prompt.txt
var categorizePrompt string

//go:embed factcheck_prompt.txt
var factcheckPrompt string

//go:embed references_prompt.txt
var referencesPrompt string

//go:embed infobox_prompt.txt
var infoboxPrompt string

//go:embed seealso_prompt.txt
var seealsoPrompt string

// JSON structure for a request to Ollama API
type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

// JSON structure for a response from the Ollama API
type ollamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// Sends prompts to a specific model (non-streaming)
func callOllama(model, prompt string) (string, error) {
	log.Printf("[ollama] Starting request to model '%s'...", model)

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

// CallOllamaStreaming sends a prompt and calls onToken for each chunk of text.
// Returns the full concatenated response.
func CallOllamaStreaming(model, prompt string, onToken func(string)) (string, error) {
	log.Printf("[ollama-stream] Starting streaming request to model '%s'...", model)

	reqData := ollamaRequest{
		Model:  model,
		Prompt: prompt,
		Stream: true,
	}

	jsonData, err := json.Marshal(reqData)
	if err != nil {
		return "", fmt.Errorf("error marshalling json: %w", err)
	}

	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("error sending request to ollama: %w", err)
	}
	defer resp.Body.Close()

	var full string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var chunk ollamaResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			continue
		}
		if chunk.Response != "" {
			full += chunk.Response
			if onToken != nil {
				onToken(chunk.Response)
			}
		}
		if chunk.Done {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return full, fmt.Errorf("error reading streaming response: %w", err)
	}

	log.Printf("[ollama-stream] Model '%s' finished. Response length: %d chars.", model, len(full))
	return full, nil
}

// GenerateArticle creates first draft (non-streaming)
func GenerateArticle(topic string) (string, error) {
	prompt := fmt.Sprintf(generatePrompt, topic)
	return callOllama("llama3.1", prompt)
}

// GenerateArticleStreaming creates first draft with token streaming
func GenerateArticleStreaming(topic string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(generatePrompt, topic)
	return CallOllamaStreaming("llama3.1", prompt, onToken)
}

// ReviseArticleStreaming revises draft based on fact-check findings
func ReviseArticleStreaming(topic, article, factcheck string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(revisePrompt, topic, article, factcheck)
	return CallOllamaStreaming("llama3.1", prompt, onToken)
}

// CategorizeArticleStreaming generates categories for an article
func CategorizeArticleStreaming(article string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(categorizePrompt, article)
	return CallOllamaStreaming("mistral", prompt, onToken)
}

// FactCheckStreaming analyzes article for potential inaccuracies
func FactCheckStreaming(article string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(factcheckPrompt, article)
	return CallOllamaStreaming("llama3.1", prompt, onToken)
}

// ReferencesStreaming generates a reference list for the article
func ReferencesStreaming(article string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(referencesPrompt, article)
	return CallOllamaStreaming("mistral", prompt, onToken)
}

// InfoboxStreaming generates a Wikipedia-style infobox table
func InfoboxStreaming(topic, article string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(infoboxPrompt, topic, article)
	return CallOllamaStreaming("mistral", prompt, onToken)
}

// SeeAlsoStreaming generates related topic suggestions
func SeeAlsoStreaming(article string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(seealsoPrompt, article)
	return CallOllamaStreaming("mistral", prompt, onToken)
}
