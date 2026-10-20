#!/usr/bin/env python3
"""
get_transcript.py — local YouTube transcript agent for AI Research Mindmapper

Runs on your machine (residential IP — no YouTube block), fetches the transcript,
and uploads it directly to the Mindmapper API as a source.

Usage:
  python3 get_transcript.py <youtube-url>
  python3 get_transcript.py <youtube-url> --session ses_abc123

Requirements (already in your venv):
  pip install youtube-transcript-api requests

Setup (one-time):
  export MINDMAPPER_API_KEY=iqJpJx8lwqSsRESXFnCqdF22kq_stRvjzmXlytBAXxM
  # or add to your ~/.zshrc
"""

import os
import re
import sys

import requests
from youtube_transcript_api import YouTubeTranscriptApi

API_BASE = "https://mindmapper-api-mu.vercel.app"
API_KEY = os.getenv("MINDMAPPER_API_KEY", "iqJpJx8lwqSsRESXFnCqdF22kq_stRvjzmXlytBAXxM")


def get_video_id(url: str) -> str:
    m = re.search(r"(?:v=|youtu\.be/)([a-zA-Z0-9_-]{8,16})", url)
    return m.group(1) if m else ""


def get_title(url: str) -> str:
    try:
        resp = requests.get(
            "https://www.youtube.com/oembed",
            params={"url": url, "format": "json"},
            timeout=6,
        )
        if resp.ok:
            return resp.json().get("title", "")
    except Exception:
        pass
    return ""


def fetch_transcript(video_id: str) -> str:
    ytt = YouTubeTranscriptApi()
    transcript = ytt.fetch(video_id)
    return " ".join(s.text for s in transcript)


def upload(text: str, filename: str, session_id: str | None) -> dict:
    headers = {
        "Content-Type": "text/plain",
        "X-Filename": filename,
        "Authorization": f"Bearer {API_KEY}",
    }
    if session_id:
        headers["X-Session-Id"] = session_id
    resp = requests.post(
        f"{API_BASE}/api/sources",
        headers=headers,
        data=text.encode("utf-8"),
        timeout=30,
    )
    resp.raise_for_status()
    return resp.json()


def main():
    if len(sys.argv) < 2:
        print("Usage: python3 get_transcript.py <youtube-url> [session-id]")
        sys.exit(1)

    url = sys.argv[1]
    session_id = sys.argv[2] if len(sys.argv) > 2 else None

    video_id = get_video_id(url)
    if not video_id:
        print("Error: could not extract video ID from URL")
        sys.exit(1)

    print(f"Fetching transcript for {video_id}...")
    try:
        text = fetch_transcript(video_id)
    except Exception as e:
        print(f"Error fetching transcript: {e}")
        sys.exit(1)

    title = get_title(url) or video_id
    safe_title = re.sub(r"[^\w\s-]", "", title).strip()[:80]
    filename = f"{safe_title}.txt"

    print(f"Got {len(text)} chars — uploading as '{filename}'...")
    try:
        result = upload(text, filename, session_id)
    except Exception as e:
        print(f"Upload failed: {e}")
        sys.exit(1)

    session = result.get("session_id") or result.get("id")
    chunks = result.get("chunks_count", 0)
    print(f"Done. session_id={session}  chunks={chunks}")
    print(f"Open: https://research-mindmapper-vite.vercel.app (load session {session})")


if __name__ == "__main__":
    main()
