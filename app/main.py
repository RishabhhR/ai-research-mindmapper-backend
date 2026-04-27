import json
import os
from typing import Optional

from dotenv import load_dotenv
load_dotenv()

from fastapi import Depends, FastAPI, HTTPException, Request
from fastapi.middleware.cors import CORSMiddleware
from pydantic import BaseModel, Field

from . import auth, groq_client, ingest, storage

app = FastAPI(title="AI Research Mindmapper API")
storage.init_db()

_allowed_origins = ["http://localhost:4173", "http://127.0.0.1:4173", "http://localhost:8008", "null"]
_frontend_url = os.getenv("FRONTEND_URL")
if _frontend_url:
    _allowed_origins.append(_frontend_url)

app.add_middleware(
    CORSMiddleware,
    allow_origins=_allowed_origins,
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
    return {"ok": True}


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
