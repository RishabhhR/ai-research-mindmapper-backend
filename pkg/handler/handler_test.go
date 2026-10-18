package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-research-mindmapper-go/pkg/auth"
	"ai-research-mindmapper-go/pkg/config"
	"ai-research-mindmapper-go/pkg/db"
	"ai-research-mindmapper-go/pkg/llm"
	"ai-research-mindmapper-go/pkg/ratelimit"
)

func setupTestServer() *Server {
	cfg := &config.Config{
		AppVersion:    "1.1.0",
		YouTubeStatus: "ok",
		FrontendURLs:  []string{"http://localhost:5173"},
		StressTestKey: "test_secret",
	}
	a := auth.NewAuthenticator(cfg)
	// dummy client for db / storage / limiter
	tursoClient, _ := db.NewClient("https://dummy.turso.io", "dummy_token")
	storage := db.NewStorage(tursoClient)
	limiter := ratelimit.NewLimiter(cfg, storage)
	llmClient := llm.NewClient(cfg)

	return NewServer(cfg, a, storage, limiter, llmClient)
}

func TestHealthEndpoint(t *testing.T) {
	srv := setupTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse json response: %v", err)
	}

	if body["ok"] != true {
		t.Fatalf("expected ok=true, got %v", body["ok"])
	}
	if body["youtube_status"] != "ok" {
		t.Fatalf("expected youtube_status='ok', got %v", body["youtube_status"])
	}
	if body["version"] != "1.1.0" {
		t.Fatalf("expected version='1.1.0', got %v", body["version"])
	}
}

func TestAuthRequiredEndpoint(t *testing.T) {
	srv := setupTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 without auth, got %d", w.Code)
	}
}
