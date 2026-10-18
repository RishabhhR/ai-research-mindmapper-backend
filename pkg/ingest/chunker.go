package ingest

import (
	"regexp"
	"strings"

	"ai-research-mindmapper-go/pkg/db"
)

const (
	ChunkSize    = 1800
	ChunkOverlap = 220
)

var whitespaceRegex = regexp.MustCompile(`\s+`)
var annotationRegex = regexp.MustCompile(`\[.*?\]`)

func NormalizeText(text string) string {
	cleaned := whitespaceRegex.ReplaceAllString(text, " ")
	return strings.TrimSpace(cleaned)
}

func ChunkText(text string, page *int) []db.ChunkItem {
	text = NormalizeText(text)
	if text == "" {
		return nil
	}

	runes := []rune(text)
	textLen := len(runes)
	var chunks []db.ChunkItem

	start := 0
	for start < textLen {
		end := start + ChunkSize
		if end > textLen {
			end = textLen
		}
		chunkStr := strings.TrimSpace(string(runes[start:end]))
		if chunkStr != "" {
			chunks = append(chunks, db.ChunkItem{
				Text: chunkStr,
				Page: page,
			})
		}
		if end >= textLen {
			break
		}
		start = end - ChunkOverlap
		if start < 0 {
			start = 0
		}
	}

	return chunks
}

func HasRealContent(text string, minChars int) bool {
	cleaned := annotationRegex.ReplaceAllString(text, "")
	cleaned = NormalizeText(cleaned)
	return len(cleaned) >= minChars
}
