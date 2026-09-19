#!/usr/bin/env bash

set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$PROJECT_DIR"

echo "Starting Ollama service..."
# export HSA_OVERRIDE_GTX_VERSION="12.0.1"
export ROCR_VISIBLE_DEVICES="${ROCR_VISIBLE_DEVICES:-1}"
export HIP_VISIBLE_DEVICES="${HIP_VISIBLE_DEVICES:-1}"
ollama serve &
OLLAMA_PID=$!

cleanup() {
    if kill -0 "$OLLAMA_PID" 2>/dev/null; then
        kill "$OLLAMA_PID" 2>/dev/null || true
        wait "$OLLAMA_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT INT TERM

# Wait for Ollama to be ready
echo "Waiting for Ollama to be ready..."
until curl -s http://localhost:11434/ > /dev/null 2>&1; do
    sleep 1
done
echo "Ollama is ready."

# Pull whichever models are configured, so switching to a reasoning model is
# a matter of setting LLM_TEXT_MODEL / LLM_STRUCTURED_MODEL.
TEXT_MODEL="${LLM_TEXT_MODEL:-${OLLAMA_TEXT_MODEL:-llama3.1}}"
STRUCTURED_MODEL="${LLM_STRUCTURED_MODEL:-${OLLAMA_STRUCTURED_MODEL:-mistral}}"

echo "Ensuring required models are available..."
ollama pull "$TEXT_MODEL"
if [ "$STRUCTURED_MODEL" != "$TEXT_MODEL" ]; then
    ollama pull "$STRUCTURED_MODEL"
fi

echo "Starting Encyclopedia-AI server (text=$TEXT_MODEL structured=$STRUCTURED_MODEL think=${LLM_THINK:-off})..."
go run ./cmd/server
