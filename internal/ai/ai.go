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
	Done     bool   `json:"done"`
}

// Sends prompts to a specific model (non-streaming)
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

// CallOllamaStreaming sends a prompt and calls onToken for each chunk of text.
// Returns the full concatenated response.
func CallOllamaStreaming(model, prompt string, onToken func(string)) (string, error) {
	log.Printf("[ollama-stream] Starting streaming request to model '%s'...", model)
	apiURL := "http://localhost:11434/api/generate"

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

// WriterAI creates first draft
func GenerateArticle(topic string) (string, error) {
	prompt := fmt.Sprintf(generatePrompt, topic)
	return callOllama("llama3.1", prompt)
}

// GenerateArticleStreaming creates first draft with token streaming
func GenerateArticleStreaming(topic string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(generatePrompt, topic)
	return CallOllamaStreaming("llama3.1", prompt, onToken)
}

// ReaderAI critiques draft
func CritiqueArticle(article string) (string, error) {
	prompt := fmt.Sprintf(critiquePrompt, article)
	return callOllama("mistral", prompt)
}

// CritiqueArticleStreaming critiques draft with token streaming
func CritiqueArticleStreaming(article string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(critiquePrompt, article)
	return CallOllamaStreaming("mistral", prompt, onToken)
}

// WriterAI revises draft
func ReviseArticle(topic, article, critique string) (string, error) {
	prompt := fmt.Sprintf(revisePrompt, topic, article, critique)
	return callOllama("llama3.1", prompt)
}

// ReviseArticleStreaming revises draft with token streaming
func ReviseArticleStreaming(topic, article, critique string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(revisePrompt, topic, article, critique)
	return CallOllamaStreaming("llama3.1", prompt, onToken)
}
