import re
from html.parser import HTMLParser
from pathlib import Path
from typing import Dict, List, Tuple
from urllib.parse import parse_qs, urlparse

import requests
from bs4 import BeautifulSoup


MAX_FILE_BYTES = 8 * 1024 * 1024
CHUNK_SIZE = 1800
CHUNK_OVERLAP = 220


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


def extract_youtube(url: str) -> Tuple[str, List[Dict], List[str], str]:
    video_id = youtube_video_id(url)
    if not video_id:
        raise ValueError("Could not find a YouTube video id in the URL.")

    # Always try oEmbed first — works fine from cloud IPs.
    title = _youtube_oembed_title(url) or f"YouTube video {video_id}"

    try:
        from youtube_transcript_api import YouTubeTranscriptApi
    except Exception:
        return (
            title,
            [],
            ["YouTube transcript extraction requires the youtube-transcript-api package."],
            "youtube",
        )

    try:
        ytt = YouTubeTranscriptApi()
        transcript = ytt.fetch(video_id)
        text = " ".join(snippet.text for snippet in transcript)
        return title, chunk_text(text), [], "youtube"
    except Exception as exc:
        err = str(exc)
        # YouTube blocks transcript requests from cloud-provider IPs (AWS/GCP/Azure).
        # Surface a clear, actionable message instead of the raw library traceback.
        if "blocked" in err.lower() or "ip" in err.lower() or "too many" in err.lower():
            warning = (
                "YouTube has blocked transcript access from cloud servers. "
                "This is a known YouTube restriction on AWS/GCP/Azure IPs. "
                "Try pasting the video transcript as a text file instead."
            )
        else:
            warning = f"Could not fetch YouTube transcript: {err[:200]}"
        return title, [], [warning], "youtube"


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
