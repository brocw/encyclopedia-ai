#!/bin/bash

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$PROJECT_DIR"

echo "Starting Ollama service..."
# export HSA_OVERRIDE_GTX_VERSION="12.0.1"
export ROCR_VISIBLE_DEVICES=1
export HIP_VISIBLE_DEVICES=1
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
ollama pull llama3.1
ollama pull mistral

echo "Starting Encyclopedia-AI server..."
go run ./cmd/server

# Clean up Ollama when the Go server exits
kill $OLLAMA_PID 2>/dev/null
