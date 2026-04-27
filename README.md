# AI Research Mindmapper Backend

FastAPI backend for the AI Research Mindmapper frontend.

## Environment

Required for real Groq responses:

```bash
export GROQ_API_KEY="your-groq-api-key"
```

Optional:

```bash
export GROQ_MODEL="groq/compound-mini"
export DATABASE_URL="sqlite:///./mindmapper.db"
```

## Run

```bash
uvicorn app.main:app --host 127.0.0.1 --port 8008 --reload
```

The existing frontend expects:

```text
http://127.0.0.1:8008
```

## Notes

- TXT and webpage URL ingestion work with the installed dependencies.
- PDF extraction uses `pypdf` when installed; otherwise the API returns a clear dependency warning.
- YouTube transcript extraction uses `youtube-transcript-api` when installed; otherwise the API returns a clear dependency warning.
- File upload is implemented without `python-multipart` by manually accepting simple one-file multipart uploads or raw bytes.
