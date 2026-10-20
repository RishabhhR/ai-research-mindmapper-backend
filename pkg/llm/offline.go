package llm

import (
	"fmt"
	"strings"

	"ai-research-mindmapper-go/pkg/db"
)

// OfflineGeneration is an extractive fallback. It never invents findings.
func OfflineGeneration(topic string, evidenceChunks []db.ChunkItem, _ string) ResearchPayload {
	if len(evidenceChunks) == 0 {
		return ResearchPayload{
			Degraded:   true,
			Summary:    "Research models are unavailable. Please try again later.",
			Warnings:   []string{"Research models are unavailable."},
			Provenance: "ai_synthesized",
		}
	}

	positions := [][2]float64{{8, 11}, {70, 12}, {8, 58}, {70, 58}, {39, 78}, {39, 5}}
	nodes := make([]Node, 0, 6)
	for _, chunk := range evidenceChunks {
		if len(nodes) >= len(positions) {
			break
		}
		body := strings.TrimSpace(chunk.Text)
		if body == "" {
			continue
		}
		if len(body) > 240 {
			body = body[:240] + "…"
		}
		title := chunk.Title
		if title == "" {
			title = fmt.Sprintf("Evidence excerpt %d", len(nodes)+1)
		}
		pos := positions[len(nodes)]
		nodes = append(nodes, Node{Title: title, Description: body, X: pos[0], Y: pos[1], Provenance: "source_grounded"})
	}

	return ResearchPayload{
		Degraded:   true,
		Summary:    fmt.Sprintf("AI synthesis is unavailable. Showing %d excerpts from the sources for %s.", len(nodes), topic),
		Nodes:      nodes,
		Sources:    groundedSources(evidenceChunks),
		Citations:  groundedCitations(evidenceChunks, 6),
		Warnings:   []string{"AI synthesis is unavailable; showing source excerpts only."},
		Provenance: "source_grounded",
	}
}
