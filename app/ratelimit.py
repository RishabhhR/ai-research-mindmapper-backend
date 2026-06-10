"""Per-user rate limiting backed by Turso (works across serverless instances).

Two fixed windows per user: per-minute (burst control) and per-day (quota
control). One batched DB roundtrip per checked request.

Tunables (env):
    RATE_LIMIT_LLM_PER_MIN   default 6   — research / generate / ask
    RATE_LIMIT_LLM_PER_DAY   default 120
    RATE_LIMIT_INGEST_PER_MIN default 12 — source ingestion
    RATE_LIMIT_INGEST_PER_DAY default 200
    RATE_LIMIT_DISABLED=1    turns the limiter off (load tests, local dev)
"""

import logging
import os
import random
import time

from fastapi import HTTPException

from . import storage

logger = logging.getLogger("mindmapper.ratelimit")


def _limits(kind: str):
    if kind == "ingest":
        return (
            int(os.getenv("RATE_LIMIT_INGEST_PER_MIN", "12")),
            int(os.getenv("RATE_LIMIT_INGEST_PER_DAY", "200")),
        )
    return (
        int(os.getenv("RATE_LIMIT_LLM_PER_MIN", "6")),
        int(os.getenv("RATE_LIMIT_LLM_PER_DAY", "120")),
    )


def enforce(user_id: str, kind: str = "llm") -> None:
    """Raise HTTP 429 if the user exceeded the window for this request kind.

    Fails open: if the rate-limit storage is unreachable we let the request
    through rather than taking the product down.
    """
    if os.getenv("RATE_LIMIT_DISABLED") == "1":
        return

    now = time.gmtime()
    minute_bucket = time.strftime(f"%Y-%m-%dT%H:%M|{kind}", now)
    day_bucket = time.strftime(f"%Y-%m-%d|{kind}", now)
    per_min, per_day = _limits(kind)

    try:
        minute_count, day_count = storage.bump_rate_counters(
            user_id, minute_bucket, day_bucket
        )
    except Exception as exc:  # noqa: BLE001 — fail open by design
        logger.warning("rate limiter unavailable, allowing request: %s", exc)
        return

    # Opportunistic cleanup of stale buckets (~1% of requests).
    if random.random() < 0.01:
        try:
            storage.cleanup_rate_buckets(time.strftime("%Y-%m-%d", now))
        except Exception:  # noqa: BLE001
            pass

    if minute_count > per_min:
        raise HTTPException(
            status_code=429,
            detail="You're going a little fast — try again in a minute.",
            headers={"Retry-After": "60"},
        )
    if day_count > per_day:
        raise HTTPException(
            status_code=429,
            detail="Daily research limit reached. Resets at midnight UTC.",
            headers={"Retry-After": "3600"},
        )
