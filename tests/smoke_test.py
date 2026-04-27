import json
import sys
import urllib.request


BASE_URL = "http://127.0.0.1:8008"


def request(path, method="GET", payload=None, headers=None, timeout=60):
    body = None
    headers = headers or {}
    if payload is not None:
        body = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(BASE_URL + path, data=body, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=timeout) as response:
        return json.loads(response.read().decode("utf-8"))


def main():
    health = request("/api/health")
    assert health["ok"] is True

    research = request(
        "/api/research",
        method="POST",
        payload={"query": "AI agents for research", "depth": "Detailed", "output": "Mindmap"},
    )
    assert research["session_id"]
    assert research["nodes"]

    source_body = b"AI research assistants help collect evidence, synthesize findings, and answer follow-up questions."
    req = urllib.request.Request(
        BASE_URL + "/api/sources",
        data=source_body,
        headers={"Content-Type": "text/plain", "X-Filename": "sample.txt"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=60) as response:
        source = json.loads(response.read().decode("utf-8"))
    assert source["chunks_count"] == 1

    generated = request(
        f"/api/sessions/{source['session_id']}/generate",
        method="POST",
        payload={"topic": "AI research assistants", "depth": "Detailed", "output": "Mindmap"},
    )
    assert generated["sources"]

    answer = request(
        f"/api/sessions/{source['session_id']}/ask",
        method="POST",
        payload={"question": "What do AI research assistants help with?"},
    )
    assert answer["answer"]
    print("Smoke tests passed")


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"Smoke tests failed: {exc}", file=sys.stderr)
        raise
