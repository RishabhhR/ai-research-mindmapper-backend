package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-research-mindmapper-go/pkg/auth"
	"ai-research-mindmapper-go/pkg/config"
	"ai-research-mindmapper-go/pkg/db"
	"ai-research-mindmapper-go/pkg/handler"
	"ai-research-mindmapper-go/pkg/llm"
	"ai-research-mindmapper-go/pkg/ratelimit"
)

func main() {
	cfg := config.Load()

	log.Printf("Starting AI Research Mindmapper Go Backend v%s on port %s", cfg.AppVersion, cfg.Port)

	// 1. Turso Database Client
	tursoClient, err := db.NewClient(cfg.TursoURL, cfg.TursoToken)
	if err != nil {
		log.Fatalf("Database initialization error: %v", err)
	}
	storage := db.NewStorage(tursoClient)

	// 2. Best-effort DB schema initialization (matches Python cold start)
	initCtx, initCancel := context.WithTimeout(context.Background(), 20*time.Second)
	if err := storage.InitDB(initCtx); err != nil {
		log.Printf("Warning: storage.InitDB failed at startup (continuing): %v", err)
	}
	initCancel()

	// 3. Components
	authenticator := auth.NewAuthenticator(cfg)
	limiter := ratelimit.NewLimiter(cfg, storage)
	llmClient := llm.NewClient(cfg)

	// 4. Server & Routing
	server := handler.NewServer(cfg, authenticator, storage, limiter, llmClient)

	httpServer := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      server.Router(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 90 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 5. Graceful shutdown
	shutdownSig := make(chan os.Signal, 1)
	signal.Notify(shutdownSig, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Server listening on http://127.0.0.1:%s", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	<-shutdownSig
	log.Println("Shutting down server gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server forced shutdown: %v", err)
	}

	log.Println("Server stopped")
}
