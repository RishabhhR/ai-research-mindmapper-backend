"""
Unit tests for the LLM fallback chain in groq_client.py.

Chain: Groq → Gemini → OpenRouter → extractive fallback.
(The old DuckDuckGo RAG stage was removed: it imported a package that was
never in requirements.txt and crashed with ImportError when reached.)

Run:
    python -m pytest tests/test_fallback_chain.py -v
"""
import os

import pytest
from unittest.mock import patch

from app.groq_client import call_llm_with_fallback


MESSAGES = [
    {"role": "system", "content": "Return only valid JSON."},
    {"role": "user", "content": "Topic: AI agents in research workflows"},
]

_VALID_JSON = (
    '{"summary":"ok","nodes":[],"insights":[],'
    '"sources":[],"tradeoffs":[],"citations":[]}'
)
ONLINE = {"offline": False, "content": _VALID_JSON}
OFFLINE = {"offline": True, "content": "", "warning": "Rate limited (429)"}


def test_groq_succeeds_no_fallback():
    with patch("app.groq_client.call_groq", return_value=dict(ONLINE)) as m:
        result = call_llm_with_fallback(MESSAGES)
    assert result["offline"] is False
    assert "fallback_warning" not in result
    m.assert_called_once()


def test_falls_to_gemini_when_groq_offline():
    with patch("app.groq_client.call_groq", return_value=dict(OFFLINE)):
        with patch("app.groq_client.call_gemini", return_value=dict(ONLINE)) as m_gem:
            result = call_llm_with_fallback(MESSAGES)
    assert result["offline"] is False
    assert "Gemini" in result.get("fallback_warning", "")
    m_gem.assert_called_once()


def test_falls_to_openrouter_when_groq_and_gemini_offline():
    with patch("app.groq_client.call_groq", return_value=dict(OFFLINE)):
        with patch("app.groq_client.call_gemini", return_value=dict(OFFLINE)):
            with patch("app.groq_client.call_openrouter", return_value=dict(ONLINE)) as m_or:
                result = call_llm_with_fallback(MESSAGES)
    assert result["offline"] is False
    assert "OpenRouter" in result.get("fallback_warning", "")
    m_or.assert_called_once_with(MESSAGES, timeout=8)


def test_max_stages_2_skips_openrouter():
    with patch("app.groq_client.call_groq", return_value=dict(OFFLINE)):
        with patch("app.groq_client.call_gemini", return_value=dict(OFFLINE)) as m_gem:
            with patch("app.groq_client.call_openrouter") as m_or:
                result = call_llm_with_fallback(MESSAGES, max_llm_stages=2)
    assert result["offline"] is True
    m_gem.assert_called_once()
    m_or.assert_not_called()


def test_extractive_fallback_when_everything_offline():
    with patch("app.groq_client.call_groq", return_value=dict(OFFLINE)):
        with patch("app.groq_client.call_gemini", return_value=dict(OFFLINE)):
            with patch("app.groq_client.call_openrouter", return_value=dict(OFFLINE)):
                result = call_llm_with_fallback(MESSAGES)
    assert result["offline"] is True
    assert result.get("warning") is not None


def test_no_ddg_stage_remains():
    """The crashing DDG path must be fully gone."""
    import app.groq_client as gc
    assert not hasattr(gc, "call_duckduckgo")


def test_openrouter_offline_when_no_key():
    from app.groq_client import call_openrouter
    env = os.environ.copy()
    env.pop("OPENROUTER_API_KEY", None)
    with patch.dict(os.environ, env, clear=True):
        result = call_openrouter(MESSAGES)
    assert result["offline"] is True
    assert "OPENROUTER_API_KEY" in result.get("warning", "")


def test_generate_research_returns_fallback_when_offline():
    from app.groq_client import generate_research
    with patch("app.groq_client.call_groq", return_value=dict(OFFLINE)):
        with patch("app.groq_client.call_gemini", return_value=dict(OFFLINE)):
            with patch("app.groq_client.call_openrouter", return_value=dict(OFFLINE)):
                data = generate_research("test topic", "Basic", "Mindmap")
    assert data["summary"]
    assert data["nodes"]
    assert any("fallback" in w.lower() or "unavailable" in w.lower() or "429" in w for w in data["warnings"])


def test_generate_research_parses_valid_response():
    from app.groq_client import generate_research
    payload = dict(ONLINE)
    payload["executed_tools"] = []
    with patch("app.groq_client.call_groq", return_value=payload):
        data = generate_research("test topic", "Basic", "Mindmap")
    assert data["summary"] == "ok"
    assert data["warnings"] == []
