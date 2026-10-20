package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/joho/godotenv"

	"ai-research-mindmapper-go/pkg/config"
	"ai-research-mindmapper-go/pkg/db"
)

func TestParseJSONOrFallback(t *testing.T) {
	fallback := ResearchPayload{Summary: "fallback summary"}

	// Clean JSON
	parsed, ok := parseJSONOrFallback(`{"summary": "hi", "nodes": [{"title": "n"}]}`, fallback)
	if !ok || parsed.Summary != "hi" {
		t.Fatalf("expected 'hi', got %q", parsed.Summary)
	}

	// Wrapped in prose
	wrapped := "Sure! Here is the JSON:\n{\"summary\": \"extracted\", \"nodes\": [{\"title\": \"n\"}]}\nHope that helps."
	parsed, ok = parseJSONOrFallback(wrapped, fallback)
	if !ok || parsed.Summary != "extracted" {
		t.Fatalf("expected 'extracted', got %q", parsed.Summary)
	}
	if _, ok := parseJSONOrFallback(`{"summary":"missing map"}`, fallback); ok {
		t.Fatal("a response with no map nodes must fall back")
	}

	// Empty content
	parsed, ok = parseJSONOrFallback("", fallback)
	if ok || parsed.Summary != "fallback summary" {
		t.Fatalf("expected fallback summary, got %q", parsed.Summary)
	}

	// Garbage content preserves degraded text as summary
	parsed, ok = parseJSONOrFallback("complete nonsense, no braces", fallback)
	if ok || parsed.Summary != "complete nonsense, no braces" {
		t.Fatalf("expected preserved nonsense summary, got %q", parsed.Summary)
	}
}

func TestNormalizePayload(t *testing.T) {
	payload := ResearchPayload{
		Provenance: "invalid_value",
		Nodes: []Node{
			{Title: "n", Provenance: "unknown"},
		},
		Citations: []Citation{
			{Title: "c", Provenance: ""},
		},
		Sources: []Source{
			{Title: "s", Confidence: "0.9"},
		},
	}

	normalized := normalizePayload(payload)
	if normalized.Provenance != "ai_synthesized" {
		t.Fatalf("expected ai_synthesized, got %s", normalized.Provenance)
	}
	if normalized.Nodes[0].Provenance != "ai_synthesized" {
		t.Fatalf("expected node provenance ai_synthesized, got %s", normalized.Nodes[0].Provenance)
	}
	if normalized.Citations[0].Provenance != "source_grounded" {
		t.Fatalf("expected citation provenance source_grounded, got %s", normalized.Citations[0].Provenance)
	}
}

func TestOfflineGeneration(t *testing.T) {
	noEvidence := OfflineGeneration("Quantum Computing", nil, "ai_synthesized")
	if !noEvidence.Degraded || len(noEvidence.Nodes) != 0 {
		t.Fatal("provider failure without evidence must not return a fabricated map")
	}
	evidence := []db.ChunkItem{{Title: "Paper", URL: "https://example.com/paper", Text: "A verified excerpt about the topic."}}
	res := OfflineGeneration("Quantum Computing", evidence, "source_grounded")
	if !res.Degraded || len(res.Nodes) != 1 || res.Nodes[0].Description != evidence[0].Text {
		t.Fatalf("expected an extractive fallback, got %+v", res)
	}
	if len(res.Citations) != 1 || res.Citations[0].URL != evidence[0].URL {
		t.Fatal("fallback citation must point to the supplied source")
	}
}

func TestAnswerQuestionWithoutProviderDoesNotClaimUngroundedSuccess(t *testing.T) {
	client := NewClient(&config.Config{})
	answer := client.AnswerQuestion(context.Background(), "What is the answer?", nil, false)
	if !answer.Degraded || answer.EvidenceSufficient || len(answer.Citations) != 0 {
		t.Fatalf("expected unavailable ungrounded answer, got %+v", answer)
	}
}

func TestGroqFallbackKeyAfterRateLimit(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.Header.Get("Authorization") {
		case "Bearer primary":
			w.WriteHeader(http.StatusTooManyRequests)
		case "Bearer fallback":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`))
		default:
			t.Errorf("unexpected authorization header")
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	client := NewClient(&config.Config{GroqAPIKey: "primary", GroqFallbackAPIKey: "fallback"})
	client.groqURL = server.URL
	result := client.callGroq(context.Background(), []ChatMessage{{Role: "user", Content: "Return JSON"}}, 2)
	if result.Offline || result.Content != `{"ok":true}` || requests != 2 {
		t.Fatalf("expected fallback key after 429, got offline=%v warning=%q content=%q requests=%d", result.Offline, result.Warning, result.Content, requests)
	}
}

func TestLiveGroq(t *testing.T) {
	if os.Getenv("RUN_LIVE_TESTS") != "1" {
		t.Skip("set RUN_LIVE_TESTS=1 to call Groq")
	}
	_ = godotenv.Load("../../.env")
	groqKey := os.Getenv("GROQ_API_KEY")
	if groqKey == "" {
		t.Skip("skipping live Groq test; GROQ_API_KEY not set")
	}

	cfg := &config.Config{
		GroqAPIKey:            groqKey,
		GroqModel:             "qwen/qwen3.8-27b",
		GenerateBudgetSeconds: 30,
	}
	client := NewClient(cfg)
	ctx := context.Background()
	direct := client.callGroq(ctx, buildMindmapPrompt("Quantum Computing", "Basic", "Mindmap", "Academic + web", "", true), 8)
	if direct.Offline {
		t.Fatalf("Groq unavailable: %s", direct.Warning)
	}
	payload := client.GenerateResearch(ctx, "Quantum Computing", "Basic", "Mindmap", "Academic + web", nil, true)
	if payload.Summary == "" {
		t.Fatal("empty summary from Groq generation")
	}
	if len(payload.Nodes) == 0 {
		t.Fatalf("empty nodes from Groq generation; warnings=%v summary=%q", payload.Warnings, payload.Summary)
	}
	t.Logf("Generated %d nodes, provenance: %s", len(payload.Nodes), payload.Provenance)
}

func TestLiveGemini(t *testing.T) {
	if os.Getenv("RUN_LIVE_TESTS") != "1" {
		t.Skip("set RUN_LIVE_TESTS=1 to call Gemini")
	}
	_ = godotenv.Load("../../.env")
	geminiKey := os.Getenv("GEMINI_API_KEY")
	if geminiKey == "" {
		t.Skip("skipping live Gemini test; GEMINI_API_KEY not set")
	}

	cfg := &config.Config{
		GeminiAPIKey:          geminiKey,
		GenerateBudgetSeconds: 30,
	}
	client := NewClient(cfg)
	ctx := context.Background()
	messages := buildMindmapPrompt("Quantum Computing", "Basic", "Mindmap", "Academic + web", "", true)
	var sysPrompt, userPrompt string
	for _, m := range messages {
		if m.Role == "system" {
			sysPrompt = m.Content
		} else if m.Role == "user" {
			userPrompt = m.Content
		}
	}
	res := client.callGemini(ctx, sysPrompt, userPrompt, 20)
	if res.Offline {
		t.Fatalf("Gemini returned offline: %s", res.Warning)
	}
	t.Logf("Gemini Content length: %d", len(res.Content))
	var payload ResearchPayload
	if err := json.Unmarshal([]byte(res.Content), &payload); err != nil {
		t.Fatalf("Gemini returned invalid JSON: %v\nContent:\n%s", err, res.Content)
	}
	t.Logf("Gemini successfully generated: %d nodes, summary: %s", len(payload.Nodes), payload.Summary)
}

func TestLiveFallbackToGemini(t *testing.T) {
	if os.Getenv("RUN_LIVE_TESTS") != "1" {
		t.Skip("set RUN_LIVE_TESTS=1 to call Gemini")
	}
	_ = godotenv.Load("../../.env")
	geminiKey := os.Getenv("GEMINI_API_KEY")
	if geminiKey == "" {
		t.Skip("skipping test; GEMINI_API_KEY not set")
	}

	cfg := &config.Config{
		GroqAPIKey:            "invalid_or_expired_groq_key",
		GeminiAPIKey:          geminiKey,
		GenerateBudgetSeconds: 40,
	}
	client := NewClient(cfg)
	ctx := context.Background()
	payload := client.GenerateResearch(ctx, "Artificial Intelligence in Medicine", "Basic", "Mindmap", "Academic + web", nil, true)
	if payload.Summary == "" {
		t.Fatal("empty summary from generation")
	}
	if len(payload.Nodes) == 0 {
		t.Fatal("empty nodes from generation")
	}
	t.Logf("Fallback successful: %d nodes, summary: %s, warnings: %v", len(payload.Nodes), payload.Summary, payload.Warnings)
}
