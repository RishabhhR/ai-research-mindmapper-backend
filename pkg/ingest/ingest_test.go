package ingest

import (
	"strings"
	"testing"
)

func TestChunkTextEmpty(t *testing.T) {
	if chunks := ChunkText("", nil); len(chunks) != 0 {
		t.Fatalf("expected empty chunks, got %d", len(chunks))
	}
	if chunks := ChunkText("   \n  ", nil); len(chunks) != 0 {
		t.Fatalf("expected empty chunks for whitespace, got %d", len(chunks))
	}
}

func TestChunkTextShort(t *testing.T) {
	chunks := ChunkText("hello world", nil)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Text != "hello world" {
		t.Fatalf("expected 'hello world', got %q", chunks[0].Text)
	}
}

func TestChunkTextLongHasOverlap(t *testing.T) {
	text := strings.Repeat("x", ChunkSize*2)
	chunks := ChunkText(text, nil)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	totalChars := 0
	for _, c := range chunks {
		totalChars += len(c.Text)
	}
	if totalChars <= len(text) {
		t.Fatalf("expected total characters (%d) to exceed original length (%d) due to overlap", totalChars, len(text))
	}
}

func TestChunkTextNormalizesWhitespace(t *testing.T) {
	chunks := ChunkText("a   b\n\n\nc\t\td", nil)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Text != "a b c d" {
		t.Fatalf("expected 'a b c d', got %q", chunks[0].Text)
	}
}

func TestSSRFBadScheme(t *testing.T) {
	if err := AssertPublicHTTPURL("ftp://example.com/file"); err == nil {
		t.Fatal("expected error for ftp scheme")
	}
	if err := AssertPublicHTTPURL("file:///etc/passwd"); err == nil {
		t.Fatal("expected error for file scheme")
	}
}

func TestSSRFBlocksInternal(t *testing.T) {
	targets := []string{
		"http://127.0.0.1/test",
		"http://localhost/test",
		"http://10.0.0.1/test",
		"http://192.168.1.1/test",
		"http://169.254.169.254/latest/meta-data",
		"http://0.0.0.0/test",
	}
	for _, url := range targets {
		if err := AssertPublicHTTPURL(url); err == nil {
			t.Fatalf("expected SSRF error for %s, but got none", url)
		}
	}
}

func TestExtractFileRejectsUnknown(t *testing.T) {
	_, _, _, _, err := ExtractFile("malware.exe", []byte("MZ"))
	if err == nil {
		t.Fatal("expected error for .exe file")
	}
}

func TestExtractTxt(t *testing.T) {
	title, chunks, warnings, srcType, err := ExtractFile("notes.txt", []byte("some evidence text"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if srcType != "txt" {
		t.Fatalf("expected srcType 'txt', got %s", srcType)
	}
	if len(chunks) != 1 || chunks[0].Text != "some evidence text" {
		t.Fatalf("unexpected chunks: %v", chunks)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if title != "notes.txt" {
		t.Fatalf("expected title 'notes.txt', got %s", title)
	}
}

func TestYouTubeURLDetection(t *testing.T) {
	if !IsYouTubeURL("https://www.youtube.com/watch?v=abc123") {
		t.Fatal("expected true for youtube.com")
	}
	if !IsYouTubeURL("https://youtu.be/abc123") {
		t.Fatal("expected true for youtu.be")
	}
	if IsYouTubeURL("https://example.com/watch?v=abc123") {
		t.Fatal("expected false for example.com")
	}
}

func TestYouTubeVideoID(t *testing.T) {
	if id := YouTubeVideoID("https://www.youtube.com/watch?v=dQw4w9WgXcQ"); id != "dQw4w9WgXcQ" {
		t.Fatalf("expected dQw4w9WgXcQ, got %s", id)
	}
	if id := YouTubeVideoID("https://youtu.be/dQw4w9WgXcQ"); id != "dQw4w9WgXcQ" {
		t.Fatalf("expected dQw4w9WgXcQ, got %s", id)
	}
}
