package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Storage struct {
	client *Client
}

func NewStorage(client *Client) *Storage {
	return &Storage{client: client}
}

func NowTS() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func NewID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b))
}

func (s *Storage) InitDB(ctx context.Context) error {
	script := `
		CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			input_json TEXT,
			result_json TEXT,
			error TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			topic TEXT NOT NULL,
			depth TEXT NOT NULL,
			output TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			summary TEXT,
			mindmap_json TEXT,
			insights_json TEXT,
			tradeoffs_json TEXT,
			citations_json TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS sources (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			source_type TEXT NOT NULL,
			title TEXT NOT NULL,
			url TEXT,
			file_path TEXT,
			created_at TEXT NOT NULL,
			FOREIGN KEY(session_id) REFERENCES sessions(id)
		);

		CREATE TABLE IF NOT EXISTS source_chunks (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			source_type TEXT NOT NULL,
			title TEXT NOT NULL,
			url TEXT,
			page INTEGER,
			chunk_index INTEGER NOT NULL,
			text TEXT NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY(source_id) REFERENCES sources(id),
			FOREIGN KEY(session_id) REFERENCES sessions(id)
		);

		CREATE TABLE IF NOT EXISTS qa_history (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			question TEXT NOT NULL,
			answer TEXT NOT NULL,
			citations_json TEXT NOT NULL,
			provenance TEXT NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY(session_id) REFERENCES sessions(id)
		);

		CREATE TABLE IF NOT EXISTS rate_limits (
			user_id TEXT NOT NULL,
			bucket TEXT NOT NULL,
			count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, bucket)
		);

		CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id, updated_at);
		CREATE INDEX IF NOT EXISTS idx_chunks_session ON source_chunks(session_id);
		CREATE INDEX IF NOT EXISTS idx_sources_session ON sources(session_id);
		CREATE INDEX IF NOT EXISTS idx_qa_session ON qa_history(session_id);
		CREATE INDEX IF NOT EXISTS idx_jobs_session ON jobs(session_id);
	`
	err := s.client.ExecuteScript(ctx, script)
	if err != nil {
		return err
	}

	// Migrate sessions column if pre-dated
	_, _ = s.client.Execute(ctx, "ALTER TABLE sessions ADD COLUMN user_id TEXT NOT NULL DEFAULT ''")
	return nil
}

func (s *Storage) CreateSession(ctx context.Context, topic, depth, output, userID string) (string, error) {
	sessionID := NewID("ses")
	created := NowTS()
	query := `
		INSERT INTO sessions (id, topic, depth, output, user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.client.Execute(ctx, query, sessionID, topic, depth, output, userID, created, created)
	if err != nil {
		return "", err
	}
	return sessionID, nil
}

func (s *Storage) SaveGeneration(ctx context.Context, sessionID, summary string, nodes, insights, tradeoffs, citations any) error {
	nodesJSON, _ := json.Marshal(nodes)
	insightsJSON, _ := json.Marshal(insights)
	tradeoffsJSON, _ := json.Marshal(tradeoffs)
	citationsJSON, _ := json.Marshal(citations)

	query := `
		UPDATE sessions
		SET summary = ?, mindmap_json = ?, insights_json = ?, tradeoffs_json = ?,
		    citations_json = ?, updated_at = ?
		WHERE id = ?
	`
	_, err := s.client.Execute(ctx, query,
		summary,
		string(nodesJSON),
		string(insightsJSON),
		string(tradeoffsJSON),
		string(citationsJSON),
		NowTS(),
		sessionID,
	)
	return err
}

func (s *Storage) AddSource(ctx context.Context, sessionID, sourceType, title, url, filePath string) (string, error) {
	sourceID := NewID("src")
	query := `
		INSERT INTO sources (id, session_id, source_type, title, url, file_path, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	var urlVal any
	if url != "" {
		urlVal = url
	}
	var fileVal any
	if filePath != "" {
		fileVal = filePath
	}

	_, err := s.client.Execute(ctx, query, sourceID, sessionID, sourceType, title, urlVal, fileVal, NowTS())
	if err != nil {
		return "", err
	}
	return sourceID, nil
}

type ChunkItem struct {
	Text string `json:"text"`
	Page *int   `json:"page,omitempty"`
}

func (s *Storage) AddChunks(ctx context.Context, sessionID, sourceID, sourceType, title string, chunks []ChunkItem, url string) error {
	if len(chunks) == 0 {
		return nil
	}

	created := NowTS()
	const rowsPerStmt = 40
	statements := make([]Statement, 0, (len(chunks)+rowsPerStmt-1)/rowsPerStmt)

	for groupStart := 0; groupStart < len(chunks); groupStart += rowsPerStmt {
		groupEnd := groupStart + rowsPerStmt
		if groupEnd > len(chunks) {
			groupEnd = len(chunks)
		}
		group := chunks[groupStart:groupEnd]

		placeholders := make([]string, len(group))
		params := make([]any, 0, len(group)*10)

		var urlVal any
		if url != "" {
			urlVal = url
		}

		for offset, chunk := range group {
			placeholders[offset] = "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
			var pageVal any
			if chunk.Page != nil {
				pageVal = *chunk.Page
			}
			params = append(params,
				NewID("chk"),
				sourceID,
				sessionID,
				sourceType,
				title,
				urlVal,
				pageVal,
				groupStart+offset,
				chunk.Text,
				created,
			)
		}

		sql := fmt.Sprintf(
			"INSERT INTO source_chunks (id, source_id, session_id, source_type, title, url, page, chunk_index, text, created_at) VALUES %s",
			strings.Join(placeholders, ", "),
		)
		statements = append(statements, Statement{SQL: sql, Args: params})
	}

	_, err := s.client.Batch(ctx, statements)
	return err
}

func (s *Storage) GetSession(ctx context.Context, sessionID, userID string) (map[string]any, error) {
	var res *ResultSet
	var err error
	if userID != "" {
		res, err = s.client.Execute(ctx, "SELECT * FROM sessions WHERE id = ? AND user_id = ?", sessionID, userID)
	} else {
		res, err = s.client.Execute(ctx, "SELECT * FROM sessions WHERE id = ?", sessionID)
	}
	if err != nil {
		return nil, err
	}
	return res.One(), nil
}

func (s *Storage) ListSessions(ctx context.Context, userID string) ([]map[string]any, error) {
	query := `
		SELECT id, topic, depth, output, summary, created_at, updated_at
		FROM sessions WHERE user_id = ? ORDER BY updated_at DESC LIMIT 50
	`
	res, err := s.client.Execute(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	return res.Rows(), nil
}

func (s *Storage) GetChunks(ctx context.Context, sessionID string, limit int) ([]map[string]any, error) {
	query := `
		SELECT * FROM source_chunks
		WHERE session_id = ?
		ORDER BY source_id, chunk_index
		LIMIT ?
	`
	res, err := s.client.Execute(ctx, query, sessionID, limit)
	if err != nil {
		return nil, err
	}
	return res.Rows(), nil
}

func (s *Storage) GetSources(ctx context.Context, sessionID string) ([]map[string]any, error) {
	query := "SELECT * FROM sources WHERE session_id = ? ORDER BY created_at DESC"
	res, err := s.client.Execute(ctx, query, sessionID)
	if err != nil {
		return nil, err
	}
	return res.Rows(), nil
}

func (s *Storage) AddQA(ctx context.Context, sessionID, question, answer string, citations any, provenance string) error {
	citationsJSON, _ := json.Marshal(citations)
	query := `
		INSERT INTO qa_history (id, session_id, question, answer, citations_json, provenance, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.client.Execute(ctx, query, NewID("qa"), sessionID, question, answer, string(citationsJSON), provenance, NowTS())
	return err
}

func (s *Storage) GetQA(ctx context.Context, sessionID string) ([]map[string]any, error) {
	query := "SELECT * FROM qa_history WHERE session_id = ? ORDER BY created_at"
	res, err := s.client.Execute(ctx, query, sessionID)
	if err != nil {
		return nil, err
	}
	return res.Rows(), nil
}

func (s *Storage) DeleteSession(ctx context.Context, sessionID, userID string) (bool, error) {
	check, err := s.client.Execute(ctx, "SELECT id FROM sessions WHERE id = ? AND user_id = ?", sessionID, userID)
	if err != nil {
		return false, err
	}
	if check.One() == nil {
		return false, nil
	}

	stmts := []Statement{
		{SQL: "DELETE FROM source_chunks WHERE session_id = ?", Args: []any{sessionID}},
		{SQL: "DELETE FROM sources WHERE session_id = ?", Args: []any{sessionID}},
		{SQL: "DELETE FROM qa_history WHERE session_id = ?", Args: []any{sessionID}},
		{SQL: "DELETE FROM jobs WHERE session_id = ?", Args: []any{sessionID}},
		{SQL: "DELETE FROM sessions WHERE id = ?", Args: []any{sessionID}},
	}
	_, err = s.client.Batch(ctx, stmts)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Storage) BumpRateCounters(ctx context.Context, userID, minuteBucket, dayBucket string) (int, int, error) {
	upsert := `
		INSERT INTO rate_limits (user_id, bucket, count) VALUES (?, ?, 1)
		ON CONFLICT(user_id, bucket) DO UPDATE SET count = count + 1
		RETURNING count
	`
	stmts := []Statement{
		{SQL: upsert, Args: []any{userID, minuteBucket}},
		{SQL: upsert, Args: []any{userID, dayBucket}},
	}

	results, err := s.client.Batch(ctx, stmts)
	if err != nil {
		return 1, 1, err
	}

	minuteCount := 1
	dayCount := 1

	if len(results) >= 2 {
		if row := results[0].One(); row != nil {
			if cnt, ok := row["count"].(int64); ok {
				minuteCount = int(cnt)
			}
		}
		if row := results[1].One(); row != nil {
			if cnt, ok := row["count"].(int64); ok {
				dayCount = int(cnt)
			}
		}
	}

	return minuteCount, dayCount, nil
}

func (s *Storage) CleanupRateBuckets(ctx context.Context, olderThanPrefix string) error {
	_, err := s.client.Execute(ctx, "DELETE FROM rate_limits WHERE bucket < ?", olderThanPrefix)
	return err
}

func (s *Storage) CreateJob(ctx context.Context, sessionID, userID, jobType string, inputData any) (string, error) {
	jobID := NewID("job")
	inputJSON, _ := json.Marshal(inputData)
	created := NowTS()
	query := `
		INSERT INTO jobs (id, session_id, user_id, type, status, input_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending', ?, ?, ?)
	`
	_, err := s.client.Execute(ctx, query, jobID, sessionID, userID, jobType, string(inputJSON), created, created)
	if err != nil {
		return "", err
	}
	return jobID, nil
}

func (s *Storage) GetJob(ctx context.Context, jobID, userID string) (map[string]any, error) {
	var res *ResultSet
	var err error
	if userID != "" {
		res, err = s.client.Execute(ctx, "SELECT * FROM jobs WHERE id = ? AND user_id = ?", jobID, userID)
	} else {
		res, err = s.client.Execute(ctx, "SELECT * FROM jobs WHERE id = ?", jobID)
	}
	if err != nil {
		return nil, err
	}
	job := res.One()
	if job == nil {
		return nil, nil
	}

	if inStr, ok := job["input_json"].(string); ok && inStr != "" {
		var inputMap any
		if json.Unmarshal([]byte(inStr), &inputMap) == nil {
			job["input"] = inputMap
		}
	}
	if resStr, ok := job["result_json"].(string); ok && resStr != "" {
		var resultMap any
		if json.Unmarshal([]byte(resStr), &resultMap) == nil {
			job["result"] = resultMap
		}
	}
	return job, nil
}

func (s *Storage) UpdateJob(ctx context.Context, jobID, status string, result any, errorMsg string) error {
	var resultJSON any
	if result != nil {
		b, _ := json.Marshal(result)
		resultJSON = string(b)
	}
	var errVal any
	if errorMsg != "" {
		errVal = errorMsg
	}

	query := `
		UPDATE jobs SET status = ?, result_json = ?, error = ?, updated_at = ?
		WHERE id = ?
	`
	_, err := s.client.Execute(ctx, query, status, resultJSON, errVal, NowTS(), jobID)
	return err
}
