package db

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	httpURL    string
	authToken  string
	httpClient *http.Client
}

type Statement struct {
	SQL  string
	Args []any
}

type protoValue struct {
	Type   string  `json:"type"`
	Value  any     `json:"value,omitempty"`
	Base64 *string `json:"base64,omitempty"`
}

type protoStmt struct {
	SQL      string       `json:"sql"`
	Args     []protoValue `json:"args"`
	WantRows bool         `json:"want_rows"`
}

func (s protoStmt) MarshalJSON() ([]byte, error) {
	type Alias protoStmt
	if s.Args == nil {
		s.Args = []protoValue{}
	}
	return json.Marshal((*Alias)(&s))
}

type protoExecuteReq struct {
	Stmt protoStmt `json:"stmt"`
}

type protoExecuteResp struct {
	Result *protoStmtResult `json:"result,omitempty"`
	Error  *protoError      `json:"error,omitempty"`
}

type protoStmtResult struct {
	Cols             []protoCol     `json:"cols"`
	Rows             [][]protoValue `json:"rows"`
	AffectedRowCount int64          `json:"affected_row_count"`
	LastInsertRowID  *string        `json:"last_insert_rowid"`
}

type protoCol struct {
	Name string `json:"name"`
}

type protoError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

type protoBatchReq struct {
	Batch protoBatch `json:"batch"`
}

type protoBatch struct {
	Steps []protoBatchStep `json:"steps"`
}

type protoBatchStep struct {
	Condition *protoCondition `json:"condition,omitempty"`
	Stmt      protoStmt       `json:"stmt"`
}

type protoCondition struct {
	Type string          `json:"type"`
	Step *int            `json:"step,omitempty"`
	Cond *protoCondition `json:"cond,omitempty"`
}

type protoBatchResp struct {
	Result *struct {
		StepResults []*protoStmtResult `json:"step_results"`
		StepErrors  []*protoError      `json:"step_errors"`
	} `json:"result,omitempty"`
	Error *protoError `json:"error,omitempty"`
}

type ResultSet struct {
	Columns      []string
	RowsList     []map[string]any
	RowsAffected int64
}

func (rs *ResultSet) Rows() []map[string]any {
	if rs == nil {
		return nil
	}
	return rs.RowsList
}

func (rs *ResultSet) One() map[string]any {
	if rs == nil || len(rs.RowsList) == 0 {
		return nil
	}
	return rs.RowsList[0]
}

func NewClient(rawURL, authToken string) (*Client, error) {
	if rawURL == "" || authToken == "" {
		return nil, errors.New("TURSO_URL and TURSO_AUTH_TOKEN must be set")
	}

	httpURL := rawURL
	if strings.HasPrefix(httpURL, "libsql://") {
		httpURL = strings.Replace(httpURL, "libsql://", "https://", 1)
	}

	return &Client{
		httpURL:   strings.TrimRight(httpURL, "/"),
		authToken: authToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func toProtoValue(val any) protoValue {
	if val == nil {
		return protoValue{Type: "null"}
	}
	switch v := val.(type) {
	case string:
		return protoValue{Type: "text", Value: v}
	case int:
		return protoValue{Type: "integer", Value: fmt.Sprintf("%d", v)}
	case int64:
		return protoValue{Type: "integer", Value: fmt.Sprintf("%d", v)}
	case int32:
		return protoValue{Type: "integer", Value: fmt.Sprintf("%d", v)}
	case uint:
		return protoValue{Type: "integer", Value: fmt.Sprintf("%d", v)}
	case uint64:
		return protoValue{Type: "integer", Value: fmt.Sprintf("%d", v)}
	case float64:
		return protoValue{Type: "float", Value: v}
	case float32:
		return protoValue{Type: "float", Value: float64(v)}
	case bool:
		if v {
			return protoValue{Type: "integer", Value: "1"}
		}
		return protoValue{Type: "integer", Value: "0"}
	case []byte:
		enc := base64.StdEncoding.EncodeToString(v)
		return protoValue{Type: "blob", Base64: &enc}
	default:
		return protoValue{Type: "text", Value: fmt.Sprintf("%v", v)}
	}
}

func fromProtoValue(pv protoValue) any {
	switch pv.Type {
	case "null":
		return nil
	case "text":
		return pv.Value
	case "integer":
		// value is string or number
		if s, ok := pv.Value.(string); ok {
			var i int64
			fmt.Sscanf(s, "%d", &i)
			return i
		}
		if f, ok := pv.Value.(float64); ok {
			return int64(f)
		}
		return pv.Value
	case "float":
		return pv.Value
	case "blob":
		if pv.Base64 != nil {
			data, _ := base64.StdEncoding.DecodeString(*pv.Base64)
			return data
		}
		return nil
	default:
		return pv.Value
	}
}

func (c *Client) Execute(ctx context.Context, sql string, args ...any) (*ResultSet, error) {
	if c == nil || c.httpClient == nil {
		return nil, errors.New("database client is not configured")
	}

	protoArgs := make([]protoValue, len(args))
	for i, arg := range args {
		protoArgs[i] = toProtoValue(arg)
	}

	reqBody := protoExecuteReq{
		Stmt: protoStmt{
			SQL:      sql,
			Args:     protoArgs,
			WantRows: true,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal execute request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.httpURL+"/v1/execute", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create execute request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.authToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read execute response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("execute returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	var execResp protoExecuteResp
	if err := json.Unmarshal(respBytes, &execResp); err != nil {
		return nil, fmt.Errorf("unmarshal execute response: %w", err)
	}

	if execResp.Error != nil {
		return nil, fmt.Errorf("libsql error [%s]: %s", execResp.Error.Code, execResp.Error.Message)
	}

	if execResp.Result == nil {
		return &ResultSet{}, nil
	}

	return parseProtoResult(execResp.Result), nil
}

func parseProtoResult(res *protoStmtResult) *ResultSet {
	columns := make([]string, len(res.Cols))
	for i, col := range res.Cols {
		columns[i] = col.Name
	}

	rows := make([]map[string]any, len(res.Rows))
	for i, rowVals := range res.Rows {
		rowMap := make(map[string]any, len(columns))
		for j, colName := range columns {
			if j < len(rowVals) {
				rowMap[colName] = fromProtoValue(rowVals[j])
			} else {
				rowMap[colName] = nil
			}
		}
		rows[i] = rowMap
	}

	return &ResultSet{
		Columns:      columns,
		RowsList:     rows,
		RowsAffected: res.AffectedRowCount,
	}
}

func (c *Client) Batch(ctx context.Context, stmts []Statement) ([]*ResultSet, error) {
	if c == nil || c.httpClient == nil {
		return nil, errors.New("database client is not configured")
	}
	if len(stmts) == 0 {
		return nil, nil
	}

	steps := make([]protoBatchStep, 0, len(stmts)+3)
	// 0: BEGIN
	steps = append(steps, protoBatchStep{
		Stmt: protoStmt{SQL: "BEGIN", WantRows: false},
	})

	for i, stmt := range stmts {
		protoArgs := make([]protoValue, len(stmt.Args))
		for j, arg := range stmt.Args {
			protoArgs[j] = toProtoValue(arg)
		}
		prevIdx := i
		steps = append(steps, protoBatchStep{
			Condition: &protoCondition{Type: "ok", Step: &prevIdx},
			Stmt: protoStmt{
				SQL:      stmt.SQL,
				Args:     protoArgs,
				WantRows: true,
			},
		})
	}

	lastIdx := len(steps) - 1
	// COMMIT
	steps = append(steps, protoBatchStep{
		Condition: &protoCondition{Type: "ok", Step: &lastIdx},
		Stmt:      protoStmt{SQL: "COMMIT", WantRows: false},
	})
	// ROLLBACK
	steps = append(steps, protoBatchStep{
		Condition: &protoCondition{
			Type: "not",
			Cond: &protoCondition{Type: "ok", Step: &lastIdx},
		},
		Stmt: protoStmt{SQL: "ROLLBACK", WantRows: false},
	})

	reqBody := protoBatchReq{
		Batch: protoBatch{Steps: steps},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal batch request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.httpURL+"/v1/batch", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create batch request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.authToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("batch request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read batch response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("batch returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	var batchResp protoBatchResp
	if err := json.Unmarshal(respBytes, &batchResp); err != nil {
		return nil, fmt.Errorf("unmarshal batch response: %w", err)
	}

	if batchResp.Error != nil {
		return nil, fmt.Errorf("libsql batch error [%s]: %s", batchResp.Error.Code, batchResp.Error.Message)
	}

	if batchResp.Result == nil {
		return nil, errors.New("empty batch response result")
	}

	for _, stepErr := range batchResp.Result.StepErrors {
		if stepErr != nil {
			return nil, fmt.Errorf("libsql step error [%s]: %s", stepErr.Code, stepErr.Message)
		}
	}

	// Slice step_results ignoring BEGIN (index 0) and COMMIT/ROLLBACK (last 2)
	rawResults := batchResp.Result.StepResults
	if len(rawResults) < len(stmts)+3 {
		return nil, errors.New("unexpected number of step results in batch response")
	}

	out := make([]*ResultSet, len(stmts))
	for i := 0; i < len(stmts); i++ {
		res := rawResults[i+1]
		if res != nil {
			out[i] = parseProtoResult(res)
		} else {
			out[i] = &ResultSet{}
		}
	}

	return out, nil
}

func (c *Client) ExecuteScript(ctx context.Context, script string) error {
	rawStatements := strings.Split(script, ";")
	stmts := make([]Statement, 0, len(rawStatements))
	for _, raw := range rawStatements {
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" {
			stmts = append(stmts, Statement{SQL: trimmed})
		}
	}
	if len(stmts) == 0 {
		return nil
	}
	_, err := c.Batch(ctx, stmts)
	return err
}
