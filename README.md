# AI Research Mindmapper — Backend

FastAPI backend serving the Research Mindmapper frontend (classic + Canvas).
Deployed on Vercel serverless; data in Turso (hosted SQLite); auth via Clerk JWTs.

## Architecture

```
app/main.py        API routes, CORS, background-job orchestration
app/auth.py        Clerk JWT verification (+ extension/script key bypasses)
app/ratelimit.py   Per-user rate limits (DB-backed, works across instances)
app/groq_client.py LLM calls: Groq → Gemini → OpenRouter fallback chain
app/ingest.py      URL / TXT / PDF / YouTube ingestion + chunking + SSRF guard
app/storage.py     Turso (libsql over HTTP) — batched writes, indexes
api/index.py       Vercel entry point
extension/         Chrome extension for browser-side YouTube transcripts
tests/             pytest suite (no network needed)
scripts/           load tests + local helpers
```

## Setup

```bash
python -m venv venv && source venv/bin/activate
pip install -r requirements.txt
cp .env.example .env   # fill in GROQ_API_KEY, TURSO_URL, TURSO_AUTH_TOKEN
```

Turso is **required** (storage refuses to start without it) — free tier is fine.

## Run

```bash
uvicorn app.main:app --host 127.0.0.1 --port 8008 --reload
```

For local dev without rate limits: `RATE_LIMIT_DISABLED=1`.

## Test

```bash
python -m pytest tests/ -v
```

## API surface

| Method | Path                          | Purpose                              |
|--------|-------------------------------|--------------------------------------|
| GET    | /api/health                   | health + youtube status + version    |
| POST   | /api/research                 | topic → full research map (LLM)      |
| POST   | /api/sources                  | ingest URL / file (YouTube → async job) |
| GET    | /api/jobs/{id}                | poll async job status                |
| POST   | /api/sessions/{id}/generate   | synthesize map from stored evidence  |
| POST   | /api/sessions/{id}/ask        | Q&A over session evidence            |
| GET    | /api/sessions                 | list user's sessions                 |
| GET    | /api/sessions/{id}            | session detail + sources + Q&A       |
| DELETE | /api/sessions/{id}            | delete session + all dependent rows  |

All routes except `/api/health` require `Authorization: Bearer <Clerk JWT>`.
LLM routes are rate limited per user (429 + `Retry-After` when exceeded).

## Scale notes

- Chunk inserts are batched (one HTTP roundtrip per source, not per chunk).
- Rate limiting is DB-backed so it holds across serverless instances.
- LLM fallback chain is budgeted to finish inside Vercel's 60s limit.
- Set `INTERNAL_SECRET` in prod so YouTube jobs run on a separate worker
  invocation; without it they run inline (slower but safe).
