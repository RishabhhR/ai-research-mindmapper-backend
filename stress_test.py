"""
Live stress test for the fallback pipeline.

Sends CONCURRENCY simultaneous POST /api/research requests.
Groq allows ~30 RPM on free tier; Gemini ~10 RPM.
A burst of 40 concurrent requests exhausts both within seconds,
forcing the system into OpenRouter → DDG+RAG paths.

Usage:
  1. Open the app in your browser and sign in.
  2. Open DevTools → Network → click any /api/ request → copy the
     Authorization header value (starts with "Bearer eyJ...").
  3. Run:
       CLERK_TOKEN="Bearer eyJ..." python stress_test.py

Optional env vars:
  BASE_URL     default: https://mindmapper-api-mu.vercel.app
  CONCURRENCY  default: 40
  TOPIC        default: varies per request
"""

import os
import sys
import time
import json
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed
from urllib import request as urllib_request
from urllib.error import HTTPError

BASE_URL    = os.getenv("BASE_URL", "https://mindmapper-api-mu.vercel.app")
CONCURRENCY = int(os.getenv("CONCURRENCY", "40"))
TOKEN       = os.getenv("CLERK_TOKEN", "")

TOPICS = [
    "How do AI agents improve product research workflows?",
    "Quantum computing applications in drug discovery",
    "Climate change impact on global food supply chains",
    "The economics of large language model inference",
    "Edge computing vs cloud computing tradeoffs",
    "CRISPR gene editing ethical implications",
    "Autonomous vehicles sensor fusion techniques",
    "Federated learning for privacy-preserving AI",
    "Microplastics impact on marine ecosystems",
    "Renewable energy grid stability challenges",
]


def do_request(idx: int) -> dict:
    topic = TOPICS[idx % len(TOPICS)]
    payload = json.dumps({
        "query": topic,
        "depth": "Basic",
        "output": "Mindmap",
        "source": "Academic + web",
    }).encode()

    headers = {
        "Content-Type": "application/json",
        "Authorization": TOKEN,
    }

    req = urllib_request.Request(
        BASE_URL + "/api/research",
        data=payload,
        headers=headers,
        method="POST",
    )

    t0 = time.time()
    try:
        with urllib_request.urlopen(req, timeout=30) as resp:
            elapsed = time.time() - t0
            data = json.loads(resp.read())
            warnings      = data.get("warnings", [])
            fallback_warn = data.get("fallback_warning", "")

            if fallback_warn:
                path = f"FALLBACK  → {fallback_warn}"
            elif warnings:
                path = f"WARNINGS  → {'; '.join(warnings)}"
            else:
                path = "PRIMARY   (Groq)"

            return {
                "idx": idx,
                "status": resp.status,
                "elapsed": round(elapsed, 2),
                "path": path,
                "topic": topic[:55],
                "ok": True,
            }
    except HTTPError as exc:
        elapsed = time.time() - t0
        body = exc.read().decode()[:200]
        return {
            "idx": idx,
            "status": exc.code,
            "elapsed": round(elapsed, 2),
            "path": f"HTTP ERROR {exc.code}: {body}",
            "topic": topic[:55],
            "ok": False,
        }
    except Exception as exc:
        elapsed = time.time() - t0
        return {
            "idx": idx,
            "status": 0,
            "elapsed": round(elapsed, 2),
            "path": f"EXCEPTION: {exc}",
            "topic": topic[:55],
            "ok": False,
        }


def print_result(r: dict):
    icon  = "✓" if r["ok"] else "✗"
    print(f"  [{r['idx']:02d}] {icon} {r['status']} {r['elapsed']:5.1f}s  {r['path']}")


def main():
    if not TOKEN:
        print("ERROR: Set CLERK_TOKEN env var to a valid Clerk JWT.")
        print("  CLERK_TOKEN='Bearer eyJ...' python stress_test.py")
        sys.exit(1)

    print(f"\n{'='*70}")
    print(f"  Stress test — {CONCURRENCY} concurrent requests → {BASE_URL}")
    print(f"  Goal: exhaust Groq (30 RPM) + Gemini (10 RPM) → DDG+RAG path")
    print(f"{'='*70}\n")

    t_start = time.time()
    results = []
    lock = threading.Lock()
    completed = 0

    with ThreadPoolExecutor(max_workers=CONCURRENCY) as pool:
        futures = {pool.submit(do_request, i): i for i in range(CONCURRENCY)}
        for future in as_completed(futures):
            r = future.result()
            with lock:
                completed += 1
                results.append(r)
                print_result(r)
                print(f"  Progress: {completed}/{CONCURRENCY}", end="\r", flush=True)

    total_elapsed = time.time() - t_start

    # ── summary ──────────────────────────────────────────────────────────────
    ok      = [r for r in results if r["ok"]]
    failed  = [r for r in results if not r["ok"]]
    primary = [r for r in ok if "PRIMARY"  in r["path"]]
    gemini  = [r for r in ok if "Gemini"   in r["path"] and "DuckDuckGo" not in r["path"]]
    openrtr = [r for r in ok if "OpenRout" in r["path"] and "DuckDuckGo" not in r["path"]]
    ddg_gem = [r for r in ok if "DuckDuck" in r["path"] and "Gemini"     in r["path"]]
    ddg_or  = [r for r in ok if "DuckDuck" in r["path"] and "OpenRout"   in r["path"]]

    avg_ok = (sum(r["elapsed"] for r in ok) / len(ok)) if ok else 0

    print(f"\n\n{'='*70}")
    print(f"  SUMMARY  ({CONCURRENCY} requests in {total_elapsed:.1f}s)")
    print(f"{'='*70}")
    print(f"  Successful          : {len(ok)}")
    print(f"  Failed              : {len(failed)}")
    print(f"  Avg latency (ok)    : {avg_ok:.1f}s")
    print()
    print(f"  Path breakdown:")
    print(f"    Groq (primary)    : {len(primary)}")
    print(f"    Gemini fallback   : {len(gemini)}")
    print(f"    OpenRouter        : {len(openrtr)}")
    print(f"    DDG + Gemini RAG  : {len(ddg_gem)}")
    print(f"    DDG + OpenRouter  : {len(ddg_or)}")
    print(f"{'='*70}\n")

    ddg_triggered = len(ddg_gem) + len(ddg_or)
    if ddg_triggered:
        print(f"  ✓ DDG RAG path triggered {ddg_triggered} time(s) — fallback pipeline working.")
    elif openrtr:
        print(f"  ✓ OpenRouter triggered {len(openrtr)} time(s) — Groq+Gemini were rate-limited.")
    else:
        print(f"  ℹ  Groq handled everything — try increasing CONCURRENCY or run again quickly.")

    print()


if __name__ == "__main__":
    main()
