"""JSON repair/parsing and provenance normalization."""
from app.groq_client import parse_json_or_fallback, normalize_payload


FALLBACK = {"summary": "fallback summary"}


def test_parses_clean_json():
    out = parse_json_or_fallback('{"summary": "hi"}', dict(FALLBACK))
    assert out["summary"] == "hi"


def test_parses_json_wrapped_in_prose():
    content = 'Sure! Here is the JSON:\n{"summary": "extracted"}\nHope that helps.'
    out = parse_json_or_fallback(content, dict(FALLBACK))
    assert out["summary"] == "extracted"


def test_repairs_truncated_json():
    content = '{"summary": "cut off", "nodes": [{"title": "a"'
    out = parse_json_or_fallback(content, dict(FALLBACK))
    assert out["summary"] == "cut off"


def test_fallback_on_garbage():
    out = parse_json_or_fallback("complete nonsense, no braces", dict(FALLBACK))
    # Garbage content is preserved as a degraded summary rather than dropped
    assert out["summary"] == "complete nonsense, no braces"


def test_empty_content_returns_fallback():
    out = parse_json_or_fallback("", dict(FALLBACK))
    assert out["summary"] == "fallback summary"


def test_missing_required_key_uses_fallback():
    out = parse_json_or_fallback('{"other": 1}', dict(FALLBACK), required_keys=["summary"])
    assert "summary" in out


def test_normalize_payload_fixes_invalid_provenance():
    data = {
        "provenance": "made_up_value",
        "nodes": [{"title": "n", "provenance": "wrong"}],
        "citations": [{"title": "c", "provenance": None}],
        "sources": [{"title": "s", "confidence": 0.9}],
    }
    out = normalize_payload(data)
    assert out["provenance"] == "ai_synthesized"
    assert out["nodes"][0]["provenance"] == "ai_synthesized"
    assert out["citations"][0]["provenance"] == "source_grounded"
    assert isinstance(out["sources"][0]["confidence"], str)
