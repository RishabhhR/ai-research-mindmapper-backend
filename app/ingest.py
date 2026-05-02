import os
import re
import time
from concurrent.futures import ThreadPoolExecutor, TimeoutError as FuturesTimeoutError
from html.parser import HTMLParser
from pathlib import Path
from typing import Dict, List, Tuple
from urllib.parse import parse_qs, urlparse

import requests
from bs4 import BeautifulSoup


MAX_FILE_BYTES = 8 * 1024 * 1024
CHUNK_SIZE = 1800
CHUNK_OVERLAP = 220

# Groq Whisper: max upload is 25 MB. At 32 kbps that's ~100 min of audio.
WHISPER_MAX_BYTES = 24 * 1024 * 1024  # stay a little under the hard limit

# Total seconds we allow for yt-dlp download + Whisper transcription combined.
# Keep this well under the serverless timeout (Vercel free = 10 s, Pro = 60 s).
# Override via env var so you can raise it after upgrading to Vercel Pro.
YOUTUBE_PIPELINE_TIMEOUT = int(os.getenv("YOUTUBE_PIPELINE_TIMEOUT", "8"))


class MultipartUploadError(ValueError):
    pass


def chunk_text(text: str, page=None) -> List[Dict]:
    text = normalize_text(text)
    if not text:
        return []
    chunks = []
    start = 0
    while start < len(text):
        end = min(len(text), start + CHUNK_SIZE)
        chunk = text[start:end].strip()
        if chunk:
            chunks.append({"text": chunk, "page": page})
        if end >= len(text):
            break
        start = max(0, end - CHUNK_OVERLAP)
    return chunks


def normalize_text(text: str) -> str:
    text = re.sub(r"\s+", " ", text or "")
    return text.strip()


def extract_url(url: str) -> Tuple[str, List[Dict], List[str], str]:
    if is_youtube_url(url):
        return extract_youtube(url)

    response = requests.get(
        url,
        timeout=15,
        headers={"User-Agent": "AIResearchMindmapper/1.0"},
    )
    response.raise_for_status()
    soup = BeautifulSoup(response.text, "html.parser")
    for tag in soup(["script", "style", "noscript", "svg", "header", "footer", "nav"]):
        tag.decompose()
    title = normalize_text(soup.title.get_text(" ")) if soup.title else url
    main = soup.find("main") or soup.find("article") or soup.body or soup
    text = normalize_text(main.get_text(" "))
    return title or url, chunk_text(text), [], "webpage"


def extract_txt(filename: str, data: bytes) -> Tuple[str, List[Dict], List[str], str]:
    if len(data) > MAX_FILE_BYTES:
        raise ValueError("File is too large. Keep uploads under 8 MB for v1.")
    text = data.decode("utf-8", errors="replace")
    return filename or "Uploaded text", chunk_text(text), [], "txt"


def extract_pdf(filename: str, data: bytes) -> Tuple[str, List[Dict], List[str], str]:
    if len(data) > MAX_FILE_BYTES:
        raise ValueError("File is too large. Keep uploads under 8 MB for v1.")
    try:
        from pypdf import PdfReader
    except Exception:
        return filename or "Uploaded PDF", [], ["PDF parsing requires optional package: pypdf."], "pdf"

    temp_path = Path("/tmp") / f"mindmapper_{re.sub(r'[^a-zA-Z0-9_.-]', '_', filename or 'upload.pdf')}"
    temp_path.write_bytes(data)
    reader = PdfReader(str(temp_path))
    chunks = []
    for page_index, page in enumerate(reader.pages, start=1):
        chunks.extend(chunk_text(page.extract_text() or "", page=page_index))
    try:
        temp_path.unlink()
    except OSError:
        pass
    return filename or "Uploaded PDF", chunks, [], "pdf"


def extract_file(filename: str, data: bytes) -> Tuple[str, List[Dict], List[str], str]:
    suffix = Path(filename or "").suffix.lower()
    if suffix == ".txt" or not suffix:
        return extract_txt(filename, data)
    if suffix == ".pdf":
        return extract_pdf(filename, data)
    raise ValueError("Unsupported file type. V1 supports .txt and .pdf uploads.")


def is_youtube_url(url: str) -> bool:
    host = urlparse(url).netloc.lower()
    return "youtube.com" in host or "youtu.be" in host


def youtube_video_id(url: str) -> str:
    parsed = urlparse(url)
    if "youtu.be" in parsed.netloc:
        return parsed.path.strip("/")
    return parse_qs(parsed.query).get("v", [""])[0]


def _youtube_oembed_title(url: str) -> str:
    """Best-effort: fetch the video title via YouTube oEmbed (works from cloud IPs)."""
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


# ── Cookie helpers ───────────────────────────────────────────────────────────

def _write_cookie_file() -> str | None:
    """
    Decode YOUTUBE_COOKIES_B64 env var and write to /tmp/yt_cookies.txt.
    Returns the path on success, None if the env var is not set.
    """
    b64 = os.getenv("YOUTUBE_COOKIES_B64", "").strip()
    if not b64:
        return None
    import base64
    path = "/tmp/yt_cookies.txt"
    try:
        Path(path).write_bytes(base64.b64decode(b64))
        return path
    except Exception:
        return None


# ── Approach 0: Supadata transcript API ─────────────────────────────────────

def _fetch_via_supadata(url: str) -> Tuple[List[Dict], str]:
    """
    Call Supadata's transcript API — they handle YouTube IP restrictions on
    their end so this works from any cloud host.
    Returns (chunks, warning_or_empty).
    Handles both synchronous (200) and async (202) responses.
    """
    api_key = os.getenv("SUPADATA_API_KEY", "").strip()
    if not api_key:
        return [], "SUPADATA_API_KEY not set."

    headers = {"x-api-key": api_key}
    params = {"url": url, "text": "true", "lang": "en"}

    try:
        resp = requests.get(
            "https://api.supadata.ai/v1/transcript",
            params=params,
            headers=headers,
            timeout=15,
        )
    except Exception as exc:
        return [], f"Supadata request failed: {str(exc)[:150]}"

    if resp.status_code == 202:
        # Async job — poll until complete (max ~50 s)
        try:
            job_id = resp.json().get("jobId")
        except Exception:
            return [], "Supadata returned 202 but no jobId."
        if not job_id:
            return [], "Supadata returned 202 but no jobId."
        for _ in range(25):
            time.sleep(2)
            try:
                poll = requests.get(
                    f"https://api.supadata.ai/v1/transcript/{job_id}",
                    headers=headers,
                    timeout=10,
                )
                if not poll.ok:
                    continue
                data = poll.json()
                status = data.get("status", "")
                if status == "completed":
                    content = data.get("content", "")
                    if isinstance(content, str) and len(content) > 80:
                        return chunk_text(content), ""
                    return [], "Supadata job completed but returned empty content."
                if status == "failed":
                    return [], f"Supadata async job failed: {data.get('error', 'unknown')}"
            except Exception:
                continue
        return [], "Supadata async job timed out after 50 s."

    if not resp.ok:
        try:
            err = resp.json().get("message") or resp.json().get("error") or str(resp.status_code)
        except Exception:
            err = str(resp.status_code)
        return [], f"Supadata error: {err}"

    try:
        data = resp.json()
    except Exception:
        return [], "Supadata returned non-JSON response."

    content = data.get("content", "")
    if isinstance(content, str) and len(content) > 80:
        return chunk_text(content), ""
    # Timestamped segment array fallback (text=true should prevent this)
    if isinstance(content, list):
        text = " ".join(seg.get("text", "") for seg in content if seg.get("text"))
        if len(text) > 80:
            return chunk_text(text), ""

    return [], "Supadata returned an empty transcript."


# ── Approach 1: youtube-transcript-api ──────────────────────────────────────

def _fetch_transcript_api(video_id: str) -> Tuple[List[Dict], List[str]]:
    """
    Try the pre-generated transcript route.
    Fast (no download), but blocked by YouTube from most cloud IPs.
    Returns (chunks, warnings).
    """
    try:
        from youtube_transcript_api import YouTubeTranscriptApi
    except Exception:
        return [], ["youtube-transcript-api package not installed."]

    try:
        ytt = YouTubeTranscriptApi()
        transcript = ytt.fetch(video_id)
        text = " ".join(snippet.text for snippet in transcript)
        return chunk_text(text), []
    except Exception as exc:
        return [], [str(exc)]


# ── Approach 2: yt-dlp subtitle download with YouTube cookies ────────────────

def _fetch_subtitles_with_cookies(url: str, video_id: str) -> Tuple[List[Dict], str]:
    """
    Use yt-dlp to download just the subtitle file (no audio/video) using the
    user's exported YouTube session cookies. Bypasses the cloud-IP block
    because the request is authenticated as a real user session.
    Returns (chunks, warning_or_empty).
    """
    cookie_path = _write_cookie_file()
    if not cookie_path:
        return [], "YOUTUBE_COOKIES_B64 not set."

    try:
        import yt_dlp
    except ImportError:
        return [], "yt-dlp not installed."

    sub_base = f"/tmp/yt_sub_{video_id}_{int(time.time())}"

    ydl_opts = {
        "cookiefile": cookie_path,
        "skip_download": True,        # subtitle only — no audio download
        "writesubtitles": True,       # manual captions
        "writeautomaticsub": True,    # auto-generated captions
        "subtitleslangs": ["en", "en-US", "en-GB"],
        "subtitlesformat": "json3",
        "outtmpl": sub_base,
        "quiet": True,
        "no_warnings": True,
        "noplaylist": True,
        "socket_timeout": 10,
    }

    try:
        with yt_dlp.YoutubeDL(ydl_opts) as ydl:
            ydl.download([url])
    except Exception as exc:
        return [], f"yt-dlp subtitle download failed: {str(exc)[:200]}"

    # Find the written subtitle file (yt-dlp appends lang + ext)
    for suffix in [".en.json3", ".en-US.json3", ".en-GB.json3"]:
        sub_path = sub_base + suffix
        if os.path.exists(sub_path):
            try:
                import json
                data = json.loads(Path(sub_path).read_text())
                events = data.get("events", [])
                text = " ".join(
                    s.get("utf8", "")
                    for e in events if e.get("segs")
                    for s in e["segs"]
                ).replace("\n", " ").strip()
                return chunk_text(text), ""
            except Exception as exc:
                return [], f"Could not parse subtitle file: {str(exc)[:150]}"
            finally:
                try:
                    os.unlink(sub_path)
                except OSError:
                    pass

    return [], "Subtitle file not found after yt-dlp ran."


# ── Approach 3: Vercel Edge Function proxy (Cloudflare IPs) ─────────────────

def _fetch_via_edge(video_id: str) -> Tuple[List[Dict], str]:
    """
    Call our own /api/yt-transcript edge function which runs on Cloudflare
    edge nodes — not AWS/GCP datacenter IPs — so YouTube's block doesn't apply.

    EDGE_TRANSCRIPT_URL should be set to:
        https://research-mindmapper-vite.vercel.app/api/yt-transcript
    """
    edge_url = os.getenv("EDGE_TRANSCRIPT_URL", "").strip()
    if not edge_url:
        return [], "EDGE_TRANSCRIPT_URL not configured."
    try:
        resp = requests.get(
            edge_url,
            params={"v": video_id},
            timeout=12,
            headers={"User-Agent": "AIResearchMindmapper/1.0"},
        )
        data = resp.json()
        if resp.ok and data.get("success") and data.get("text"):
            return chunk_text(data["text"]), ""
        return [], data.get("error", f"Edge function returned HTTP {resp.status_code}")
    except Exception as exc:
        return [], f"Edge function unreachable: {str(exc)[:150]}"


# ── Approach 3: yt-dlp audio download + Groq Whisper STT ────────────────────

def _download_audio(url: str, video_id: str) -> str:
    """
    Download the lowest-bitrate audio stream to /tmp using yt-dlp.
    Returns the local file path on success, raises on failure.
    yt-dlp has better bot-evasion than youtube-transcript-api and can often
    pull audio-only streams from IPs that are blocked for transcript scraping.
    """
    try:
        import yt_dlp  # noqa: F401 (presence check)
    except ImportError:
        raise RuntimeError("yt-dlp is not installed.")

    import yt_dlp

    audio_path = f"/tmp/yt_audio_{video_id}_{int(time.time())}"

    ydl_opts = {
        # Prefer lowest-bitrate m4a/opus so the file stays small.
        # These containers don't require ffmpeg post-processing.
        "format": (
            "worstaudio[ext=m4a]/worstaudio[ext=webm]"
            "/bestaudio[ext=m4a]/bestaudio[ext=webm]/bestaudio"
        ),
        "outtmpl": audio_path + ".%(ext)s",
        "quiet": True,
        "no_warnings": True,
        "noplaylist": True,
        # Abort if the audio would exceed Whisper's 25 MB cap.
        "max_filesize": WHISPER_MAX_BYTES,
        # Socket-level timeout; keeps individual ops from hanging indefinitely.
        "socket_timeout": 4,
        # Realistic browser fingerprint reduces bot-detection rate.
        "http_headers": {
            "User-Agent": (
                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
                "AppleWebKit/537.36 (KHTML, like Gecko) "
                "Chrome/124.0.0.0 Safari/537.36"
            ),
        },
    }

    with yt_dlp.YoutubeDL(ydl_opts) as ydl:
        info = ydl.extract_info(url, download=True)

    # yt-dlp appends the real extension; find the file.
    ext = (info or {}).get("ext", "")
    candidate = f"{audio_path}.{ext}"
    if os.path.exists(candidate):
        return candidate

    # Fallback scan in case ext was wrong
    for f in Path("/tmp").glob(f"yt_audio_{video_id}_*"):
        if f.suffix in {".m4a", ".webm", ".mp3", ".ogg", ".opus"}:
            return str(f)

    raise RuntimeError("yt-dlp finished but audio file not found in /tmp.")


def _transcribe_with_groq_whisper(audio_path: str) -> str:
    """
    POST the audio file to Groq's Whisper-large-v3 endpoint and return the
    transcript text.  Uses the same GROQ_API_KEY as the LLM pipeline.
    """
    api_key = os.getenv("GROQ_API_KEY")
    if not api_key:
        raise RuntimeError("GROQ_API_KEY not set — cannot use Whisper transcription.")

    file_size = os.path.getsize(audio_path)
    if file_size > WHISPER_MAX_BYTES:
        raise RuntimeError(
            f"Audio file is {file_size // (1024*1024)} MB, "
            "which exceeds Groq Whisper's 25 MB limit. "
            "Try a shorter video."
        )

    with open(audio_path, "rb") as fh:
        filename = Path(audio_path).name
        mime = "audio/mp4" if audio_path.endswith(".m4a") else "audio/webm"
        resp = requests.post(
            "https://api.groq.com/openai/v1/audio/transcriptions",
            timeout=30,
            headers={"Authorization": f"Bearer {api_key}"},
            files={"file": (filename, fh, mime)},
            data={
                "model": "whisper-large-v3",
                "response_format": "text",
                "language": "en",
            },
        )

    resp.raise_for_status()
    # response_format=text → plain text body, not JSON
    return resp.text.strip()


def _fetch_audio_whisper(url: str, video_id: str) -> Tuple[List[Dict], str]:
    """
    Full pipeline: yt-dlp audio download → Groq Whisper transcription.
    Runs in a thread so the caller can enforce a wall-clock timeout.
    Returns (chunks, warning_or_empty_string).
    """
    audio_path = None
    try:
        audio_path = _download_audio(url, video_id)
        text = _transcribe_with_groq_whisper(audio_path)
        if not text:
            return [], "Whisper returned an empty transcript."
        return chunk_text(text), ""
    except Exception as exc:
        return [], str(exc)
    finally:
        if audio_path:
            try:
                os.unlink(audio_path)
            except OSError:
                pass


# ── Public entry point ───────────────────────────────────────────────────────

def extract_youtube(url: str) -> Tuple[str, List[Dict], List[str], str]:
    video_id = youtube_video_id(url)
    if not video_id:
        raise ValueError("Could not find a YouTube video id in the URL.")

    # oEmbed title always works from cloud IPs — fetch it first.
    title = _youtube_oembed_title(url) or f"YouTube video {video_id}"

    # ── Step 1: Supadata transcript API (handles IP blocking server-side) ───────
    chunks, supadata_warning = _fetch_via_supadata(url)
    if chunks:
        return title, chunks, [], "youtube"

    # ── Step 2: try the fast pre-generated transcript route ──────────────────
    chunks, api_warnings = _fetch_transcript_api(video_id)
    if chunks:
        return title, chunks, [], "youtube"

    ip_blocked = any(
        k in " ".join(api_warnings).lower()
        for k in ("blocked", "ip", "too many", "403", "429")
    )

    # ── Step 3: yt-dlp subtitle-only download using YouTube session cookies ───
    chunks, cookie_warning = _fetch_subtitles_with_cookies(url, video_id)
    if chunks:
        return title, chunks, [], "youtube"

    # ── Step 4: Vercel Edge Function (Cloudflare IPs — free, fast) ───────────
    chunks, edge_warning = _fetch_via_edge(video_id)
    if chunks:
        return title, chunks, [], "youtube"

    # ── Step 5: yt-dlp + Groq Whisper STT ────────────────────────────────────
    # Run in a thread with a hard wall-clock timeout so we don't exceed the
    # serverless function limit.  Raise YOUTUBE_PIPELINE_TIMEOUT (default 8 s)
    # via env var after upgrading to Vercel Pro (60 s limit).
    whisper_warning = ""
    try:
        with ThreadPoolExecutor(max_workers=1) as pool:
            future = pool.submit(_fetch_audio_whisper, url, video_id)
            chunks, whisper_warning = future.result(timeout=YOUTUBE_PIPELINE_TIMEOUT)
        if chunks:
            return title, chunks, [], "youtube"
    except FuturesTimeoutError:
        whisper_warning = (
            "Audio download timed out on this server. "
            "For long videos, upgrade to Vercel Pro (60 s limit) or paste the "
            "transcript as a .txt file."
        )
    except Exception as exc:
        whisper_warning = f"Audio pipeline error: {str(exc)[:200]}"

    # ── Step 6: surface a clear, actionable message ───────────────────────────
    if ip_blocked:
        final_warning = (
            "YouTube has blocked transcript access from this server's IP. "
            "All fallback methods also failed. "
            "Please paste the video transcript as a .txt file to continue."
        )
    elif whisper_warning:
        final_warning = f"Could not transcribe YouTube video: {whisper_warning}"
    else:
        final_warning = (
            f"Could not extract YouTube transcript: {'; '.join(api_warnings)[:300]}"
        )

    return title, [], [final_warning], "youtube"


def parse_single_file_multipart(content_type: str, body: bytes) -> Tuple[str, bytes]:
    match = re.search(r"boundary=([^;]+)", content_type)
    if not match:
        raise MultipartUploadError("Missing multipart boundary.")
    boundary = match.group(1).strip('"')
    parts = body.split(("--" + boundary).encode())
    for part in parts:
        if b'Content-Disposition:' not in part or b'name="file"' not in part:
            continue
        header_blob, _, content = part.partition(b"\r\n\r\n")
        if not content:
            continue
        filename_match = re.search(rb'filename="([^"]*)"', header_blob)
        filename = filename_match.group(1).decode("utf-8", errors="replace") if filename_match else "upload.txt"
        content = content.rstrip(b"\r\n-")
        return filename, content
    raise MultipartUploadError("No file field named 'file' was found.")
