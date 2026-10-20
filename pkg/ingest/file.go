package ingest

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ledongthuc/pdf"

	"ai-research-mindmapper-go/pkg/db"
)

const MaxFileBytes = 8 * 1024 * 1024 // 8 MB

func ExtractTxt(filename string, data []byte) (string, []db.ChunkItem, []string, string, error) {
	if len(data) > MaxFileBytes {
		return "", nil, nil, "", errors.New("file is too large. Keep uploads under 8 MB")
	}
	text := string(data)
	if filename == "" {
		filename = "Uploaded text"
	}
	chunks := ChunkText(text, nil)
	return filename, chunks, nil, "txt", nil
}

func ExtractPDF(filename string, data []byte) (string, []db.ChunkItem, []string, string, error) {
	if len(data) > MaxFileBytes {
		return "", nil, nil, "", errors.New("file is too large. Keep uploads under 8 MB")
	}
	if filename == "" {
		filename = "Uploaded PDF"
	}

	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return filename, nil, []string{fmt.Sprintf("PDF read error: %v", err)}, "pdf", nil
	}

	numPages := reader.NumPage()
	var chunks []db.ChunkItem

	for p := 1; p <= numPages; p++ {
		page := reader.Page(p)
		if page.V.IsNull() {
			continue
		}
		plainText, err := page.GetPlainText(nil)
		if err != nil || plainText == "" {
			continue
		}
		pageIdx := p
		pageChunks := ChunkText(plainText, &pageIdx)
		chunks = append(chunks, pageChunks...)
	}

	return filename, chunks, nil, "pdf", nil
}

func ExtractFile(filename string, data []byte) (string, []db.ChunkItem, []string, string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".txt" || ext == "" {
		return ExtractTxt(filename, data)
	}
	if ext == ".pdf" {
		return ExtractPDF(filename, data)
	}
	return "", nil, nil, "", errors.New("unsupported file type. Only .txt and .pdf uploads are supported")
}
