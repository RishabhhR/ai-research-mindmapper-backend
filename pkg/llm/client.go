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
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
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
	"Mindmap": "Structure output as an interconnected concept graph. Nodes should represent distinct sub-concepts with descriptive titles. Insights highlight cross-cutting themes. Tradeoffs capture conceptual tensions between nodes.",
	"Report": "Structure output as a formal research report. The summary should read as an executive introduction. Nodes represent main sections (e.g., Background, Findings, Methodology, Implications, Gaps). Insights should be structured as numbered findings. Tradeoffs should frame competing schools of thought or methodological debates.",
	"Comparison": "Structure output as a comparative analysis. Each node should represent one distinct approach, technology, framework, or perspective being compared. Insights should explicitly contrast two or more options (use 'vs.' phrasing). Tradeoffs must contain concrete pros and cons for each compared option. Sources should prioritize comparative studies and benchmarks.",
	"Bullets": "Structure output for fast scanning. The summary must be a single string containing 3–5 punchy bullet points separated by newline characters (\\n) — NOT a JSON array, always a string. Node descriptions should be single-sentence takeaways. Insights should each contain one key fact or statistic. Tradeoffs should be formatted as 'Pro: ... / Con: ...' pairs.",
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
	if c.cfg.GroqAPIKey == "" {
		return llmCallResult{Offline: true, Content: "", Warning: "GROQ_API_KEY not set"}
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
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return llmCallResult{Offline: true, Warning: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.GroqAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return llmCallResult{Offline: true, Warning: fmt.Sprintf("Groq request failed: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return llmCallResult{Offline: true, Warning: fmt.Sprintf("Groq returned HTTP %d", resp.StatusCode)}
	}

	var data struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || len(data.Choices) == 0 {
		return llmCallResult{Offline: true, Warning: "Groq response decode failed"}
	}

	return llmCallResult{
		Offline: false,
		Content: data.Choices[0].Message.Content,
	}
}

func (c *Client) callGemini(ctx context.Context, systemPrompt, userPrompt string, timeoutSec int) llmCallResult {
	if c.cfg.GeminiAPIKey == "" {
		return llmCallResult{Offline: true, Content: "", Warning: "GEMINI_API_KEY not set"}
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-flash-latest:generateContent?key=%s", c.cfg.GeminiAPIKey)

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

	reqBody := map[string]any{
		"model":       "mistralai/mistral-7b-instruct",
		"messages":    messages,
		"temperature": 0.2,
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
	geminiRes := c.callGemini(ctx, sysPrompt, userPrompt, llmTimeout)
	if !geminiRes.Offline {
		geminiRes.FallbackWarning = "Groq unavailable. Used Gemini fallback."
		return geminiRes
	}

	if maxStages <= 2 {
		return geminiRes
	}

	// 3. OpenRouter
	openRouterRes := c.callOpenRouter(ctx, messages, llmTimeout)
	if !openRouterRes.Offline {
		openRouterRes.FallbackWarning = "Groq and Gemini unavailable. Used OpenRouter fallback."
		return openRouterRes
	}

	return res
}

func buildMindmapPrompt(topic, depth, output, source, evidence string, webSearch bool) []ChatMessage {
	provenance := "source_grounded"
	if webSearch {
		provenance = "web_enriched"
	}

	sourceBlock := "No local evidence was provided."
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

	systemPrompt := "Return only valid JSON. You are a structured research synthesis engine. " +
		"Every insight, node, tradeoff, source, and citation must include a provenance field " +
		"whose value is exactly one of: 'source_grounded', 'ai_synthesized', or 'web_enriched'."

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
	if err := json.Unmarshal([]byte(content), &parsed); err == nil && parsed.Summary != "" {
		return parsed, true
	}

	// Regex search for { ... }
	match := jsonBlockRegex.FindString(content)
	if match != "" {
		if err := json.Unmarshal([]byte(match), &parsed); err == nil && parsed.Summary != "" {
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
		evidenceLines = append(evidenceLines, fmt.Sprintf("[%d] %s", i+1, chk.Text))
	}
	evidenceText := strings.Join(evidenceLines, "\n\n")

	fallbackProv := "source_grounded"
	if webSearch {
		fallbackProv = "web_enriched"
	}
	fallback := OfflineGeneration(topic, chunks, fallbackProv)

	deadline := time.Now().Add(time.Duration(c.cfg.GenerateBudgetSeconds) * time.Second)
	messages := buildMindmapPrompt(topic, depth, output, source, evidenceText, webSearch)

	var lastRes llmCallResult
	for attempt := 0; attempt < 2; attempt++ {
		lastRes = c.CallLLMWithFallback(ctx, messages, 25, 3)
		if lastRes.Offline {
			break
		}

		data, ok := parseJSONOrFallback(lastRes.Content, fallback)
		if ok {
			if lastRes.FallbackWarning != "" {
				data.Warnings = append(data.Warnings, lastRes.FallbackWarning)
			}
			return normalizePayload(data)
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

	return normalizePayload(fallback)
}

func (c *Client) AnswerQuestion(ctx context.Context, question string, chunks []db.ChunkItem, useWeb bool) QAPayload {
	var evidenceLines []string
	for i, chk := range chunks {
		if i >= 8 {
			break
		}
		evidenceLines = append(evidenceLines, fmt.Sprintf("[%d] %s", i+1, chk.Text))
	}
	evidenceText := strings.Join(evidenceLines, "\n\n")

	if c.cfg.GroqAPIKey == "" {
		answer := "Set GROQ_API_KEY to answer questions that require web search or LLM synthesis."
		prov := "ai_synthesized"
		if len(chunks) > 0 {
			first := chunks[0].Text
			if len(first) > 500 {
				first = first[:500]
			}
			answer = fmt.Sprintf("Based on the provided evidence, the closest answer is: %s", first)
			prov = "source_grounded"
		}

		var citations []Citation
		for i, chk := range chunks {
			if i >= 5 {
				break
			}
			snip := chk.Text
			if len(snip) > 220 {
				snip = snip[:220]
			}
			citations = append(citations, Citation{
				Title:      "Evidence",
				Snippet:    snip,
				Provenance: "source_grounded",
			})
		}

		return QAPayload{
			Answer:             answer,
			Provenance:         prov,
			Citations:          citations,
			EvidenceSufficient: len(chunks) > 0,
			Warnings:           []string{"GROQ_API_KEY is not set; returned local fallback answer."},
		}
	}

	mode := "Use only the provided evidence."
	if useWeb {
		mode = "Use the evidence first. If insufficient, use web search."
	}

	systemPrompt := "Return only valid JSON with keys answer, provenance, citations, evidence_sufficient."
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

	prov := "source_grounded"
	if useWeb {
		prov = "web_enriched"
	}

	fallbackQA := QAPayload{
		Answer:             "",
		Provenance:         prov,
		EvidenceSufficient: len(chunks) > 0,
	}

	callRes := c.CallLLMWithFallback(ctx, messages, 15, 2)
	if callRes.Offline {
		if len(chunks) > 0 {
			snip := chunks[0].Text
			if len(snip) > 500 {
				snip = snip[:500]
			}
			fallbackQA.Answer = fmt.Sprintf("Based on the provided evidence, the closest answer is: %s", snip)
			for i, chk := range chunks {
				if i >= 5 {
					break
				}
				s := chk.Text
				if len(s) > 220 {
					s = s[:220]
				}
				fallbackQA.Citations = append(fallbackQA.Citations, Citation{
					Title:      "Evidence",
					Snippet:    s,
					Provenance: "source_grounded",
				})
			}
		}
		fallbackQA.Warnings = []string{callRes.Warning}
		return fallbackQA
	}

	var parsed QAPayload
	if err := json.Unmarshal([]byte(callRes.Content), &parsed); err == nil && parsed.Answer != "" {
		if callRes.FallbackWarning != "" {
			parsed.Warnings = append(parsed.Warnings, callRes.FallbackWarning)
		}
		return parsed
	}

	match := jsonBlockRegex.FindString(callRes.Content)
	if match != "" {
		if err := json.Unmarshal([]byte(match), &parsed); err == nil && parsed.Answer != "" {
			if callRes.FallbackWarning != "" {
				parsed.Warnings = append(parsed.Warnings, callRes.FallbackWarning)
			}
			return parsed
		}
	}

	fallbackQA.Answer = callRes.Content
	return fallbackQA
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
