"""
Unit tests for the LLM fallback chain in groq_client.py.

Each test mocks one more layer offline and asserts the correct fallback path
is taken, without making any real network calls.

Run:
    cd ai-research-mindmapper-backend
    python -m pytest tests/test_fallback_chain.py -v
"""
import sys
import os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import pytest
from unittest.mock import patch, call as mcall
from app.groq_client import call_llm_with_fallback


# ── fixtures ─────────────────────────────────────────────────────────────────

MESSAGES = [
    {"role": "system", "content": "Return only valid JSON."},
    {"role": "user", "content": "Topic: AI agents in research workflows"},
]

_VALID_JSON = (
    '{"summary":"ok","nodes":[],"insights":[],'
    '"sources":[],"tradeoffs":[],"citations":[]}'
)
ONLINE   = {"offline": False, "content": _VALID_JSON}
OFFLINE  = {"offline": True,  "content": "", "warning": "Rate limited (429)"}

DDG_RESULTS = [
    {"title": "AI Agents Overview",  "body": "AI agents can automate research tasks.", "url": "https://example.com/1"},
    {"title": "Research Automation", "body": "Automation reduces manual effort.",       "url": "https://example.com/2"},
]


# ── stage 1: Groq succeeds ────────────────────────────────────────────────────

def test_groq_succeeds_no_fallback():
    with patch("app.groq_client.call_groq", return_value=ONLINE) as m:
        result = call_llm_with_fallback(MESSAGES)
    assert result["offline"] is False
    assert "fallback_warning" not in result
    m.assert_called_once()


# ── stage 2: Groq offline → Gemini ───────────────────────────────────────────

def test_falls_to_gemini_when_groq_offline():
    with patch("app.groq_client.call_groq", return_value=OFFLINE):
        with patch("app.groq_client.call_gemini", return_value=ONLINE) as m_gem:
            result = call_llm_with_fallback(MESSAGES)
    assert result["offline"] is False
    assert "Gemini" in result.get("fallback_warning", "")
    m_gem.assert_called_once()


# ── stage 3: Groq + Gemini offline → OpenRouter ──────────────────────────────

def test_falls_to_openrouter_when_groq_and_gemini_offline():
    with patch("app.groq_client.call_groq",    return_value=OFFLINE):
        with patch("app.groq_client.call_gemini",  return_value=OFFLINE):
            with patch("app.groq_client.call_openrouter", return_value=ONLINE) as m_or:
                result = call_llm_with_fallback(MESSAGES)
    assert result["offline"] is False
    assert "OpenRouter" in result.get("fallback_warning", "")
    m_or.assert_called_once_with(MESSAGES)


# ── stage 4a: all LLMs offline → DDG RAG → Gemini succeeds ──────────────────

def test_ddg_rag_gemini_when_all_primary_llms_fail():
    # call_gemini is called twice: first attempt (offline), then RAG retry (online)
    with patch("app.groq_client.call_groq",      return_value=OFFLINE):
        with patch("app.groq_client.call_gemini",  side_effect=[OFFLINE, ONLINE]) as m_gem:
            with patch("app.groq_client.call_openrouter", return_value=OFFLINE):
                with patch("app.groq_client.call_duckduckgo", return_value=DDG_RESULTS) as m_ddg:
                    result = call_llm_with_fallback(MESSAGES)

    assert result["offline"] is False
    fw = result.get("fallback_warning", "")
    assert "DuckDuckGo" in fw
    assert "Gemini"     in fw
    m_ddg.assert_called_once()
    assert m_gem.call_count == 2


# ── stage 4b: all LLMs offline + DDG+Gemini fails → DDG+OpenRouter ───────────

def test_ddg_rag_openrouter_when_gemini_also_fails_in_rag():
    # call_openrouter called twice: first attempt (offline), then RAG retry (online)
    with patch("app.groq_client.call_groq",      return_value=OFFLINE):
        with patch("app.groq_client.call_gemini",  return_value=OFFLINE):
            with patch("app.groq_client.call_openrouter", side_effect=[OFFLINE, ONLINE]) as m_or:
                with patch("app.groq_client.call_duckduckgo", return_value=DDG_RESULTS) as m_ddg:
                    result = call_llm_with_fallback(MESSAGES)

    assert result["offline"] is False
    fw = result.get("fallback_warning", "")
    assert "DuckDuckGo"  in fw
    assert "OpenRouter"  in fw
    m_ddg.assert_called_once()
    assert m_or.call_count == 2


# ── stage 5: DDG returns empty → extractive fallback ─────────────────────────

def test_extractive_fallback_when_ddg_empty():
    with patch("app.groq_client.call_groq",      return_value=OFFLINE):
        with patch("app.groq_client.call_gemini",  return_value=OFFLINE):
            with patch("app.groq_client.call_openrouter", return_value=OFFLINE):
                with patch("app.groq_client.call_duckduckgo", return_value=[]):
                    result = call_llm_with_fallback(MESSAGES)
    assert result["offline"] is True


# ── stage 5 alt: everything fails including DDG RAG ──────────────────────────

def test_extractive_fallback_when_ddg_rag_also_fails():
    with patch("app.groq_client.call_groq",      return_value=OFFLINE):
        with patch("app.groq_client.call_gemini",  return_value=OFFLINE):
            with patch("app.groq_client.call_openrouter", return_value=OFFLINE):
                with patch("app.groq_client.call_duckduckgo", return_value=DDG_RESULTS):
                    result = call_llm_with_fallback(MESSAGES)
    assert result["offline"] is True
    assert result.get("warning") is not None


# ── DDG retrieval returns structured results ──────────────────────────────────

def test_ddg_returns_list_of_dicts():
    from app.groq_client import call_duckduckgo
    from unittest.mock import MagicMock

    raw = [
        {"title": "T1", "body": "B1", "href": "https://example.com/1"},
        {"title": "T2", "body": "B2", "href": "https://example.com/2"},
    ]
    mock_ddgs_instance = MagicMock()
    mock_ddgs_instance.text.return_value = iter(raw)
    mock_ddgs_cls = MagicMock(return_value=mock_ddgs_instance)

    # DDGS is imported inside call_duckduckgo, so patch at the source package
    with patch("duckduckgo_search.DDGS", mock_ddgs_cls):
        with patch("app.groq_client.DDGS", mock_ddgs_cls, create=True):
            results = call_duckduckgo("test query", max_results=2)

    assert len(results) == 2
    for item in results:
        assert "title" in item
        assert "body"  in item
        assert "url"   in item


# ── openrouter returns offline when key missing ───────────────────────────────

def test_openrouter_offline_when_no_key():
    from app.groq_client import call_openrouter
    with patch.dict(os.environ, {}, clear=False):
        env = os.environ.copy()
        env.pop("OPENROUTER_API_KEY", None)
        with patch.dict(os.environ, env, clear=True):
            result = call_openrouter(MESSAGES)
    assert result["offline"] is True
    assert "OPENROUTER_API_KEY" in result.get("warning", "")
