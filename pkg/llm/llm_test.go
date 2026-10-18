package llm

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/joho/godotenv"

	"ai-research-mindmapper-go/pkg/config"
)

func TestParseJSONOrFallback(t *testing.T) {
	fallback := ResearchPayload{Summary: "fallback summary"}

	// Clean JSON
	parsed, ok := parseJSONOrFallback(`{"summary": "hi"}`, fallback)
	if !ok || parsed.Summary != "hi" {
		t.Fatalf("expected 'hi', got %q", parsed.Summary)
	}

	// Wrapped in prose
	wrapped := "Sure! Here is the JSON:\n{\"summary\": \"extracted\"}\nHope that helps."
	parsed, ok = parseJSONOrFallback(wrapped, fallback)
	if !ok || parsed.Summary != "extracted" {
		t.Fatalf("expected 'extracted', got %q", parsed.Summary)
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
	res := OfflineGeneration("Quantum Computing", nil, "ai_synthesized")
	if res.Summary == "" {
		t.Fatal("expected summary in offline generation")
	}
	if len(res.Nodes) == 0 {
		t.Fatal("expected nodes in offline generation")
	}
	if len(res.Insights) == 0 {
		t.Fatal("expected insights in offline generation")
	}
	if len(res.Tradeoffs) == 0 {
		t.Fatal("expected tradeoffs in offline generation")
	}
}

func TestLiveGroq(t *testing.T) {
	_ = godotenv.Load("../../.env")
	groqKey := os.Getenv("GROQ_API_KEY")
	if groqKey == "" {
		t.Skip("skipping live Groq test; GROQ_API_KEY not set")
	}

	cfg := &config.Config{
		GroqAPIKey:            groqKey,
		GroqModel:             "llama-3.1-8b-instant",
		GenerateBudgetSeconds: 30,
	}
	client := NewClient(cfg)
	ctx := context.Background()
	payload := client.GenerateResearch(ctx, "Quantum Computing", "Basic", "Mindmap", "Academic + web", nil, true)
	if payload.Summary == "" {
		t.Fatal("empty summary from Groq generation")
	}
	if len(payload.Nodes) == 0 {
		t.Fatal("empty nodes from Groq generation")
	}
	t.Logf("Generated %d nodes, provenance: %s", len(payload.Nodes), payload.Provenance)
}

func TestLiveGemini(t *testing.T) {
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



