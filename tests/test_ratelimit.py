"""Rate limiter behavior: bursts blocked, daily quota blocked, fail-open."""
import os
from unittest.mock import patch

import pytest
from fastapi import HTTPException

from app import ratelimit


def _enforce(minute_count, day_count, kind="llm"):
    with patch("app.ratelimit.storage.bump_rate_counters", return_value=(minute_count, day_count)):
        with patch("app.ratelimit.storage.cleanup_rate_buckets"):
            ratelimit.enforce("user_test", kind)


def test_allows_under_limit():
    _enforce(1, 1)  # no exception


def test_blocks_minute_burst():
    with pytest.raises(HTTPException) as exc:
        _enforce(7, 7)  # default per-min is 6
    assert exc.value.status_code == 429
    assert exc.value.headers.get("Retry-After") == "60"


def test_blocks_daily_quota():
    with pytest.raises(HTTPException) as exc:
        _enforce(1, 121)  # default per-day is 120
    assert exc.value.status_code == 429
    assert "Daily" in exc.value.detail


def test_ingest_kind_uses_higher_minute_limit():
    _enforce(10, 10, kind="ingest")  # 10 < 12 → allowed
    with pytest.raises(HTTPException):
        _enforce(13, 13, kind="ingest")


def test_fails_open_when_db_down():
    with patch("app.ratelimit.storage.bump_rate_counters", side_effect=RuntimeError("turso down")):
        ratelimit.enforce("user_test", "llm")  # must NOT raise


def test_disabled_flag_skips_everything():
    with patch.dict(os.environ, {"RATE_LIMIT_DISABLED": "1"}):
        with patch("app.ratelimit.storage.bump_rate_counters") as m:
            ratelimit.enforce("user_test", "llm")
    m.assert_not_called()
