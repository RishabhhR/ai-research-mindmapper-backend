package ratelimit

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"ai-research-mindmapper-go/pkg/config"
	"ai-research-mindmapper-go/pkg/db"
)

type RateLimitError struct {
	Message    string
	RetryAfter int
}

func (e *RateLimitError) Error() string {
	return e.Message
}

type Limiter struct {
	cfg     *config.Config
	storage *db.Storage
}

func NewLimiter(cfg *config.Config, storage *db.Storage) *Limiter {
	return &Limiter{
		cfg:     cfg,
		storage: storage,
	}
}

func (l *Limiter) limits(kind string) (int, int) {
	if kind == "ingest" {
		return l.cfg.RateLimitIngestPerMin, l.cfg.RateLimitIngestPerDay
	}
	return l.cfg.RateLimitLLMPerMin, l.cfg.RateLimitLLMPerDay
}

func (l *Limiter) Enforce(ctx context.Context, userID, kind string) error {
	if l.cfg.RateLimitDisabled {
		return nil
	}

	now := time.Now().UTC()
	minuteBucket := fmt.Sprintf("%s|%s", now.Format("2006-01-02T15:04"), kind)
	dayBucket := fmt.Sprintf("%s|%s", now.Format("2006-01-02"), kind)
	perMin, perDay := l.limits(kind)

	minuteCount, dayCount, err := l.storage.BumpRateCounters(ctx, userID, minuteBucket, dayBucket)
	if err != nil {
		// Fail open: log warning and allow request
		log.Printf("rate limiter unavailable, allowing request: %v", err)
		return nil
	}

	// Opportunistic cleanup of stale buckets (~1% of requests)
	if rand.Float64() < 0.01 {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = l.storage.CleanupRateBuckets(bgCtx, now.Format("2006-01-02"))
		}()
	}

	if minuteCount > perMin {
		return &RateLimitError{
			Message:    "You're going a little fast — try again in a minute.",
			RetryAfter: 60,
		}
	}
	if dayCount > perDay {
		return &RateLimitError{
			Message:    "Daily research limit reached. Resets at midnight UTC.",
			RetryAfter: 3600,
		}
	}

	return nil
}
