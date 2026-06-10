"""Ingestion: chunking, multipart parsing, SSRF guard, file-type handling."""
import pytest
from unittest.mock import patch

from app import ingest


# ── chunking ────────────────────────────────────────────────────────────────

def test_chunk_text_empty():
    assert ingest.chunk_text("") == []
    assert ingest.chunk_text("   \n  ") == []


def test_chunk_text_short():
    chunks = ingest.chunk_text("hello world")
    assert len(chunks) == 1
    assert chunks[0]["text"] == "hello world"


def test_chunk_text_long_has_overlap():
    text = "x" * (ingest.CHUNK_SIZE * 2)
    chunks = ingest.chunk_text(text)
    assert len(chunks) >= 2
    # overlap means total characters across chunks exceed the source length
    assert sum(len(c["text"]) for c in chunks) > len(text)


def test_chunk_text_normalizes_whitespace():
    chunks = ingest.chunk_text("a   b\n\n\nc\t\td")
    assert chunks[0]["text"] == "a b c d"


# ── multipart ───────────────────────────────────────────────────────────────

def _multipart_body(boundary, filename, content):
    return (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="file"; filename="{filename}"\r\n'
        f"Content-Type: text/plain\r\n\r\n"
    ).encode() + content + f"\r\n--{boundary}--\r\n".encode()


def test_parse_single_file_multipart():
    boundary = "----testboundary123"
    body = _multipart_body(boundary, "notes.txt", b"hello evidence")
    filename, content = ingest.parse_single_file_multipart(
        f"multipart/form-data; boundary={boundary}", body
    )
    assert filename == "notes.txt"
    assert content == b"hello evidence"


def test_parse_multipart_missing_boundary():
    with pytest.raises(ingest.MultipartUploadError):
        ingest.parse_single_file_multipart("multipart/form-data", b"")


def test_parse_multipart_no_file_field():
    boundary = "b1"
    body = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="other"\r\n\r\nvalue\r\n'
        f"--{boundary}--\r\n"
    ).encode()
    with pytest.raises(ingest.MultipartUploadError):
        ingest.parse_single_file_multipart(f"multipart/form-data; boundary={boundary}", body)


# ── SSRF guard ──────────────────────────────────────────────────────────────

def _addrinfo(ip):
    import socket
    return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", (ip, 0))]


def test_ssrf_blocks_bad_scheme():
    with pytest.raises(ValueError, match="http"):
        ingest.assert_public_http_url("ftp://example.com/file")
    with pytest.raises(ValueError):
        ingest.assert_public_http_url("file:///etc/passwd")


@pytest.mark.parametrize("ip", [
    "127.0.0.1",        # loopback
    "10.0.0.5",         # RFC1918
    "172.16.1.1",       # RFC1918
    "192.168.1.1",      # RFC1918
    "169.254.169.254",  # cloud metadata (link-local)
    "0.0.0.0",          # unspecified
])
def test_ssrf_blocks_internal_ips(ip):
    with patch("app.ingest.socket.getaddrinfo", return_value=_addrinfo(ip)):
        with pytest.raises(ValueError, match="private or internal"):
            ingest.assert_public_http_url("https://innocent-looking.com/page")


def test_ssrf_allows_public_ip():
    with patch("app.ingest.socket.getaddrinfo", return_value=_addrinfo("93.184.216.34")):
        ingest.assert_public_http_url("https://example.com/article")  # no raise


def test_ssrf_unresolvable_host():
    import socket as s
    with patch("app.ingest.socket.getaddrinfo", side_effect=s.gaierror):
        with pytest.raises(ValueError, match="resolve"):
            ingest.assert_public_http_url("https://no-such-host.invalid/")


# ── file handling ───────────────────────────────────────────────────────────

def test_extract_file_rejects_unknown_type():
    with pytest.raises(ValueError, match="Unsupported file type"):
        ingest.extract_file("malware.exe", b"MZ")


def test_extract_txt_too_large():
    with pytest.raises(ValueError, match="too large"):
        ingest.extract_txt("big.txt", b"x" * (ingest.MAX_FILE_BYTES + 1))


def test_extract_txt_ok():
    title, chunks, warnings, source_type = ingest.extract_file("notes.txt", b"some evidence text")
    assert source_type == "txt"
    assert chunks[0]["text"] == "some evidence text"
    assert warnings == []


# ── YouTube helpers ─────────────────────────────────────────────────────────

def test_youtube_url_detection():
    assert ingest.is_youtube_url("https://www.youtube.com/watch?v=abc123")
    assert ingest.is_youtube_url("https://youtu.be/abc123")
    assert not ingest.is_youtube_url("https://example.com/watch?v=abc123")


def test_youtube_video_id():
    assert ingest.youtube_video_id("https://www.youtube.com/watch?v=dQw4w9WgXcQ") == "dQw4w9WgXcQ"
    assert ingest.youtube_video_id("https://youtu.be/dQw4w9WgXcQ") == "dQw4w9WgXcQ"
