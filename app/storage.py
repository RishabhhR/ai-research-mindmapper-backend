import json
import os
import time
import uuid
from pathlib import Path

import libsql_client  # hard dependency — pip install libsql-client


BASE_DIR = Path(__file__).resolve().parent.parent
UPLOAD_DIR = Path(os.getenv("UPLOAD_DIR", "/tmp/mindmapper_uploads"))
UPLOAD_DIR.mkdir(exist_ok=True)

TURSO_URL   = os.getenv("TURSO_URL", "")         # libsql://mindmapper-db-<user>.turso.io
TURSO_TOKEN = os.getenv("TURSO_AUTH_TOKEN", "")  # turso db tokens create mindmapper-db

# ── Startup guard ─────────────────────────────────────────────────────────────
if not TURSO_URL or not TURSO_TOKEN:
    raise RuntimeError(
        "\n\n❌  TURSO_URL and TURSO_AUTH_TOKEN must be set in .env\n"
        "   Run: turso db show mindmapper-db --url  (→ TURSO_URL)\n"
        "        turso db tokens create mindmapper-db (→ TURSO_AUTH_TOKEN)\n"
    )


def now_ts() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def new_id(prefix: str) -> str:
    return f"{prefix}_{uuid.uuid4().hex[:16]}"


# ── Connection helpers ─────────────────────────────────────────────────────

class _TursoConn:
    """Minimal sqlite3-compatible wrapper around libsql_client (HTTP).

    Supports:
        with _TursoConn() as conn:
            conn.execute(sql, params)
            conn.executescript(sql)
            for row in conn.execute(...)   → rows are plain dicts
            conn.execute(...).fetchone()   → first dict or None
            conn.execute(...).fetchall()   → list of dicts
    """

    def __init__(self):
        # Convert libsql:// → https:// for HTTP transport
        http_url = TURSO_URL.replace("libsql://", "https://", 1)
        self._client = libsql_client.create_client_sync(
            url=http_url, auth_token=TURSO_TOKEN
        )

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        self._client.close()
        return False

    def execute(self, sql: str, params=()) -> "_TursoCursor":
        result = self._client.execute(libsql_client.Statement(sql, list(params)))
        return _TursoCursor(result)

    def executescript(self, script: str) -> None:
        """Run each semicolon-separated statement independently."""
        statements = [s.strip() for s in script.split(";") if s.strip()]
        for stmt in statements:
            self._client.execute(libsql_client.Statement(stmt))


class _TursoCursor:
    """Wraps a libsql_client ResultSet to look like sqlite3.Cursor."""

    def __init__(self, result):
        self._rows = []
        if result and result.rows:
            cols = [c for c in result.columns]
            self._rows = [dict(zip(cols, row)) for row in result.rows]
        self._index = 0

    def __iter__(self):
        return iter(self._rows)

    def fetchone(self):
        return self._rows[0] if self._rows else None

    def fetchall(self):
        return self._rows


def connect() -> _TursoConn:
    """Open and return a Turso connection."""
    return _TursoConn()


def init_db() -> None:
    with connect() as conn:
        conn.executescript(
            """
            CREATE TABLE IF NOT EXISTS jobs (
                id TEXT PRIMARY KEY,
                session_id TEXT NOT NULL,
                user_id TEXT NOT NULL DEFAULT '',
                type TEXT NOT NULL,
                status TEXT NOT NULL DEFAULT 'pending',
                input_json TEXT,
                result_json TEXT,
                error TEXT,
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL
            );

            CREATE TABLE IF NOT EXISTS sessions (
                id TEXT PRIMARY KEY,
                topic TEXT NOT NULL,
                depth TEXT NOT NULL,
                output TEXT NOT NULL,
                user_id TEXT NOT NULL DEFAULT '',
                summary TEXT,
                mindmap_json TEXT,
                insights_json TEXT,
                tradeoffs_json TEXT,
                citations_json TEXT,
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL
            );

            CREATE TABLE IF NOT EXISTS sources (
                id TEXT PRIMARY KEY,
                session_id TEXT NOT NULL,
                source_type TEXT NOT NULL,
                title TEXT NOT NULL,
                url TEXT,
                file_path TEXT,
                created_at TEXT NOT NULL,
                FOREIGN KEY(session_id) REFERENCES sessions(id)
            );

            CREATE TABLE IF NOT EXISTS source_chunks (
                id TEXT PRIMARY KEY,
                source_id TEXT NOT NULL,
                session_id TEXT NOT NULL,
                source_type TEXT NOT NULL,
                title TEXT NOT NULL,
                url TEXT,
                page INTEGER,
                chunk_index INTEGER NOT NULL,
                text TEXT NOT NULL,
                created_at TEXT NOT NULL,
                FOREIGN KEY(source_id) REFERENCES sources(id),
                FOREIGN KEY(session_id) REFERENCES sessions(id)
            );

            CREATE TABLE IF NOT EXISTS qa_history (
                id TEXT PRIMARY KEY,
                session_id TEXT NOT NULL,
                question TEXT NOT NULL,
                answer TEXT NOT NULL,
                citations_json TEXT NOT NULL,
                provenance TEXT NOT NULL,
                created_at TEXT NOT NULL,
                FOREIGN KEY(session_id) REFERENCES sessions(id)
            );
            """
        )
        # Migrate existing tables that pre-date the user_id column
        try:
            conn.execute("ALTER TABLE sessions ADD COLUMN user_id TEXT NOT NULL DEFAULT ''")
        except Exception:
            pass  # column already exists


def create_session(topic: str, depth: str = "Detailed", output: str = "Mindmap", user_id: str = "") -> str:
    session_id = new_id("ses")
    created = now_ts()
    with connect() as conn:
        conn.execute(
            """
            INSERT INTO sessions (id, topic, depth, output, user_id, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (session_id, topic, depth, output, user_id, created, created),
        )
    return session_id


def save_generation(session_id: str, summary: str, nodes, insights, tradeoffs, citations) -> None:
    with connect() as conn:
        conn.execute(
            """
            UPDATE sessions
            SET summary = ?, mindmap_json = ?, insights_json = ?, tradeoffs_json = ?,
                citations_json = ?, updated_at = ?
            WHERE id = ?
            """,
            (
                summary,
                json.dumps(nodes),
                json.dumps(insights),
                json.dumps(tradeoffs),
                json.dumps(citations),
                now_ts(),
                session_id,
            ),
        )


def add_source(session_id: str, source_type: str, title: str, url=None, file_path=None) -> str:
    source_id = new_id("src")
    with connect() as conn:
        conn.execute(
            """
            INSERT INTO sources (id, session_id, source_type, title, url, file_path, created_at)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (source_id, session_id, source_type, title, url, file_path, now_ts()),
        )
    return source_id


def add_chunks(session_id: str, source_id: str, source_type: str, title: str, chunks, url=None) -> None:
    created = now_ts()
    with connect() as conn:
        for index, chunk in enumerate(chunks):
            conn.execute(
                """
                INSERT INTO source_chunks
                (id, source_id, session_id, source_type, title, url, page, chunk_index, text, created_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    new_id("chk"),
                    source_id,
                    session_id,
                    source_type,
                    title,
                    url,
                    chunk.get("page"),
                    index,
                    chunk["text"],
                    created,
                ),
            )


def get_session(session_id: str, user_id: str = None):
    with connect() as conn:
        if user_id is not None:
            row = conn.execute(
                "SELECT * FROM sessions WHERE id = ? AND user_id = ?",
                (session_id, user_id),
            ).fetchone()
        else:
            row = conn.execute("SELECT * FROM sessions WHERE id = ?", (session_id,)).fetchone()
        return dict(row) if row else None


def list_sessions(user_id: str = ""):
    with connect() as conn:
        return [
            dict(row)
            for row in conn.execute(
                """
                SELECT id, topic, depth, output, summary, created_at, updated_at
                FROM sessions WHERE user_id = ? ORDER BY updated_at DESC LIMIT 50
                """,
                (user_id,),
            ).fetchall()
        ]


def get_chunks(session_id: str, limit: int = 80):
    with connect() as conn:
        return [
            dict(row)
            for row in conn.execute(
                """
                SELECT * FROM source_chunks
                WHERE session_id = ?
                ORDER BY source_id, chunk_index
                LIMIT ?
                """,
                (session_id, limit),
            ).fetchall()
        ]


def get_sources(session_id: str):
    with connect() as conn:
        return [
            dict(row)
            for row in conn.execute(
                "SELECT * FROM sources WHERE session_id = ? ORDER BY created_at DESC",
                (session_id,),
            ).fetchall()
        ]


def add_qa(session_id: str, question: str, answer: str, citations, provenance: str) -> None:
    with connect() as conn:
        conn.execute(
            """
            INSERT INTO qa_history (id, session_id, question, answer, citations_json, provenance, created_at)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (new_id("qa"), session_id, question, answer, json.dumps(citations), provenance, now_ts()),
        )


def get_qa(session_id: str):
    with connect() as conn:
        return [
            dict(row)
            for row in conn.execute(
                "SELECT * FROM qa_history WHERE session_id = ? ORDER BY created_at",
                (session_id,),
            ).fetchall()
        ]


# ── Background jobs ────────────────────────────────────────────────────────────

def create_job(session_id: str, user_id: str, job_type: str, input_data: dict) -> str:
    job_id = new_id("job")
    created = now_ts()
    with connect() as conn:
        conn.execute(
            """
            INSERT INTO jobs (id, session_id, user_id, type, status, input_json, created_at, updated_at)
            VALUES (?, ?, ?, ?, 'pending', ?, ?, ?)
            """,
            (job_id, session_id, user_id, job_type, json.dumps(input_data), created, created),
        )
    return job_id


def get_job(job_id: str, user_id: str = None) -> dict | None:
    with connect() as conn:
        if user_id:
            row = conn.execute(
                "SELECT * FROM jobs WHERE id = ? AND user_id = ?", (job_id, user_id)
            ).fetchone()
        else:
            row = conn.execute("SELECT * FROM jobs WHERE id = ?", (job_id,)).fetchone()
    if not row:
        return None
    job = dict(row)
    if job.get("input_json"):
        job["input"] = json.loads(job["input_json"])
    if job.get("result_json"):
        job["result"] = json.loads(job["result_json"])
    return job


def update_job(job_id: str, status: str, result: dict = None, error: str = None) -> None:
    with connect() as conn:
        conn.execute(
            """
            UPDATE jobs SET status = ?, result_json = ?, error = ?, updated_at = ?
            WHERE id = ?
            """,
            (status, json.dumps(result) if result is not None else None, error, now_ts(), job_id),
        )
