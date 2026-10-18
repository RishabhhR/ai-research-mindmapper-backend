package handler

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"ai-research-mindmapper-go/pkg/auth"
	"ai-research-mindmapper-go/pkg/config"
	"ai-research-mindmapper-go/pkg/db"
	"ai-research-mindmapper-go/pkg/handler"
	"ai-research-mindmapper-go/pkg/llm"
	"ai-research-mindmapper-go/pkg/ratelimit"
)

var (
	appHandler http.Handler
	initOnce   sync.Once
)

func initApp() {
	cfg := config.Load()

	tursoClient, err := db.NewClient(cfg.TursoURL, cfg.TursoToken)
	if err != nil {
		log.Printf("Turso client error: %v", err)
	}
	storage := db.NewStorage(tursoClient)

	initCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = storage.InitDB(initCtx)
	cancel()

	authenticator := auth.NewAuthenticator(cfg)
	limiter := ratelimit.NewLimiter(cfg, storage)
	llmClient := llm.NewClient(cfg)

	server := handler.NewServer(cfg, authenticator, storage, limiter, llmClient)
	appHandler = server.Router()
}

func Handler(w http.ResponseWriter, r *http.Request) {
	initOnce.Do(initApp)
	if appHandler == nil {
		http.Error(w, "server initialization failed", http.StatusInternalServerError)
		return
	}
	appHandler.ServeHTTP(w, r)
}
