package llm

import (
	"fmt"
	"sort"

	"ai-research-mindmapper-go/pkg/db"
)

func OfflineGeneration(topic string, evidenceChunks []db.ChunkItem, provenance string) ResearchPayload {
	if provenance == "" {
		provenance = "ai_synthesized"
	}

	summary := fmt.Sprintf("Research map for %s. Set GROQ_API_KEY to enable Groq Compound search and higher quality synthesis.", topic)
	if len(evidenceChunks) > 0 {
		summary = fmt.Sprintf("Evidence-backed research map for %s using %d local evidence chunks.", topic, len(evidenceChunks))
	}

	// Distinct source titles up to 4
	titleMap := make(map[string]bool)
	var titles []string
	for range evidenceChunks {
		t := "Provided evidence"
		if !titleMap[t] {
			titleMap[t] = true
			titles = append(titles, t)
		}
	}
	sort.Strings(titles)
	if len(titles) > 4 {
		titles = titles[:4]
	}

	evidenceProv := provenance
	if len(evidenceChunks) > 0 {
		evidenceProv = "source_grounded"
	}

	nodes := []Node{
		{Title: "Context", Description: "Frame the core question and constraints.", X: 8, Y: 11, Provenance: provenance},
		{Title: "Evidence", Description: "Use provided material before web expansion.", X: 70, Y: 12, Provenance: evidenceProv},
		{Title: "Patterns", Description: "Cluster recurring concepts and claims.", X: 8, Y: 58, Provenance: provenance},
		{Title: "Trade-offs", Description: "Separate tensions, risks, and open questions.", X: 70, Y: 58, Provenance: provenance},
		{Title: "Next research", Description: "Identify gaps and follow-up searches.", X: 39, Y: 78, Provenance: provenance},
	}

	insights := []Insight{
		{Title: "Evidence-first workflow", Body: "Local sources should answer questions before web search is used.", Provenance: evidenceProv},
		{Title: "Traceable synthesis", Body: "Generated claims are labeled separately from source-backed material.", Provenance: "ai_synthesized"},
		{Title: "Research gaps stay visible", Body: "The map keeps unanswered areas available for follow-up questions.", Provenance: provenance},
	}

	var sources []Source
	for _, title := range titles {
		sources = append(sources, Source{
			Title:      title,
			Type:       "Provided evidence",
			Confidence: "High",
			Body:       "Stored local evidence source.",
			Provenance: "source_grounded",
			URL:        "",
		})
	}

	tradeoffs := []Tradeoff{
		{Title: "Coverage vs. cost", Body: "The system limits token usage by searching local evidence before web search.", Provenance: "ai_synthesized"},
		{Title: "Speed vs. nuance", Body: "Fast summaries need citations and provenance labels to remain trustworthy.", Provenance: "ai_synthesized"},
	}

	var citations []Citation
	for i, chunk := range evidenceChunks {
		if i >= 6 {
			break
		}
		snippet := chunk.Text
		if len(snippet) > 220 {
			snippet = snippet[:220]
		}
		citations = append(citations, Citation{
			Title:      "Evidence",
			URL:        "",
			Snippet:    snippet,
			Provenance: "source_grounded",
		})
	}

	return ResearchPayload{
		Summary:    summary,
		Nodes:      nodes,
		Insights:   insights,
		Sources:    sources,
		Tradeoffs:  tradeoffs,
		Citations:  citations,
		Warnings:   []string{"GROQ_API_KEY is not set; returned local fallback output."},
		Provenance: provenance,
	}
}
