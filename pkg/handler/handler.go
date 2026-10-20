package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"ai-research-mindmapper-go/pkg/auth"
	"ai-research-mindmapper-go/pkg/config"
	"ai-research-mindmapper-go/pkg/db"
	"ai-research-mindmapper-go/pkg/ingest"
	"ai-research-mindmapper-go/pkg/llm"
	"ai-research-mindmapper-go/pkg/ratelimit"
)

type Server struct {
	cfg     *config.Config
	auth    *auth.Authenticator
	storage *db.Storage
	limiter *ratelimit.Limiter
	llm     *llm.Client
	router  *chi.Mux
}

func NewServer(cfg *config.Config, a *auth.Authenticator, s *db.Storage, l *ratelimit.Limiter, client *llm.Client) *Server {
	srv := &Server{
		cfg:     cfg,
		auth:    a,
		storage: s,
		limiter: l,
		llm:     client,
		router:  chi.NewRouter(),
	}
	srv.routes()
	return srv
}

func (s *Server) Router() http.Handler {
	return s.router
}

func (s *Server) routes() {
	r := s.router

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.CleanPath)
	r.Use(middleware.GetHead)

	// Custom JSON 404 & 405 handlers
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		s.jsonError(w, http.StatusNotFound, "Not Found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		s.jsonError(w, http.StatusMethodNotAllowed, "Method Not Allowed")
	})

	// CORS matching Python backend (open for Bearer token requests)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "X-Filename", "X-Session-Id", "X-Internal-Secret", "*"},
		AllowedMethods: []string{"GET", "POST", "DELETE", "OPTIONS", "HEAD"},
	}))

	// Root & Swagger docs
	r.Get("/", s.handleRoot)
	r.Get("/docs", s.handleDocs)
	r.Get("/openapi.json", s.handleOpenAPI)

	// Register API endpoints both under root AND under /api prefix
	register := func(router chi.Router) {
		router.Get("/health", s.handleHealth)
		router.Post("/research", s.requireAuth(s.handleResearch))
		router.Post("/sources", s.requireAuth(s.handleSources))
		router.Get("/jobs/{job_id}", s.requireAuth(s.handleGetJob))
		router.Post("/internal/jobs/{job_id}/run", s.handleRunJob)
		router.Post("/sessions/{session_id}/generate", s.requireAuth(s.handleGenerate))
		router.Post("/sessions/{session_id}/ask", s.requireAuth(s.handleAsk))
		router.Get("/sessions", s.requireAuth(s.handleListSessions))
		router.Get("/sessions/{session_id}", s.requireAuth(s.handleGetSession))
		router.Delete("/sessions/{session_id}", s.requireAuth(s.handleDeleteSession))
	}

	register(r)
	r.Route("/api", register)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		s.handleDocs(w, r)
		return
	}
	s.json(w, http.StatusOK, map[string]any{
		"name":           "AI Research Mindmapper API",
		"status":         "online",
		"version":        s.cfg.AppVersion,
		"runtime":        "golang",
		"docs":           "/docs",
		"health":         "/api/health",
		"youtube_status": s.cfg.YouTubeStatus,
	})
}

func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	html := `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>AI Research Mindmapper API — Docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
  <style>
    body { margin: 0; background: #0f0c1b; font-family: system-ui, sans-serif; }
    .topbar { display: none !important; }
    .swagger-ui { filter: invert(88%) hue-rotate(180deg); }
    .swagger-ui .info { margin: 20px 0; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: '/openapi.json',
      dom_id: '#swagger-ui',
      deepLinking: true,
      presets: [
        SwaggerUIBundle.presets.apis,
        SwaggerUIBundle.SwaggerUIStandalonePreset
      ],
      layout: "BaseLayout"
    });
  </script>
</body>
</html>`
	_, _ = w.Write([]byte(html))
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	s.json(w, http.StatusOK, map[string]any{
		"openapi": "3.0.0",
		"info": map[string]any{
			"title":       "AI Research Mindmapper API",
			"version":     s.cfg.AppVersion,
			"description": "High-performance Go backend for AI Research Mindmapper",
		},
		"paths": map[string]any{
			"/api/health": map[string]any{
				"get": map[string]any{
					"summary": "Health check",
					"responses": map[string]any{
						"200": map[string]any{"description": "Server is healthy"},
					},
				},
			},
			"/api/research": map[string]any{
				"post": map[string]any{
					"summary": "Generate mindmap from topic",
					"responses": map[string]any{
						"200": map[string]any{"description": "Mindmap synthesis"},
					},
				},
			},
			"/api/sources": map[string]any{
				"post": map[string]any{
					"summary": "Ingest URL or upload file",
					"responses": map[string]any{
						"200": map[string]any{"description": "Ingested source and chunks"},
					},
				},
			},
			"/api/sessions": map[string]any{
				"get": map[string]any{
					"summary": "List user sessions",
					"responses": map[string]any{
						"200": map[string]any{"description": "List of sessions"},
					},
				},
			},
			"/api/sessions/{session_id}": map[string]any{
				"get": map[string]any{
					"summary": "Get session details",
				},
				"delete": map[string]any{
					"summary": "Delete session",
				},
			},
			"/api/sessions/{session_id}/generate": map[string]any{
				"post": map[string]any{
					"summary": "Synthesize map from stored evidence",
				},
			},
			"/api/sessions/{session_id}/ask": map[string]any{
				"post": map[string]any{
					"summary": "Q&A over session evidence",
				},
			},
		},
	})
}

type contextKey string

const userContextKey contextKey = "user_id"

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		userID, err := s.auth.GetUserID(authHeader)
		if err != nil {
			s.jsonError(w, http.StatusUnauthorized, "Authentication required.")
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, userID)
		next(w, r.WithContext(ctx))
	}
}

func getUserID(r *http.Request) string {
	val := r.Context().Value(userContextKey)
	if val == nil {
		return ""
	}
	return val.(string)
}

func (s *Server) json(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) jsonError(w http.ResponseWriter, status int, message string) {
	s.json(w, status, map[string]string{"detail": message})
}

// GET /api/health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.json(w, http.StatusOK, map[string]any{
		"ok":             true,
		"youtube_status": s.cfg.YouTubeStatus,
		"version":        s.cfg.AppVersion,
	})
}

// POST /api/research
type ResearchRequest struct {
	Query  string `json:"query"`
	Depth  string `json:"depth"`
	Output string `json:"output"`
	Source string `json:"source"`
}

func (s *Server) handleResearch(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)
	if err := s.limiter.Enforce(r.Context(), userID, "llm"); err != nil {
		var rlErr *ratelimit.RateLimitError
		if errors.As(err, &rlErr) {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", rlErr.RetryAfter))
			s.jsonError(w, http.StatusTooManyRequests, rlErr.Message)
			return
		}
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req ResearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Query) == "" {
		s.jsonError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.Depth == "" {
		req.Depth = "Detailed"
	}
	if req.Output == "" {
		req.Output = "Mindmap"
	}
	if req.Source == "" {
		req.Source = "Academic + web"
	}

	sessionID, err := s.storage.CreateSession(r.Context(), req.Query, req.Depth, req.Output, userID)
	if err != nil {
		log.Printf("[STORAGE] Warning: failed to create session in DB, generating fallback ID: %v", err)
		sessionID = db.NewID("ses")
	}

	data := s.llm.GenerateResearch(r.Context(), req.Query, req.Depth, req.Output, req.Source, nil, true)
	_ = s.storage.SaveGeneration(r.Context(), sessionID, data.Summary, data.Nodes, data.Insights, data.Tradeoffs, data.Citations)

	data.ID = sessionID
	data.SessionID = sessionID
	data.Topic = req.Query
	data.Depth = req.Depth
	data.Output = req.Output
	data.Root = req.Query

	s.json(w, http.StatusOK, data)
}

// POST /api/sources
type SourceURLRequest struct {
	URL       string `json:"url"`
	SessionID string `json:"session_id"`
	Topic     string `json:"topic"`
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)
	if err := s.limiter.Enforce(r.Context(), userID, "ingest"); err != nil {
		var rlErr *ratelimit.RateLimitError
		if errors.As(err, &rlErr) {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", rlErr.RetryAfter))
			s.jsonError(w, http.StatusTooManyRequests, rlErr.Message)
			return
		}
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	contentType := r.Header.Get("Content-Type")

	// JSON: URL ingestion
	if strings.Contains(contentType, "application/json") {
		var payload SourceURLRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || strings.TrimSpace(payload.URL) == "" {
			s.jsonError(w, http.StatusBadRequest, "url is required")
			return
		}

		// YouTube: async background job
		if ingest.IsYouTubeURL(payload.URL) {
			sessionID := payload.SessionID
			if sessionID == "" {
				topic := payload.Topic
				if topic == "" {
					topic = payload.URL
				}
				var err error
				sessionID, err = s.storage.CreateSession(r.Context(), topic, "Detailed", "Mindmap", userID)
				if err != nil {
					s.jsonError(w, http.StatusInternalServerError, err.Error())
					return
				}
			}

			jobID, err := s.storage.CreateJob(r.Context(), sessionID, userID, "youtube", map[string]string{"url": payload.URL})
			if err != nil {
				s.jsonError(w, http.StatusInternalServerError, err.Error())
				return
			}

			s.fireJob(jobID)

			s.json(w, http.StatusOK, map[string]any{
				"id":           sessionID,
				"session_id":   sessionID,
				"job_id":       jobID,
				"source_type":  "youtube",
				"status":       "pending",
				"chunks_count": 0,
				"warnings":     []string{},
			})
			return
		}

		// All other URLs: synchronous
		sessionID := payload.SessionID
		if sessionID == "" {
			topic := payload.Topic
			if topic == "" {
				topic = payload.URL
			}
			var err error
			sessionID, err = s.storage.CreateSession(r.Context(), topic, "Detailed", "Mindmap", userID)
			if err != nil {
				log.Printf("[STORAGE] Warning: failed to create session in DB, generating fallback ID: %v", err)
				sessionID = db.NewID("ses")
			}
		}

		title, chunks, warnings, sourceType, err := ingest.ExtractWebpage(payload.URL)
		if err != nil {
			s.jsonError(w, http.StatusBadRequest, err.Error())
			return
		}

		sourceID, err := s.storage.AddSource(r.Context(), sessionID, sourceType, title, payload.URL, "")
		if err != nil {
			log.Printf("[STORAGE] Warning: failed to add source to DB: %v", err)
			sourceID = db.NewID("src")
		}
		_ = s.storage.AddChunks(r.Context(), sessionID, sourceID, sourceType, title, chunks, payload.URL)

		s.json(w, http.StatusOK, map[string]any{
			"id":           sessionID,
			"session_id":   sessionID,
			"source_id":    sourceID,
			"source_type":  sourceType,
			"title":        title,
			"chunks_count": len(chunks),
			"warnings":     warnings,
		})
		return
	}

	// File uploads: multipart/form-data or raw body
	filename := r.Header.Get("X-Filename")
	var bodyBytes []byte

	if strings.Contains(contentType, "multipart/form-data") {
		err := r.ParseMultipartForm(10 * 1024 * 1024)
		if err != nil {
			s.jsonError(w, http.StatusBadRequest, "failed to parse multipart: "+err.Error())
			return
		}
		file, fileHeader, err := r.FormFile("file")
		if err != nil {
			s.jsonError(w, http.StatusBadRequest, "no file field named 'file' found")
			return
		}
		defer file.Close()
		filename = fileHeader.Filename
		bodyBytes, _ = io.ReadAll(file)
	} else {
		if filename == "" {
			filename = "upload.txt"
		}
		bodyBytes, _ = io.ReadAll(r.Body)
	}

	sessionID := r.Header.Get("X-Session-Id")
	if sessionID == "" {
		var err error
		sessionID, err = s.storage.CreateSession(r.Context(), filename, "Detailed", "Mindmap", userID)
		if err != nil {
			log.Printf("[STORAGE] Warning: failed to create session in DB, generating fallback ID: %v", err)
			sessionID = db.NewID("ses")
		}
	}

	title, chunks, warnings, sourceType, err := ingest.ExtractFile(filename, bodyBytes)
	if err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	sourceID, err := s.storage.AddSource(r.Context(), sessionID, sourceType, title, "", "")
	if err != nil {
		log.Printf("[STORAGE] Warning: failed to add source to DB: %v", err)
		sourceID = db.NewID("src")
	}
	_ = s.storage.AddChunks(r.Context(), sessionID, sourceID, sourceType, title, chunks, "")

	s.json(w, http.StatusOK, map[string]any{
		"id":           sessionID,
		"session_id":   sessionID,
		"source_id":    sourceID,
		"source_type":  sourceType,
		"title":        title,
		"chunks_count": len(chunks),
		"warnings":     warnings,
	})
}

func (s *Server) fireJob(jobID string) {
	if s.cfg.InternalSecret == "" {
		log.Printf("INTERNAL_SECRET not set — running job %s inline in background goroutine", jobID)
		go s.processJob(jobID)
		return
	}

	// Trigger worker endpoint asynchronously
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/internal/jobs/%s/run", s.cfg.BackendURL, jobID), nil)
		if err == nil {
			req.Header.Set("X-Internal-Secret", s.cfg.InternalSecret)
			_, _ = client.Do(req)
		}
	}()
}

func (s *Server) processJob(jobID string) {
	ctx := context.Background()
	job, err := s.storage.GetJob(ctx, jobID, "")
	if err != nil || job == nil || job["status"] != "pending" {
		return
	}

	_ = s.storage.UpdateJob(ctx, jobID, "processing", nil, "")

	jobType, _ := job["type"].(string)
	sessionID, _ := job["session_id"].(string)

	if jobType == "youtube" {
		inputMap, _ := job["input"].(map[string]any)
		rawURL, _ := inputMap["url"].(string)

		title, chunks, warnings, sourceType, err := ingest.ExtractYouTube(s.cfg, rawURL)
		if err != nil {
			_ = s.storage.UpdateJob(ctx, jobID, "failed", nil, err.Error())
			return
		}

		sourceID, _ := s.storage.AddSource(ctx, sessionID, sourceType, title, rawURL, "")
		if len(chunks) > 0 {
			_ = s.storage.AddChunks(ctx, sessionID, sourceID, sourceType, title, chunks, rawURL)
		}

		result := map[string]any{
			"title":        title,
			"source_id":    sourceID,
			"session_id":   sessionID,
			"source_type":  sourceType,
			"chunks_count": len(chunks),
			"warnings":     warnings,
		}
		_ = s.storage.UpdateJob(ctx, jobID, "done", result, "")
	} else {
		_ = s.storage.UpdateJob(ctx, jobID, "failed", nil, fmt.Sprintf("Unknown job type: %s", jobType))
	}
}

// POST /api/internal/jobs/{job_id}/run
func (s *Server) handleRunJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "job_id")
	secret := r.Header.Get("X-Internal-Secret")

	if s.cfg.InternalSecret == "" || secret != s.cfg.InternalSecret {
		s.jsonError(w, http.StatusForbidden, "Forbidden")
		return
	}

	s.processJob(jobID)
	job, _ := s.storage.GetJob(r.Context(), jobID, "")
	status := "unknown"
	if job != nil {
		if st, ok := job["status"].(string); ok {
			status = st
		}
	}

	s.json(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}

// GET /api/jobs/{job_id}
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "job_id")
	userID := getUserID(r)

	job, err := s.storage.GetJob(r.Context(), jobID, userID)
	if err != nil || job == nil {
		s.jsonError(w, http.StatusNotFound, "Job not found")
		return
	}

	out := map[string]any{
		"job_id":     jobID,
		"status":     job["status"],
		"session_id": job["session_id"],
	}

	if job["status"] == "done" && job["result"] != nil {
		if resMap, ok := job["result"].(map[string]any); ok {
			for k, v := range resMap {
				out[k] = v
			}
		}
	} else if job["status"] == "failed" {
		errMsg := "Unknown error"
		if errStr, ok := job["error"].(string); ok && errStr != "" {
			errMsg = errStr
		}
		out["error"] = errMsg
	}

	s.json(w, http.StatusOK, out)
}

// POST /api/sessions/{session_id}/generate
type GenerateRequest struct {
	Topic  string `json:"topic"`
	Depth  string `json:"depth"`
	Output string `json:"output"`
	Source string `json:"source"`
}

func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	userID := getUserID(r)

	if err := s.limiter.Enforce(r.Context(), userID, "llm"); err != nil {
		var rlErr *ratelimit.RateLimitError
		if errors.As(err, &rlErr) {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", rlErr.RetryAfter))
			s.jsonError(w, http.StatusTooManyRequests, rlErr.Message)
			return
		}
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	session, err := s.storage.GetSession(r.Context(), sessionID, userID)
	if err != nil || session == nil {
		s.jsonError(w, http.StatusNotFound, "Session not found.")
		return
	}

	var req GenerateRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.Depth == "" {
		req.Depth = "Detailed"
	}
	if req.Output == "" {
		req.Output = "Mindmap"
	}
	if req.Source == "" {
		req.Source = "Academic + web"
	}

	topic := req.Topic
	if topic == "" {
		topic, _ = session["topic"].(string)
	}

	rawChunks, _ := s.storage.GetChunks(r.Context(), sessionID, 80)
	var chunkItems []db.ChunkItem
	for _, c := range rawChunks {
		txt, _ := c["text"].(string)
		chunkItems = append(chunkItems, db.ChunkItem{Text: txt})
	}

	useWeb := len(chunkItems) == 0
	data := s.llm.GenerateResearch(r.Context(), topic, req.Depth, req.Output, req.Source, chunkItems, useWeb)
	_ = s.storage.SaveGeneration(r.Context(), sessionID, data.Summary, data.Nodes, data.Insights, data.Tradeoffs, data.Citations)

	data.ID = sessionID
	data.SessionID = sessionID
	data.Topic = topic
	data.Depth = req.Depth
	data.Output = req.Output
	data.Root = topic

	s.json(w, http.StatusOK, data)
}

// POST /api/sessions/{session_id}/ask
type AskRequest struct {
	Question string `json:"question"`
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	userID := getUserID(r)

	if err := s.limiter.Enforce(r.Context(), userID, "llm"); err != nil {
		var rlErr *ratelimit.RateLimitError
		if errors.As(err, &rlErr) {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", rlErr.RetryAfter))
			s.jsonError(w, http.StatusTooManyRequests, rlErr.Message)
			return
		}
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	session, err := s.storage.GetSession(r.Context(), sessionID, userID)
	if err != nil || session == nil {
		s.jsonError(w, http.StatusNotFound, "Session not found.")
		return
	}

	var req AskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Question) == "" {
		s.jsonError(w, http.StatusBadRequest, "question is required")
		return
	}

	rawChunks, _ := s.storage.GetChunks(r.Context(), sessionID, 80)
	var chunkItems []db.ChunkItem
	for _, c := range rawChunks {
		txt, _ := c["text"].(string)
		chunkItems = append(chunkItems, db.ChunkItem{Text: txt})
	}

	ranked := rankChunks(chunkItems, req.Question)
	useWeb := len(ranked) < 2
	data := s.llm.AnswerQuestion(r.Context(), req.Question, ranked, useWeb)

	_ = s.storage.AddQA(r.Context(), sessionID, req.Question, data.Answer, data.Citations, data.Provenance)
	s.json(w, http.StatusOK, data)
}

func rankChunks(chunks []db.ChunkItem, question string) []db.ChunkItem {
	words := strings.Fields(strings.ToLower(question))
	terms := make(map[string]bool)
	for _, w := range words {
		if len(w) > 3 {
			terms[w] = true
		}
	}

	type scoredChunk struct {
		score int
		chunk db.ChunkItem
	}
	var scored []scoredChunk

	for _, chk := range chunks {
		text := strings.ToLower(chk.Text)
		score := 0
		for term := range terms {
			if strings.Contains(text, term) {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, scoredChunk{score: score, chunk: chk})
		}
	}

	if len(scored) == 0 {
		if len(chunks) > 4 {
			return chunks[:4]
		}
		return chunks
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	limit := 6
	if len(scored) < limit {
		limit = len(scored)
	}
	out := make([]db.ChunkItem, limit)
	for i := 0; i < limit; i++ {
		out[i] = scored[i].chunk
	}
	return out
}

// GET /api/sessions
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)
	list, err := s.storage.ListSessions(r.Context(), userID)
	if err != nil {
		log.Printf("[STORAGE] Warning: ListSessions error: %v", err)
		list = []map[string]any{}
	}
	if list == nil {
		list = []map[string]any{}
	}
	s.json(w, http.StatusOK, map[string]any{"sessions": list})
}

// GET /api/sessions/{session_id}
func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	userID := getUserID(r)

	session, err := s.storage.GetSession(r.Context(), sessionID, userID)
	if err != nil || session == nil {
		s.jsonError(w, http.StatusNotFound, "Session not found.")
		return
	}

	decodedSession := decodeSession(session)
	sources, _ := s.storage.GetSources(r.Context(), sessionID)
	chunks, _ := s.storage.GetChunks(r.Context(), sessionID, 20)
	rawQA, _ := s.storage.GetQA(r.Context(), sessionID)

	qaHistory := make([]map[string]any, len(rawQA))
	for i, q := range rawQA {
		qaHistory[i] = decodeQA(q)
	}

	s.json(w, http.StatusOK, map[string]any{
		"session":    decodedSession,
		"sources":    sources,
		"chunks":     chunks,
		"qa_history": qaHistory,
	})
}

func decodeSession(row map[string]any) map[string]any {
	out := make(map[string]any)
	for k, v := range row {
		out[k] = v
	}
	for _, key := range []string{"mindmap_json", "insights_json", "tradeoffs_json", "citations_json"} {
		if valStr, ok := out[key].(string); ok && valStr != "" {
			var decoded any
			if json.Unmarshal([]byte(valStr), &decoded) == nil {
				newKey := strings.Replace(key, "_json", "", 1)
				out[newKey] = decoded
			}
		}
	}
	return out
}

func decodeQA(row map[string]any) map[string]any {
	out := make(map[string]any)
	for k, v := range row {
		out[k] = v
	}
	if citationsStr, ok := out["citations_json"].(string); ok && citationsStr != "" {
		var citations any
		if json.Unmarshal([]byte(citationsStr), &citations) == nil {
			out["citations"] = citations
		} else {
			out["citations"] = []any{}
		}
	} else {
		out["citations"] = []any{}
	}
	delete(out, "citations_json")
	return out
}

// DELETE /api/sessions/{session_id}
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	userID := getUserID(r)

	deleted, err := s.storage.DeleteSession(r.Context(), sessionID, userID)
	if err != nil || !deleted {
		s.jsonError(w, http.StatusNotFound, "Session not found.")
		return
	}

	s.json(w, http.StatusOK, map[string]any{"ok": true, "deleted": sessionID})
}
