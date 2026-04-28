"""
Stress test for the LLM fallback pipeline.

Fallback chain under test:
  Groq (primary) → Gemini → OpenRouter → DDG+Gemini RAG → DDG+OpenRouter RAG → extractive

Strategy: rapid concurrent waves to exhaust LLM rate limits in order.

  0–30s  (~30 reqs)  → exhausts Groq 30 RPM    → Gemini kicks in
  30–40s (~10 reqs)  → exhausts Gemini 10 RPM  → OpenRouter kicks in
  40–90s (remaining) → OpenRouter / DDG+RAG paths

Auth (pick one — no Clerk JWT needed):
  # Auto-reads STRESS_TEST_KEY from .env (bypass, no JWT needed):
  python3 stress_test.py

  # Override with a live Clerk JWT:
  CLERK_TOKEN="Bearer <jwt>" python3 stress_test.py

  # Simulate locally without hitting the real API:
  SIMULATE=1 python3 stress_test.py

Optional env vars:
  BASE_URL        default: https://mindmapper-api-mu.vercel.app
  STRESS_TEST_KEY default: read from .env (st_mindmapper_internal_bypass_2024)
  BATCH_SIZE      default: 8
  WAVE_INTERVAL   default: 4   (seconds between waves)
  DURATION        default: 90  (total test window, seconds)
  SIMULATE        default: 0   (1 = mock responses, no real HTTP)
"""

import os
import sys
import time
import json
import random
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed
from urllib import request as urllib_request
from urllib.error import HTTPError

# ── Config ────────────────────────────────────────────────────────────────────
BASE_URL      = os.getenv("BASE_URL",      "https://mindmapper-api-mu.vercel.app")
BATCH_SIZE    = int(os.getenv("BATCH_SIZE",    "8"))
WAVE_INTERVAL = int(os.getenv("WAVE_INTERVAL", "4"))
DURATION      = int(os.getenv("DURATION",      "90"))
SIMULATE      = os.getenv("SIMULATE", "0") == "1"

# Auth resolution: CLERK_TOKEN override > STRESS_TEST_KEY bypass > .env file
def _resolve_token() -> str:
    clerk_token = os.getenv("CLERK_TOKEN", "")
    if clerk_token:
        return clerk_token
    bypass = os.getenv("STRESS_TEST_KEY", "")
    if not bypass:
        # Try reading directly from .env file in case dotenv isn't loaded
        env_path = os.path.join(os.path.dirname(__file__), ".env")
        try:
            with open(env_path) as f:
                for line in f:
                    if line.startswith("STRESS_TEST_KEY="):
                        bypass = line.split("=", 1)[1].strip().strip('"').strip("'")
                        break
        except FileNotFoundError:
            pass
    return f"Bearer {bypass}" if bypass else ""

TOKEN = _resolve_token()

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

# ── Path classifier ───────────────────────────────────────────────────────────
def classify_path(data: dict, warnings: list) -> str:
    fw = data.get("fallback_warning", "")
    all_warnings = warnings + ([fw] if fw else [])
    combined = " ".join(all_warnings).lower()

    if "duckduckgo" in combined and "openrouter" in combined:
        return "DDG+OpenRouter RAG"
    if "duckduckgo" in combined and "gemini" in combined:
        return "DDG+Gemini RAG"
    if "duckduckgo" in combined:
        return "DDG RAG"
    if "openrouter" in combined:
        return "OpenRouter"
    if "gemini" in combined:
        return "Gemini"
    if all_warnings:
        return f"WARN: {all_warnings[0][:80]}"
    return "Groq (primary)"

# ── Simulation mode ───────────────────────────────────────────────────────────
_sim_counter = [0]
_sim_lock = threading.Lock()

_SIM_PATHS = [
    "Groq (primary)",       # first ~30 requests
    "Groq (primary)",
    "Groq (primary)",
    "Gemini",               # Groq exhausted
    "Gemini",
    "OpenRouter",           # Gemini exhausted
    "DDG+Gemini RAG",       # all LLMs down
    "DDG+OpenRouter RAG",
]

def simulate_request(idx: int) -> dict:
    time.sleep(random.uniform(0.3, 1.2))
    with _sim_lock:
        n = _sim_counter[0]
        _sim_counter[0] += 1
    path = _SIM_PATHS[min(n // 4, len(_SIM_PATHS) - 1)]
    ok = path != "FAIL"
    return {
        "idx": idx,
        "status": 200 if ok else 503,
        "elapsed": round(random.uniform(0.5, 3.5), 2),
        "path": path,
        "ok": ok,
    }

# ── Live HTTP request ─────────────────────────────────────────────────────────
def live_request(idx: int) -> dict:
    topic = TOPICS[idx % len(TOPICS)]
    payload = json.dumps({
        "query": topic,
        "depth": "Basic",
        "output": "Mindmap",
        "source": "Academic + web",
    }).encode()

    req = urllib_request.Request(
        BASE_URL + "/api/research",
        data=payload,
        headers={
            "Content-Type": "application/json",
            "Authorization": TOKEN,
        },
        method="POST",
    )

    t0 = time.time()
    try:
        with urllib_request.urlopen(req, timeout=45) as resp:
            elapsed = time.time() - t0
            data = json.loads(resp.read())
            path = classify_path(data, data.get("warnings", []))
            return {
                "idx": idx, "status": resp.status,
                "elapsed": round(elapsed, 2), "path": path, "ok": True,
            }
    except HTTPError as exc:
        elapsed = time.time() - t0
        try:
            body = exc.read().decode()[:160]
        except Exception:
            body = "(no body)"
        return {
            "idx": idx, "status": exc.code, "elapsed": round(elapsed, 2),
            "path": f"HTTP {exc.code}: {body}", "ok": False,
        }
    except Exception as exc:
        elapsed = time.time() - t0
        return {
            "idx": idx, "status": 0, "elapsed": round(elapsed, 2),
            "path": f"ERR: {exc}", "ok": False,
        }

# ── Main ──────────────────────────────────────────────────────────────────────
def main():
    if not SIMULATE and not TOKEN:
        print("ERROR: No auth token available.")
        print("  Add STRESS_TEST_KEY to .env  (must also be set in Vercel env vars)")
        print("  Or: CLERK_TOKEN='Bearer ...' python3 stress_test.py")
        print("  Or: SIMULATE=1 python3 stress_test.py")
        sys.exit(1)

    do_request = simulate_request if SIMULATE else live_request

    waves_planned = DURATION // WAVE_INTERVAL
    total_planned = waves_planned * BATCH_SIZE

    mode_label = "SIMULATION" if SIMULATE else f"LIVE → {BASE_URL}"
    print(f"\n{'='*72}")
    print(f"  Rolling stress test [{mode_label}]")
    print(f"  {waves_planned} waves × {BATCH_SIZE} concurrent ≈ {total_planned} reqs over {DURATION}s")
    print(f"  Wave interval: {WAVE_INTERVAL}s  |  target RPM: ~{total_planned * 60 // DURATION}")
    print(f"  Fallback chain: Groq → Gemini → OpenRouter → DDG+Gemini → DDG+OpenRouter")
    print(f"{'='*72}\n")

    all_results = []
    lock = threading.Lock()
    t_start = time.time()

    with ThreadPoolExecutor(max_workers=BATCH_SIZE) as pool:
        wave = 0
        while time.time() - t_start < DURATION:
            wave += 1
            wave_start = time.time()
            base_idx = (wave - 1) * BATCH_SIZE

            futures = {pool.submit(do_request, base_idx + i): i for i in range(BATCH_SIZE)}
            for future in as_completed(futures):
                r = future.result()
                with lock:
                    all_results.append(r)
                    icon = "✓" if r["ok"] else "✗"
                    print(f"  W{wave:02d} [{r['idx']:03d}] {icon} {r['status']} {r['elapsed']:5.1f}s  {r['path']}")

            wave_elapsed = time.time() - wave_start
            sleep_for = max(0, WAVE_INTERVAL - wave_elapsed)
            if sleep_for > 0 and (time.time() - t_start + sleep_for) < DURATION:
                time.sleep(sleep_for)

    total_elapsed = time.time() - t_start

    # ── Summary ───────────────────────────────────────────────────────────────
    ok      = [r for r in all_results if r["ok"]]
    failed  = [r for r in all_results if not r["ok"]]

    path_counts = {}
    for r in ok:
        path_counts[r["path"]] = path_counts.get(r["path"], 0) + 1

    avg_lat = (sum(r["elapsed"] for r in ok) / len(ok)) if ok else 0
    actual_rpm = round(len(all_results) / total_elapsed * 60) if total_elapsed > 0 else 0

    print(f"\n\n{'='*72}")
    print(f"  SUMMARY  ({len(all_results)} requests in {total_elapsed:.0f}s ≈ {actual_rpm} RPM)")
    print(f"{'='*72}")
    print(f"  Successful       : {len(ok)}")
    print(f"  Failed (non-2xx) : {len(failed)}")
    print(f"  Avg latency (ok) : {avg_lat:.1f}s\n")

    # Ordered display of fallback chain
    ordered_paths = [
        "Groq (primary)",
        "Gemini",
        "OpenRouter",
        "DDG+Gemini RAG",
        "DDG+OpenRouter RAG",
        "DDG RAG",
    ]
    print("  LLM path breakdown:")
    for p in ordered_paths:
        count = path_counts.get(p, 0)
        if count or p in ("Groq (primary)", "DDG+Gemini RAG"):
            bar = "█" * min(count, 40)
            print(f"    {p:<26}: {count:3d}  {bar}")

    other_paths = {k: v for k, v in path_counts.items() if k not in ordered_paths}
    for p, count in other_paths.items():
        print(f"    {p:<26}: {count:3d}")

    print(f"{'='*72}\n")

    # ── Verdict ───────────────────────────────────────────────────────────────
    ddg_hits   = path_counts.get("DDG+Gemini RAG", 0) + path_counts.get("DDG+OpenRouter RAG", 0) + path_counts.get("DDG RAG", 0)
    or_hits    = path_counts.get("OpenRouter", 0)
    gem_hits   = path_counts.get("Gemini", 0)
    groq_hits  = path_counts.get("Groq (primary)", 0)

    if ddg_hits:
        print(f"  ✅ FULL fallback chain validated — DDG RAG triggered {ddg_hits}×")
    elif or_hits:
        print(f"  ✅ OpenRouter triggered {or_hits}× (Groq + Gemini were rate-limited)")
        print(f"     → Run again immediately to push into the DDG RAG path")
    elif gem_hits:
        print(f"  ✅ Gemini triggered {gem_hits}× (Groq was rate-limited)")
        print(f"     → Run again immediately to exhaust Gemini too")
    elif groq_hits:
        print(f"  ℹ  All {groq_hits} requests served by Groq — rate limits not yet hit")
        print(f"     → Run again immediately or increase BATCH_SIZE/DURATION")
    if failed:
        print(f"  ⚠  {len(failed)} request(s) failed outright (non-2xx or network error)")
    print()

if __name__ == "__main__":
    main()
