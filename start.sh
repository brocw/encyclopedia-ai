#!/bin/bash

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$PROJECT_DIR"

echo "Starting Ollama service..."
ollama serve &
OLLAMA_PID=$!

# Wait for Ollama to be ready
echo "Waiting for Ollama to be ready..."
until curl -s http://localhost:11434/ > /dev/null 2>&1; do
    sleep 1
done
echo "Ollama is ready."

# Pull models if not already available
echo "Ensuring required models are available..."
ollama pull llama2
ollama pull mistral

echo "Starting Encyclopedia-AI server..."
go run ./cmd/server

# Clean up Ollama when the Go server exits
kill $OLLAMA_PID 2>/dev/null
