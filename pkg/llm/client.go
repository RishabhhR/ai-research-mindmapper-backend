package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"ai-research-mindmapper-go/pkg/config"
	"ai-research-mindmapper-go/pkg/db"
)

type Client struct {
	cfg        *config.Config
	httpClient *http.Client
	groqURL    string
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		groqURL:    "https://api.groq.com/openai/v1/chat/completions",
	}
}

type depthProfile struct {
	nodeCount     string
	insightCount  string
	tradeoffCount string
	language      string
	summaryLength string
}

var depthProfiles = map[string]depthProfile{
	"Basic": {
		nodeCount:     "exactly 4 nodes",
		insightCount:  "2–3 insights",
		tradeoffCount: "1–2 trade-offs",
		language:      "Use plain, jargon-free language suitable for a general audience.",
		summaryLength: "Write a 1–2 sentence high-level overview.",
	},
	"Detailed": {
		nodeCount:     "6–8 nodes",
		insightCount:  "4–5 insights",
		tradeoffCount: "2–3 trade-offs",
		language:      "Use clear, moderately technical language with defined terms where needed.",
		summaryLength: "Write a 3–4 sentence balanced overview covering key themes.",
	},
	"Expert": {
		nodeCount:     "10–12 nodes",
		insightCount:  "6–8 insights",
		tradeoffCount: "4–5 trade-offs",
		language:      "Use precise technical terminology. Assume the reader has deep domain expertise.",
		summaryLength: "Write a 5–6 sentence analytical summary covering nuances, open debates, and research gaps.",
	},
}

var outputProfiles = map[string]string{
	"Mindmap":    "Structure output as an interconnected concept graph. Nodes should represent distinct sub-concepts with descriptive titles. Insights highlight cross-cutting themes. Tradeoffs capture conceptual tensions between nodes.",
	"Report":     "Structure output as a formal research report. The summary should read as an executive introduction. Nodes represent main sections (e.g., Background, Findings, Methodology, Implications, Gaps). Insights should be structured as numbered findings. Tradeoffs should frame competing schools of thought or methodological debates.",
	"Comparison": "Structure output as a comparative analysis. Each node should represent one distinct approach, technology, framework, or perspective being compared. Insights should explicitly contrast two or more options (use 'vs.' phrasing). Tradeoffs must contain concrete pros and cons for each compared option. Sources should prioritize comparative studies and benchmarks.",
	"Bullets":    "Structure output for fast scanning. The summary must be a single string containing 3–5 punchy bullet points separated by newline characters (\\n) — NOT a JSON array, always a string. Node descriptions should be single-sentence takeaways. Insights should each contain one key fact or statistic. Tradeoffs should be formatted as 'Pro: ... / Con: ...' pairs.",
}

var sourceProfiles = map[string]string{
	"Academic + web": "Prioritise peer-reviewed papers, academic databases (arXiv, PubMed, IEEE, ACM), and high-authority web sources (government, universities, established journals). Citations must include DOI or canonical URL where possible.",
	"Market signals": "Prioritise industry reports (Gartner, McKinsey, Forrester, CB Insights), press releases, earnings calls, startup funding data, and trend analyses. Frame insights around business impact, market size, growth rate, and competitive dynamics. Tradeoffs should reflect business risks and opportunities.",
	"Internal notes": "Draw exclusively from the provided evidence. Do NOT invent external sources. Every node, insight, and citation must be traceable to the supplied evidence text. If evidence is sparse, explicitly flag gaps rather than filling them with speculation. Set provenance to 'source_grounded' for all items.",
}

type llmCallResult struct {
	Offline         bool
	Content         string
	Warning         string
	FallbackWarning string
}

func (c *Client) callGroq(ctx context.Context, messages []ChatMessage, timeoutSec int) llmCallResult {
	keys := make([]string, 0, 2)
	for _, key := range []string{c.cfg.GroqAPIKey, c.cfg.GroqFallbackAPIKey} {
		if key != "" && (len(keys) == 0 || key != keys[0]) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return llmCallResult{Offline: true, Warning: "Groq API keys not set"}
	}

	model := c.cfg.GroqModel
	if model == "" {
		model = "qwen/qwen3.8-27b"
	}

	reqBody := map[string]any{
		"model":           model,
		"messages":        messages,
		"temperature":     0.2,
		"response_format": map[string]string{"type": "json_object"},
	}

	b, _ := json.Marshal(reqBody)
	last := llmCallResult{Offline: true, Warning: "Groq unavailable"}
	for _, key := range keys {
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.groqURL, bytes.NewReader(b))
		if err != nil {
			cancel()
			return llmCallResult{Offline: true, Warning: "Groq request could not be created"}
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			cancel()
			last = llmCallResult{Offline: true, Warning: fmt.Sprintf("Groq request failed: %v", err)}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			last = llmCallResult{Offline: true, Warning: fmt.Sprintf("Groq returned HTTP %d", resp.StatusCode)}
			resp.Body.Close()
			cancel()
			continue
		}

		var data struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		cancel()
		if err != nil || len(data.Choices) == 0 || data.Choices[0].Message.Content == "" {
			last = llmCallResult{Offline: true, Warning: "Groq response decode failed"}
			continue
		}
		return llmCallResult{Content: data.Choices[0].Message.Content}
	}
	return last
}

func (c *Client) callGemini(ctx context.Context, systemPrompt, userPrompt string, timeoutSec int) llmCallResult {
	if c.cfg.GeminiAPIKey == "" {
		return llmCallResult{Offline: true, Content: "", Warning: "GEMINI_API_KEY not set"}
	}

	model := c.cfg.GeminiModel
	if model == "" {
		model = "gemini-3.5-flash-lite"
	}
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", model)

	reqBody := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]string{{"text": systemPrompt}},
		},
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]string{{"text": userPrompt}}},
		},
		"generationConfig": map[string]any{
			"responseMimeType": "application/json",
			"temperature":      0.2,
		},
	}

	b, _ := json.Marshal(reqBody)
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return llmCallResult{Offline: true, Warning: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", c.cfg.GeminiAPIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return llmCallResult{Offline: true, Warning: fmt.Sprintf("Gemini request failed: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return llmCallResult{Offline: true, Warning: fmt.Sprintf("Gemini returned HTTP %d", resp.StatusCode)}
	}

	var data struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || len(data.Candidates) == 0 || len(data.Candidates[0].Content.Parts) == 0 {
		return llmCallResult{Offline: true, Warning: "Gemini response decode failed"}
	}

	return llmCallResult{
		Offline: false,
		Content: data.Candidates[0].Content.Parts[0].Text,
	}
}

func (c *Client) callOpenRouter(ctx context.Context, messages []ChatMessage, timeoutSec int) llmCallResult {
	if c.cfg.OpenRouterAPIKey == "" {
		return llmCallResult{Offline: true, Content: "", Warning: "OPENROUTER_API_KEY not set"}
	}

	model := c.cfg.OpenRouterModel
	if model == "" {
		model = "openai/gpt-oss-20b"
	}
	reqBody := map[string]any{
		"model":           model,
		"messages":        messages,
		"temperature":     0.2,
		"response_format": map[string]string{"type": "json_object"},
	}

	b, _ := json.Marshal(reqBody)
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return llmCallResult{Offline: true, Warning: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.OpenRouterAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return llmCallResult{Offline: true, Warning: fmt.Sprintf("OpenRouter request failed: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return llmCallResult{Offline: true, Warning: fmt.Sprintf("OpenRouter returned HTTP %d", resp.StatusCode)}
	}

	var data struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || len(data.Choices) == 0 {
		return llmCallResult{Offline: true, Warning: "OpenRouter response decode failed"}
	}

	return llmCallResult{
		Offline: false,
		Content: data.Choices[0].Message.Content,
	}
}

func (c *Client) CallLLMWithFallback(ctx context.Context, messages []ChatMessage, llmTimeout, maxStages int) llmCallResult {
	// 1. Groq
	res := c.callGroq(ctx, messages, llmTimeout)
	if !res.Offline {
		return res
	}

	var sysPrompt, userPrompt string
	for _, m := range messages {
		if m.Role == "system" {
			sysPrompt = m.Content
		} else if m.Role == "user" {
			userPrompt = m.Content
		}
	}

	// 2. Gemini
	geminiTimeout := llmTimeout
	if geminiTimeout < 20 {
		geminiTimeout = 20
	}
	geminiRes := c.callGemini(ctx, sysPrompt, userPrompt, geminiTimeout)
	if !geminiRes.Offline {
		geminiRes.FallbackWarning = "Groq unavailable. Used Gemini fallback."
		return geminiRes
	}

	if maxStages <= 2 {
		return geminiRes
	}

	// 3. OpenRouter
	openRouterTimeout := llmTimeout
	if openRouterTimeout < 20 {
		openRouterTimeout = 20
	}
	openRouterRes := c.callOpenRouter(ctx, messages, openRouterTimeout)
	if !openRouterRes.Offline {
		openRouterRes.FallbackWarning = "Groq and Gemini unavailable. Used OpenRouter fallback."
		return openRouterRes
	}

	return llmCallResult{Offline: true, Warning: "All configured research models are unavailable."}
}

func buildMindmapPrompt(topic, depth, output, source, evidence string, webSearch bool) []ChatMessage {
	provenance := "source_grounded"
	if webSearch {
		provenance = "ai_synthesized"
	}

	sourceBlock := "No source text was provided. Use general model knowledge only. Do not invent source titles, URLs, citations, or claim web search was performed."
	if evidence != "" {
		if len(evidence) > 12000 {
			evidence = evidence[:12000]
		}
		sourceBlock = evidence
	}

	dp, ok := depthProfiles[depth]
	if !ok {
		dp = depthProfiles["Detailed"]
	}
	op, ok := outputProfiles[output]
	if !ok {
		op = outputProfiles["Mindmap"]
	}
	sp, ok := sourceProfiles[source]
	if !ok {
		sp = sourceProfiles["Academic + web"]
	}
	if webSearch {
		sp = "No external search results were retrieved. Use general model knowledge and mark every claim as AI synthesis."
	}

	systemPrompt := "Return only valid JSON. You are a structured research synthesis engine. " +
		"Every insight, node, tradeoff, source, and citation must include a provenance field " +
		"whose value is exactly one of: 'source_grounded', 'ai_synthesized', or 'web_enriched'. " +
		"Only claim source_grounded when the claim appears in provided evidence. Do not fabricate sources or citations."

	userPrompt := fmt.Sprintf(`Topic: %s

=== DEPTH: %s ===
- Produce %s.
- Produce %s.
- Produce %s.
- %s
- %s

=== OUTPUT FORMAT: %s ===
%s

=== SOURCE MIX: %s ===
%s

=== PROVIDED EVIDENCE ===
%s

=== INSTRUCTIONS ===
Default provenance for AI-generated content: %s
Return JSON with exactly these top-level keys:
  summary       : string
  nodes         : array of objects — title (string), description (string), x (number 0–90), y (number 0–90), provenance (string)
  insights      : array of objects — title (string), body (string), provenance (string)
  sources       : array of objects — title (string), type (string), confidence (string: "High"|"Medium"|"Low"), body (string), provenance (string), url (string)
  tradeoffs     : array of objects — title (string), body (string), provenance (string)
  citations     : array of objects — title (string), url (string), snippet (string), provenance (string)

Do not include any markdown, code fences, or explanatory text outside the JSON object.
`, topic, depth, dp.nodeCount, dp.insightCount, dp.tradeoffCount, dp.language, dp.summaryLength,
		output, op, source, sp, sourceBlock, provenance)

	return []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
}

var jsonBlockRegex = regexp.MustCompile(`(?s)\{.*\}`)

func parseJSONOrFallback(content string, fallback ResearchPayload) (ResearchPayload, bool) {
	if content == "" {
		return fallback, false
	}

	var parsed ResearchPayload
	if err := json.Unmarshal([]byte(content), &parsed); err == nil && parsed.Summary != "" && len(parsed.Nodes) > 0 {
		return parsed, true
	}

	// Regex search for { ... }
	match := jsonBlockRegex.FindString(content)
	if match != "" {
		if err := json.Unmarshal([]byte(match), &parsed); err == nil && parsed.Summary != "" && len(parsed.Nodes) > 0 {
			return parsed, true
		}
	}

	// If unmarshaling failed, set fallback summary to preview of content
	if len(content) > 900 {
		content = content[:900]
	}
	fallback.Summary = content
	return fallback, false
}

func (c *Client) GenerateResearch(ctx context.Context, topic, depth, output, source string, chunks []db.ChunkItem, webSearch bool) ResearchPayload {
	var evidenceLines []string
	for i, chk := range chunks {
		if i >= 10 {
			break
		}
		evidenceLines = append(evidenceLines, fmt.Sprintf("[%d] %s (%s): %s", i+1, chk.Title, chk.URL, chk.Text))
	}
	evidenceText := strings.Join(evidenceLines, "\n\n")

	fallbackProv := "ai_synthesized"
	if len(chunks) > 0 {
		fallbackProv = "source_grounded"
	}
	fallback := OfflineGeneration(topic, chunks, fallbackProv)
	finish := func(data ResearchPayload) ResearchPayload {
		data = normalizePayload(data)
		if len(chunks) > 0 {
			data.Sources = groundedSources(chunks)
			data.Citations = groundedCitations(chunks, 6)
		} else {
			data.Sources = nil
			data.Citations = nil
			data.Provenance = "ai_synthesized"
			for i := range data.Nodes {
				data.Nodes[i].Provenance = "ai_synthesized"
			}
			for i := range data.Insights {
				data.Insights[i].Provenance = "ai_synthesized"
			}
			for i := range data.Tradeoffs {
				data.Tradeoffs[i].Provenance = "ai_synthesized"
			}
			data.Warnings = append(data.Warnings, "No source text was provided. This is an AI overview, not verified web research.")
		}
		return data
	}

	budgetSeconds := c.cfg.GenerateBudgetSeconds
	if budgetSeconds <= 0 {
		budgetSeconds = 45
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(budgetSeconds)*time.Second)
	defer cancel()
	deadline := time.Now().Add(time.Duration(budgetSeconds) * time.Second)
	messages := buildMindmapPrompt(topic, depth, output, source, evidenceText, webSearch)

	var lastRes llmCallResult
	for attempt := 0; attempt < 2; attempt++ {
		lastRes = c.CallLLMWithFallback(callCtx, messages, 10, 3)
		if lastRes.Offline {
			break
		}

		data, ok := parseJSONOrFallback(lastRes.Content, fallback)
		if ok {
			if lastRes.FallbackWarning != "" {
				data.Warnings = append(data.Warnings, lastRes.FallbackWarning)
			}
			return finish(data)
		}

		if time.Now().After(deadline.Add(-25 * time.Second)) {
			break
		}
	}

	if lastRes.Offline {
		warn := lastRes.Warning
		if warn == "" {
			warn = "All LLM APIs unavailable; returned local fallback output."
		}
		fallback.Warnings = append(fallback.Warnings, warn)
	} else {
		fallback.Warnings = append(fallback.Warnings, "Models returned invalid JSON repeatedly. Showing fallback.")
	}

	return finish(fallback)
}

func groundedCitations(chunks []db.ChunkItem, limit int) []Citation {
	var citations []Citation
	for _, chunk := range chunks {
		if len(citations) >= limit {
			break
		}
		if strings.TrimSpace(chunk.Text) == "" {
			continue
		}
		snippet := chunk.Text
		if len(snippet) > 220 {
			snippet = snippet[:220]
		}
		title := chunk.Title
		if title == "" {
			title = "Provided evidence"
		}
		citations = append(citations, Citation{Title: title, URL: chunk.URL, Snippet: snippet, Page: chunk.Page, Provenance: "source_grounded"})
	}
	return citations
}

func groundedSources(chunks []db.ChunkItem) []Source {
	seen := make(map[string]bool)
	var sources []Source
	for _, chunk := range chunks {
		title := chunk.Title
		if title == "" {
			title = "Provided evidence"
		}
		key := title + "\x00" + chunk.URL
		if seen[key] {
			continue
		}
		seen[key] = true
		body := chunk.Text
		if len(body) > 220 {
			body = body[:220]
		}
		sources = append(sources, Source{Title: title, Type: "Imported source", Confidence: "High", Body: body, Provenance: "source_grounded", URL: chunk.URL})
	}
	return sources
}

func (c *Client) AnswerQuestion(ctx context.Context, question string, chunks []db.ChunkItem, useWeb bool) QAPayload {
	_ = useWeb // No search tool is configured; answers use stored evidence or model knowledge.
	var evidenceLines []string
	for i, chk := range chunks {
		if i >= 8 {
			break
		}
		evidenceLines = append(evidenceLines, fmt.Sprintf("[%d] %s (%s): %s", i+1, chk.Title, chk.URL, chk.Text))
	}
	evidenceText := strings.Join(evidenceLines, "\n\n")
	finish := func(data QAPayload) QAPayload {
		if len(chunks) > 0 {
			if data.EvidenceSufficient {
				data.Citations = groundedCitations(chunks, 5)
				data.Provenance = "source_grounded"
			} else {
				data.Citations = nil
				data.Provenance = "ai_synthesized"
			}
		} else {
			data.Citations = nil
			data.Provenance = "ai_synthesized"
			data.EvidenceSufficient = false
			data.Warnings = append(data.Warnings, "No source text is available for this session. This answer is not source verified.")
		}
		return data
	}

	if c.cfg.GroqAPIKey == "" && c.cfg.GroqFallbackAPIKey == "" && c.cfg.GeminiAPIKey == "" && c.cfg.OpenRouterAPIKey == "" {
		answer := "The research model is unavailable. Please try again later."
		prov := "ai_synthesized"
		if len(chunks) > 0 {
			first := chunks[0].Text
			if len(first) > 500 {
				first = first[:500]
			}
			answer = fmt.Sprintf("Based on the provided evidence, the closest answer is: %s", first)
			prov = "source_grounded"
		}

		return finish(QAPayload{
			Degraded:           true,
			Answer:             answer,
			Provenance:         prov,
			EvidenceSufficient: len(chunks) > 0,
			Warnings:           []string{"Research model is unavailable; showing an extractive answer."},
		})
	}

	mode := "Use only the provided evidence. If it does not answer the question, say so clearly."
	if len(chunks) == 0 {
		mode = "No evidence was supplied. Give a clearly qualified general answer without sources or citations."
	}

	systemPrompt := "Return only valid JSON with keys answer, provenance, citations, evidence_sufficient. Do not claim to have searched the web or fabricate citations."
	userPrompt := fmt.Sprintf(`Question: %s
Mode: %s

Evidence:
%s

Return citations as objects with title, url, snippet, provenance. Output must be in JSON format.
`, question, mode, evidenceText)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	prov := "ai_synthesized"
	if len(chunks) > 0 {
		prov = "source_grounded"
	}

	fallbackQA := QAPayload{
		Answer:             "",
		Provenance:         prov,
		EvidenceSufficient: len(chunks) > 0,
	}

	callRes := c.CallLLMWithFallback(ctx, messages, 8, 3)
	if callRes.Offline {
		fallbackQA.Degraded = true
		if len(chunks) > 0 {
			snip := chunks[0].Text
			if len(snip) > 500 {
				snip = snip[:500]
			}
			fallbackQA.Answer = fmt.Sprintf("Based on the provided evidence, the closest answer is: %s", snip)
		} else {
			fallbackQA.Answer = "The research model is unavailable. Please try again later."
		}
		fallbackQA.Warnings = []string{callRes.Warning}
		return finish(fallbackQA)
	}

	var parsed QAPayload
	if err := json.Unmarshal([]byte(callRes.Content), &parsed); err == nil && parsed.Answer != "" {
		if callRes.FallbackWarning != "" {
			parsed.Warnings = append(parsed.Warnings, callRes.FallbackWarning)
		}
		return finish(parsed)
	}

	match := jsonBlockRegex.FindString(callRes.Content)
	if match != "" {
		if err := json.Unmarshal([]byte(match), &parsed); err == nil && parsed.Answer != "" {
			if callRes.FallbackWarning != "" {
				parsed.Warnings = append(parsed.Warnings, callRes.FallbackWarning)
			}
			return finish(parsed)
		}
	}

	fallbackQA.Answer = "The research model returned an unreadable answer. Please try again."
	fallbackQA.Degraded = true
	fallbackQA.Warnings = append(fallbackQA.Warnings, "Model response could not be parsed.")
	return finish(fallbackQA)
}

func normalizePayload(data ResearchPayload) ResearchPayload {
	valid := map[string]bool{"source_grounded": true, "ai_synthesized": true, "web_enriched": true}
	clean := func(v, def string) string {
		if valid[v] {
			return v
		}
		return def
	}

	data.Provenance = clean(data.Provenance, "ai_synthesized")
	for i := range data.Nodes {
		data.Nodes[i].Provenance = clean(data.Nodes[i].Provenance, "ai_synthesized")
	}
	for i := range data.Insights {
		data.Insights[i].Provenance = clean(data.Insights[i].Provenance, "ai_synthesized")
	}
	for i := range data.Tradeoffs {
		data.Tradeoffs[i].Provenance = clean(data.Tradeoffs[i].Provenance, "ai_synthesized")
	}
	for i := range data.Citations {
		data.Citations[i].Provenance = clean(data.Citations[i].Provenance, "source_grounded")
	}
	for i := range data.Sources {
		data.Sources[i].Provenance = clean(data.Sources[i].Provenance, "ai_synthesized")
	}
	return data
}
