package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port                   string
	LogLevel               string
	AppVersion             string
	BackendURL             string
	InternalSecret         string
	TursoURL               string
	TursoToken             string
	ClerkPublishableKey    string
	StressTestKey          string
	ScriptAPIKey           string
	ExtensionPublicKey     string
	GroqAPIKey             string
	GroqModel              string
	GeminiAPIKey           string
	GeminiModel            string
	OpenRouterAPIKey       string
	OpenRouterModel        string
	RateLimitLLMPerMin     int
	RateLimitLLMPerDay     int
	RateLimitIngestPerMin  int
	RateLimitIngestPerDay  int
	RateLimitDisabled      bool
	FrontendURLs           []string
	SupadataAPIKey         string
	EdgeTranscriptURL      string
	YouTubeCookiesB64      string
	YouTubePipelineTimeout int
	YouTubeStatus          string
	GenerateBudgetSeconds  int
}

const DefaultClerkPublishableKey = "pk_test_c2tpbGxlZC1vcmNhLTc4LmNsZXJrLmFjY291bnRzLmRldiQ"

func Load() *Config {
	// Best-effort .env loading
	_ = godotenv.Load()

	cfg := &Config{
		Port:                   getEnv("PORT", "8008"),
		LogLevel:               getEnv("LOG_LEVEL", "INFO"),
		AppVersion:             "1.2.0",
		BackendURL:             strings.TrimRight(getEnv("BACKEND_URL", "https://mindmapper-api-mu.vercel.app"), "/"),
		InternalSecret:         getEnv("INTERNAL_SECRET", ""),
		TursoURL:               getEnv("TURSO_URL", ""),
		TursoToken:             getEnv("TURSO_AUTH_TOKEN", ""),
		ClerkPublishableKey:    getEnv("CLERK_PUBLISHABLE_KEY", DefaultClerkPublishableKey),
		StressTestKey:          getEnv("STRESS_TEST_KEY", ""),
		ScriptAPIKey:           getEnv("SCRIPT_API_KEY", ""),
		ExtensionPublicKey:     getEnv("EXTENSION_PUBLIC_KEY", ""),
		GroqAPIKey:             getEnv("GROQ_API_KEY", ""),
		GroqModel:              getEnv("GROQ_MODEL", "qwen/qwen3.8-27b"),
		GeminiAPIKey:           getEnv("GEMINI_API_KEY", ""),
		GeminiModel:            getEnv("GEMINI_MODEL", "gemini-3.5-flash-lite"),
		OpenRouterAPIKey:       getEnv("OPENROUTER_API_KEY", ""),
		OpenRouterModel:        getEnv("OPENROUTER_MODEL", "openai/gpt-oss-20b"),
		RateLimitLLMPerMin:     getEnvInt("RATE_LIMIT_LLM_PER_MIN", 6),
		RateLimitLLMPerDay:     getEnvInt("RATE_LIMIT_LLM_PER_DAY", 120),
		RateLimitIngestPerMin:  getEnvInt("RATE_LIMIT_INGEST_PER_MIN", 12),
		RateLimitIngestPerDay:  getEnvInt("RATE_LIMIT_INGEST_PER_DAY", 200),
		RateLimitDisabled:      getEnv("RATE_LIMIT_DISABLED", "") == "1",
		SupadataAPIKey:         getEnv("SUPADATA_API_KEY", ""),
		EdgeTranscriptURL:      getEnv("EDGE_TRANSCRIPT_URL", "https://mindmapper-app-zeta.vercel.app/api/yt-transcript"),
		YouTubeCookiesB64:      getEnv("YOUTUBE_COOKIES_B64", ""),
		YouTubePipelineTimeout: getEnvInt("YOUTUBE_PIPELINE_TIMEOUT", 50),
		YouTubeStatus:          getEnv("YOUTUBE_STATUS", "ok"),
		GenerateBudgetSeconds:  getEnvInt("GENERATE_BUDGET_SECONDS", 45),
	}

	origins := []string{
		"http://localhost:5173",
		"http://127.0.0.1:5173",
		"http://localhost:4173",
		"http://127.0.0.1:4173",
		"https://research-mindmapper-vite.vercel.app",
		"https://mindmapper-app-zeta.vercel.app",
	}

	for _, key := range []string{"FRONTEND_URL", "FRONTEND_URLS"} {
		val := os.Getenv(key)
		if val != "" {
			for _, part := range strings.Split(val, ",") {
				clean := strings.TrimRight(strings.TrimSpace(part), "/")
				if clean != "" {
					origins = append(origins, clean)
				}
			}
		}
	}
	cfg.FrontendURLs = origins

	return cfg
}

func getEnv(key, defaultVal string) string {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return val
}

func getEnvInt(key string, defaultVal int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return defaultVal
	}
	return n
}
