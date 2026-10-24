# Foundry Router: Model-Agnostic Context Gateway

A high-throughput reverse proxy and intelligent traffic gateway written in Go that intercepts OpenAI-compatible API requests. It dynamically routes requests between local Ollama instances and cloud endpoints based on a strict token-budget threshold, with Redis rate limiting and asynchronous telemetry streaming to `foundry-pulse`.

## Features
- **Dynamic Token Budget Routing**: Evaluates OpenAI Chat Completion prompt payloads:
  - `< 1000 tokens`: Dispatched to low-latency local Ollama instance (`http://localhost:11434/v1/chat/completions`).
  - `>= 1000 tokens`: Dispatched to Cloud LLM upstream.
- **Distributed & In-Memory Rate Limiting**: Redis sliding-minute window with automatic fallback to local token buckets.
- **Observability Pipeline**: Streams request metrics (latency, token count, route chosen) to the Local Telemetry Ingestion Engine (`/ingest`).
- **Streaming Reverse Proxy**: Full support for server-sent events (SSE) streaming tokens.

## Running Locally
```bash
go mod tidy
go run main.go
```
