import json
import os
from typing import Optional

import requests as _requests
from dotenv import load_dotenv
load_dotenv()

from fastapi import Depends, FastAPI, HTTPException, Request
from fastapi.middleware.cors import CORSMiddleware
from pydantic import BaseModel, Field

from . import auth, groq_client, ingest, storage

# Self-call URL used to trigger background job workers.
# Set BACKEND_URL in Vercel env to your production URL.
BACKEND_URL = os.getenv("BACKEND_URL", "https://mindmapper-api-mu.vercel.app").rstrip("/")

# Shared secret that the main function passes when calling its own worker endpoint.
# Set INTERNAL_SECRET to any random string in Vercel env vars.
INTERNAL_SECRET = os.getenv("INTERNAL_SECRET", "")

app = FastAPI(title="AI Research Mindmapper API")
storage.init_db()

_allowed_origins = ["http://localhost:4173", "http://127.0.0.1:4173", "http://localhost:8008", "null"]
_frontend_url = os.getenv("FRONTEND_URL")
if _frontend_url:
    _allowed_origins.append(_frontend_url)

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=False,
    allow_methods=["*"],
    allow_headers=["*"],
)


class ResearchRequest(BaseModel):
    query: str = Field(min_length=1)
    depth: str = "Detailed"
    output: str = "Mindmap"
    source: str = "Academic + web"


class SourceUrlRequest(BaseModel):
    url: str = Field(min_length=1)
    session_id: Optional[str] = None
    topic: Optional[str] = None


class GenerateRequest(BaseModel):
    topic: Optional[str] = None
    depth: str = "Detailed"
    output: str = "Mindmap"
    source: str = "Academic + web"


class AskRequest(BaseModel):
    question: str = Field(min_length=1)


@app.get("/api/health")
def health():
    # Set YOUTUBE_STATUS to "degraded" or "down" in Vercel env when
    # Supadata quota runs out — the frontend will show a banner immediately.
    yt_status = os.getenv("YOUTUBE_STATUS", "ok")
    return {"ok": True, "youtube_status": yt_status}


@app.post("/api/research")
def research(payload: ResearchRequest, user_id: str = Depends(auth.get_user_id)):
    session_id = storage.create_session(payload.query, payload.depth, payload.output, user_id=user_id)
    data = groq_client.generate_research(payload.query, payload.depth, payload.output, source=payload.source, web_search=True)
    summary = data.get("summary", "")
    if isinstance(summary, list):
        summary = "\n".join(str(s) for s in summary)
    storage.save_generation(
        session_id,
        summary,
        data.get("nodes", []),
        data.get("insights", []),
        data.get("tradeoffs", []),
        data.get("citations", []),
    )
    data["summary"] = summary
    return normalize_generation(session_id, payload.query, payload.depth, payload.output, data)


@app.post("/api/sources")
async def add_source(request: Request, user_id: str = Depends(auth.get_user_id)):
    content_type = request.headers.get("content-type", "")
    try:
        if "application/json" in content_type:
            payload = SourceUrlRequest.model_validate(await request.json())

            # ── YouTube: async job so we never block on the 10-60 s pipeline ──
            if ingest.is_youtube_url(payload.url):
                session_id = payload.session_id or storage.create_session(
                    payload.topic or payload.url, user_id=user_id
                )
                job_id = storage.create_job(
                    session_id, user_id, "youtube", {"url": payload.url}
                )
                _fire_job(job_id)
                return {
                    "id": session_id,
                    "session_id": session_id,
                    "job_id": job_id,
                    "source_type": "youtube",
                    "status": "pending",
                    "chunks_count": 0,
                    "warnings": [],
                }

            # ── All other URLs: synchronous (fast) ───────────────────────────
            session_id = payload.session_id or storage.create_session(payload.topic or payload.url, user_id=user_id)
            title, chunks, warnings, source_type = ingest.extract_url(payload.url)
            source_id = storage.add_source(session_id, source_type, title, url=payload.url)
            storage.add_chunks(session_id, source_id, source_type, title, chunks, url=payload.url)
            return {
                "id": session_id,
                "session_id": session_id,
                "source_id": source_id,
                "source_type": source_type,
                "title": title,
                "chunks_count": len(chunks),
                "warnings": warnings,
            }

        body = await request.body()
        filename = request.headers.get("x-filename", "upload.txt")
        if "multipart/form-data" in content_type:
            filename, body = ingest.parse_single_file_multipart(content_type, body)
        session_id = request.headers.get("x-session-id") or storage.create_session(filename, user_id=user_id)
        title, chunks, warnings, source_type = ingest.extract_file(filename, body)
        source_id = storage.add_source(session_id, source_type, title)
        storage.add_chunks(session_id, source_id, source_type, title, chunks)
        return {
            "id": session_id,
            "session_id": session_id,
            "source_id": source_id,
            "source_type": source_type,
            "title": title,
            "chunks_count": len(chunks),
            "warnings": warnings,
        }
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


def _fire_job(job_id: str) -> None:
    """
    Trigger the worker endpoint without waiting for its response.
    Uses a very short read timeout (0.1 s) so the main function returns quickly.
    The worker is a separate Vercel function instance with its own timeout budget.
    """
    try:
        _requests.post(
            f"{BACKEND_URL}/api/internal/jobs/{job_id}/run",
            headers={"X-Internal-Secret": INTERNAL_SECRET},
            timeout=(3, 0.1),   # connect: 3 s  |  read: 0.1 s (intentionally short)
        )
    except Exception:
        pass   # ReadTimeout is expected — worker received the request and is running


@app.post("/api/internal/jobs/{job_id}/run")
async def run_job(job_id: str, request: Request):
    """
    Internal worker: called by _fire_job(), not by the frontend.
    Runs the heavy YouTube pipeline with the full function-timeout budget (60 s on Pro).
    Protected by X-Internal-Secret header.
    """
    secret = request.headers.get("X-Internal-Secret", "")
    if INTERNAL_SECRET and secret != INTERNAL_SECRET:
        raise HTTPException(status_code=403, detail="Forbidden")

    job = storage.get_job(job_id)
    if not job:
        raise HTTPException(status_code=404, detail="Job not found")
    if job["status"] not in ("pending",):
        return {"ok": True, "status": job["status"]}   # idempotent

    storage.update_job(job_id, "processing")

    try:
        inp = job.get("input", {})
        if job["type"] == "youtube":
            url = inp["url"]
            session_id = job["session_id"]
            title, chunks, warnings, source_type = ingest.extract_youtube(url)
            source_id = storage.add_source(session_id, source_type, title, url=url)
            if chunks:
                storage.add_chunks(session_id, source_id, source_type, title, chunks, url=url)
            storage.update_job(job_id, "done", result={
                "title": title,
                "source_id": source_id,
                "session_id": session_id,
                "source_type": source_type,
                "chunks_count": len(chunks),
                "warnings": warnings,
            })
        else:
            storage.update_job(job_id, "failed", error=f"Unknown job type: {job['type']}")
    except Exception as exc:
        storage.update_job(job_id, "failed", error=str(exc)[:500])

    return {"ok": True}


@app.get("/api/jobs/{job_id}")
def get_job_status(job_id: str, user_id: str = Depends(auth.get_user_id)):
    """Fast polling endpoint — returns job status + result when done."""
    job = storage.get_job(job_id, user_id=user_id)
    if not job:
        raise HTTPException(status_code=404, detail="Job not found")
    out = {
        "job_id": job_id,
        "status": job["status"],
        "session_id": job["session_id"],
    }
    if job["status"] == "done" and job.get("result"):
        out.update(job["result"])
    elif job["status"] == "failed":
        out["error"] = job.get("error") or "Unknown error"
    return out


@app.post("/api/sessions/{session_id}/generate")
def generate(session_id: str, payload: GenerateRequest, user_id: str = Depends(auth.get_user_id)):
    session = storage.get_session(session_id, user_id=user_id)
    if not session:
        raise HTTPException(status_code=404, detail="Session not found.")
    chunks = storage.get_chunks(session_id)
    topic = payload.topic or session["topic"]
    use_web = len(chunks) == 0
    data = groq_client.generate_research(topic, payload.depth, payload.output, chunks, source=payload.source, web_search=use_web)
    summary = data.get("summary", "")
    if isinstance(summary, list):
        summary = "\n".join(str(s) for s in summary)
    storage.save_generation(
        session_id,
        summary,
        data.get("nodes", []),
        data.get("insights", []),
        data.get("tradeoffs", []),
        data.get("citations", []),
    )
    data["summary"] = summary
    return normalize_generation(session_id, topic, payload.depth, payload.output, data)


@app.post("/api/sessions/{session_id}/ask")
def ask(session_id: str, payload: AskRequest, user_id: str = Depends(auth.get_user_id)):
    if not storage.get_session(session_id, user_id=user_id):
        raise HTTPException(status_code=404, detail="Session not found.")
    chunks = rank_chunks(storage.get_chunks(session_id), payload.question)
    use_web = len(chunks) < 2
    data = groq_client.answer_question(payload.question, chunks, use_web)
    storage.add_qa(
        session_id,
        payload.question,
        data.get("answer", ""),
        data.get("citations", []),
        data.get("provenance", "ai_synthesized"),
    )
    return data


@app.get("/api/sessions")
def sessions(user_id: str = Depends(auth.get_user_id)):
    return {"sessions": storage.list_sessions(user_id=user_id)}


@app.get("/api/sessions/{session_id}")
def session_detail(session_id: str, user_id: str = Depends(auth.get_user_id)):
    session = storage.get_session(session_id, user_id=user_id)
    if not session:
        raise HTTPException(status_code=404, detail="Session not found.")
    return {
        "session": decode_session(session),
        "sources": storage.get_sources(session_id),
        "chunks": storage.get_chunks(session_id, limit=20),
        "qa_history": [decode_qa(row) for row in storage.get_qa(session_id)],
    }


def normalize_generation(session_id: str, topic: str, depth: str, output: str, data):
    return {
        "id": session_id,
        "topic": topic,
        "depth": depth,
        "output": output,
        "summary": data.get("summary", ""),
        "root": topic,
        "nodes": data.get("nodes", []),
        "insights": data.get("insights", []),
        "sources": data.get("sources", []),
        "tradeoffs": data.get("tradeoffs", []),
        "citations": data.get("citations", []),
        "warnings": data.get("warnings", []),
    }


def decode_session(row):
    item = dict(row)
    for key in ["mindmap_json", "insights_json", "tradeoffs_json", "citations_json"]:
        if item.get(key):
            item[key.replace("_json", "")] = json.loads(item[key])
    return item


def decode_qa(row):
    item = dict(row)
    item["citations"] = json.loads(item.pop("citations_json") or "[]")
    return item


def rank_chunks(chunks, question: str):
    terms = {term.lower() for term in question.split() if len(term) > 3}
    scored = []
    for chunk in chunks:
        text = chunk.get("text", "").lower()
        score = sum(1 for term in terms if term in text)
        if score:
            scored.append((score, chunk))
    if not scored:
        return chunks[:4]
    return [chunk for _, chunk in sorted(scored, key=lambda item: item[0], reverse=True)[:6]]
