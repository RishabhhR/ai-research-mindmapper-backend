# AI Research Mindmapper — Go backend

This is the active API for the Research Mindmapper frontend. The Python backend is legacy. The Go service uses Clerk JWT authentication, Turso storage, Groq/Gemini/OpenRouter for synthesis, and optional transcript providers for YouTube.

## Run locally

Requires Go 1.27+, `TURSO_URL`, `TURSO_AUTH_TOKEN`, the frontend's `CLERK_PUBLISHABLE_KEY`, and at least one of `GROQ_API_KEY`, `GEMINI_API_KEY`, or `OPENROUTER_API_KEY` for AI generation. Copy `.env.example` to `.env`, fill in the values, then run:

```bash
go run ./cmd/server
```

The server listens on port 8008 by default. `go test ./...` runs local tests; `RUN_LIVE_TESTS=1 go test ./...` calls real providers and Turso. Live tests can consume provider quota and write temporary database records.

## What the API does

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/health` | Checks service and storage availability |
| POST | `/api/research` | Creates an AI topic overview |
| POST | `/api/sources` | Imports a webpage, YouTube video, TXT, or PDF |
| GET | `/api/jobs/{id}` | Polls and processes an authenticated YouTube import |
| POST | `/api/sessions/{id}/generate` | Creates a map from stored source text |
| POST | `/api/sessions/{id}/ask` | Answers with retrieved source excerpts |
| GET | `/api/sessions` | Lists the signed-in user's sessions |
| GET | `/api/sessions/{id}` | Returns a session, sources, excerpts, and Q&A |
| PUT | `/api/sessions/{id}/map` | Saves map edits and node positions |
| DELETE | `/api/sessions/{id}` | Deletes one session |
| DELETE | `/api/sessions` | Deletes all sessions for the signed-in user |

All routes except health require a Clerk bearer token. `/api/internal/jobs/{id}/run` additionally requires `X-Internal-Secret` if configured.

Topic overviews use model knowledge and are labelled AI synthesis. There is currently **no live web search tool**; entering a topic does not retrieve web pages or academic papers. Import a source for evidence-backed maps and answers. Source retrieval is lexical, not semantic; answers may miss paraphrases or material absent from the selected excerpts.

YouTube transcripts try Supadata, the frontend edge transcript endpoint, then local `yt-dlp` if available. The Vercel Go runtime does not include `yt-dlp`; deploy the Docker image or configure a working transcript service for that fallback. The Docker image includes `yt-dlp`, and `YOUTUBE_COOKIES_B64` can supply its cookies. Some videos still block transcript access.

## Deployment

`vercel.json` builds `api/index.go` as the Vercel entrypoint. For a standalone server with the YouTube CLI fallback:

```bash
docker build -t mindmapper-backend-go .
docker run -p 8008:8008 --env-file .env mindmapper-backend-go
```

The package layout is `pkg/auth`, `pkg/config`, `pkg/db`, `pkg/handler`, `pkg/ingest`, `pkg/llm`, and `pkg/ratelimit`. Database migrations run at startup. Source import and chunk insertion are transactional; storage failures return an error rather than reporting a saved map.
