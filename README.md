# AI Research Mindmapper — Go Backend

High-performance Go backend rewrite of the AI Research Mindmapper API, providing instant cold starts (<5ms), minimal memory footprint, and robust concurrency for web research, YouTube transcription, and mindmap generation.

---

## ⚡ Key Improvements over Python Backend

| Feature | Python (FastAPI + uvicorn) | Go (Chi + native net/http) |
|---|---|---|
| **Cold Start** | ~1.5s – 3.0s | **< 5ms** |
| **Memory Footprint** | ~120MB – 250MB | **~15MB – 25MB** |
| **Concurrency** | ThreadPoolExecutor + async event loop | **Native lightweight goroutines** |
| **Packaging** | Python runtime + venv + site-packages | **Single static executable binary** |
| **Database Driver** | Python `libsql-client` | **Native HTTP LibSQL pipeline (zero-CGO)** |

---

## 🏛 Architecture

```
cmd/server/
  └── main.go          Entrypoint, graceful signal shutdown, config injection
pkg/
  ├── auth/            Clerk RS256 JWKS verification + test & extension bypass keys
  ├── config/          Environment variables loader (.env & system env)
  ├── db/              Turso / LibSQL HTTP client (v1/execute & v1/batch), migrations, storage repository
  ├── handler/         Chi HTTP router, CORS middleware, REST endpoints
  ├── ingest/          Text chunker, SSRF firewall, webpage extractor (goquery), PDF & TXT, YouTube
  ├── llm/             Groq (Llama 3.1) → Gemini → OpenRouter fallback chain & offline synthesis
  └── ratelimit/       Database-backed sliding windows (per-minute and per-day) with fail-open
```

---

## 🚀 Getting Started

### Prerequisites
- Go 1.22+ (tested on Go 1.27.2)
- Turso Database (`TURSO_URL` and `TURSO_AUTH_TOKEN`)

### Quick Start

1. Copy `.env.example` to `.env` and fill in your keys:
   ```bash
   cp .env.example .env
   ```

2. Build and run the server:
   ```bash
   go build -o bin/server cmd/server/main.go
   ./bin/server
   ```
   Or run directly with live reload / go run:
   ```bash
   go run cmd/server/main.go
   ```

3. Run the automated test suite:
   ```bash
   go test ./... -v
   ```

---

## 🐳 Docker Deployment

Build and run using the lightweight multi-stage Dockerfile:

```bash
# Build Docker image
docker build -t mindmapper-backend-go .

# Run container with environment file
docker run -p 8008:8008 --env-file .env mindmapper-backend-go
```

---

## 📡 API Surface

All routes except `/api/health` require `Authorization: Bearer <Clerk JWT>` (or bypass key).

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/health` | Healthcheck, YouTube status, version |
| `POST` | `/api/research` | Generates mindmap from topic string |
| `POST` | `/api/sources` | Ingests URL (article/YouTube) or uploaded file (.txt/.pdf) |
| `GET` | `/api/jobs/{id}` | Polls async job status (used for YouTube audio processing) |
| `POST` | `/api/internal/jobs/{id}/run` | Worker endpoint for processing queued jobs |
| `POST` | `/api/sessions/{id}/generate` | Synthesizes mindmap from stored session evidence |
| `POST` | `/api/sessions/{id}/ask` | Q&A with semantic ranking over evidence chunks |
| `GET` | `/api/sessions` | Lists user's research sessions |
| `GET` | `/api/sessions/{id}` | Session details, sources, chunks, and Q&A history |
| `DELETE` | `/api/sessions/{id}` | Deletes session and cascade-deletes related rows |
