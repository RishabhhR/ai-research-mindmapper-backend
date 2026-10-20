package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

func TestProtoValueConversion(t *testing.T) {
	// String
	pv := toProtoValue("hello")
	if pv.Type != "text" || pv.Value != "hello" {
		t.Fatalf("unexpected proto value for string: %+v", pv)
	}

	// Integer
	pv = toProtoValue(42)
	if pv.Type != "integer" || pv.Value != "42" {
		t.Fatalf("unexpected proto value for int: %+v", pv)
	}

	// Float
	pv = toProtoValue(3.14)
	if pv.Type != "float" || pv.Value != 3.14 {
		t.Fatalf("unexpected proto value for float: %+v", pv)
	}

	// Null
	pv = toProtoValue(nil)
	if pv.Type != "null" {
		t.Fatalf("unexpected proto value for nil: %+v", pv)
	}

	// From proto
	val := fromProtoValue(protoValue{Type: "integer", Value: "100"})
	if val != int64(100) {
		t.Fatalf("expected int64(100), got %v (%T)", val, val)
	}
}

func TestLiveTursoConnection(t *testing.T) {
	if os.Getenv("RUN_LIVE_TESTS") != "1" {
		t.Skip("set RUN_LIVE_TESTS=1 to call Turso")
	}
	_ = godotenv.Load("../../.env")
	tursoURL := os.Getenv("TURSO_URL")
	tursoToken := os.Getenv("TURSO_AUTH_TOKEN")
	if tursoURL == "" || tursoToken == "" {
		t.Skip("skipping live Turso test; TURSO_URL or TURSO_AUTH_TOKEN not set")
	}

	client, err := NewClient(tursoURL, tursoToken)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rs, err := client.Execute(ctx, "SELECT 1 as test")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(rs.Rows()) == 0 {
		t.Fatalf("expected at least 1 row, got 0")
	}
	row := rs.One()
	if row["test"] != int64(1) {
		t.Fatalf("expected test=1, got %v (%T)", row["test"], row["test"])
	}
}

func TestLiveStorageOperations(t *testing.T) {
	if os.Getenv("RUN_LIVE_TESTS") != "1" {
		t.Skip("set RUN_LIVE_TESTS=1 to write to Turso")
	}
	_ = godotenv.Load("../../.env")
	tursoURL := os.Getenv("TURSO_URL")
	tursoToken := os.Getenv("TURSO_AUTH_TOKEN")
	if tursoURL == "" || tursoToken == "" {
		t.Skip("skipping live storage test")
	}

	client, err := NewClient(tursoURL, tursoToken)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	storage := NewStorage(client)
	ctx := context.Background()
	if err := storage.InitDB(ctx); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	sessionID, err := storage.CreateSession(ctx, "Test Topic", "Detailed", "Mindmap", "test_user_123")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Clean up
	defer storage.DeleteSession(ctx, sessionID, "test_user_123")
	_, err = storage.AddSourceWithChunks(ctx, sessionID, "txt", "A test document", "", []ChunkItem{
		{Text: "The opening explains a general overview."},
		{Text: "The later section explains a quantum algorithm."},
	})
	if err != nil {
		t.Fatalf("AddSourceWithChunks failed: %v", err)
	}
	relevant, err := storage.GetRelevantChunks(ctx, sessionID, "What does the quantum algorithm do?", 10)
	if err != nil || len(relevant) == 0 || relevant[0]["text"] != "The later section explains a quantum algorithm." {
		t.Fatalf("relevant chunk retrieval failed: chunks=%v err=%v", relevant, err)
	}
}
