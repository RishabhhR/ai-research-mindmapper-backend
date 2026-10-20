package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-research-mindmapper-go/pkg/auth"
	"ai-research-mindmapper-go/pkg/config"
	"ai-research-mindmapper-go/pkg/db"
	"ai-research-mindmapper-go/pkg/llm"
	"ai-research-mindmapper-go/pkg/ratelimit"
)

func setupTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppVersion:        "1.1.0",
		YouTubeStatus:     "ok",
		FrontendURLs:      []string{"http://localhost:5173"},
		StressTestKey:     "test_secret",
		RateLimitDisabled: true,
	}
	a := auth.NewAuthenticator(cfg)
	dbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"cols":[],"rows":[],"affected_row_count":0}}`))
	}))
	t.Cleanup(dbServer.Close)
	tursoClient, _ := db.NewClient(dbServer.URL, "dummy_token")
	storage := db.NewStorage(tursoClient)
	limiter := ratelimit.NewLimiter(cfg, storage)
	llmClient := llm.NewClient(cfg)

	return NewServer(cfg, a, storage, limiter, llmClient)
}

func TestResearchDoesNotClaimSuccessWithoutAWorkingModel(t *testing.T) {
	srv := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/research", strings.NewReader(`{"query":"quantum computing"}`))
	req.Header.Set("Authorization", "Bearer test_secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without any configured model, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSourceCannotBeAddedToAnotherUsersSession(t *testing.T) {
	var writes atomic.Int64
	dbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stmt struct {
				SQL string `json:"sql"`
			} `json:"stmt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if !strings.HasPrefix(strings.TrimSpace(request.Stmt.SQL), "SELECT") {
			writes.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"cols":[],"rows":[],"affected_row_count":0}}`))
	}))
	defer dbServer.Close()
	client, err := db.NewClient(dbServer.URL, "test_token")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{StressTestKey: "test_secret", RateLimitDisabled: true}
	storage := db.NewStorage(client)
	srv := NewServer(cfg, auth.NewAuthenticator(cfg), storage, ratelimit.NewLimiter(cfg, storage), llm.NewClient(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/sources", strings.NewReader("This is private source text."))
	req.Header.Set("Authorization", "Bearer test_secret")
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Filename", "notes.txt")
	req.Header.Set("X-Session-Id", "other_users_session")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || writes.Load() != 0 {
		t.Fatalf("expected 404 without database writes, got %d and %d writes", w.Code, writes.Load())
	}
}

func TestRankChunksIgnoresQuestionPunctuation(t *testing.T) {
	chunks := []db.ChunkItem{
		{Text: "Unrelated introduction."},
		{Text: "Quantum computing uses qubits."},
	}
	ranked := rankChunks(chunks, "What is quantum computing?")
	if len(ranked) == 0 || ranked[0].Text != chunks[1].Text {
		t.Fatalf("expected the relevant chunk first, got %+v", ranked)
	}
}

func TestHealthEndpoint(t *testing.T) {
	srv := setupTestServer(t)
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

func TestMapSaveCORSPreflightAllowsPut(t *testing.T) {
	srv := setupTestServer(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/sessions/test/map", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" || !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), "PUT") {
		t.Fatalf("PUT preflight failed: status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
}

func TestAuthRequiredEndpoint(t *testing.T) {
	srv := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 without auth, got %d", w.Code)
	}
}
