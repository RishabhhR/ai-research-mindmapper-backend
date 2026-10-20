package ingest

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"ai-research-mindmapper-go/pkg/db"
)

const MaxWebpageBytes = 3 * 1024 * 1024 // 3 MB

func ExtractWebpage(rawURL string) (string, []db.ChunkItem, []string, string, error) {
	if err := AssertPublicHTTPURL(rawURL); err != nil {
		return "", nil, nil, "", err
	}

	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DialContext: DialPublic},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return AssertPublicHTTPURL(req.URL.String())
		},
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, nil, "", err
	}
	req.Header.Set("User-Agent", "AIResearchMindmapper/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", nil, nil, "", errors.New("Could not fetch this webpage. Check the URL and try again")
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, nil, "", fmt.Errorf("URL returned HTTP %d", resp.StatusCode)
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	validType := false
	for _, t := range []string{"text/", "html", "xml", "json"} {
		if strings.Contains(contentType, t) {
			validType = true
			break
		}
	}
	if contentType != "" && !validType {
		return "", nil, nil, "", errors.New("URL returned unsupported content type. Paste an article URL or upload the file directly")
	}

	lr := io.LimitReader(resp.Body, MaxWebpageBytes)
	doc, err := goquery.NewDocumentFromReader(lr)
	if err != nil {
		return "", nil, nil, "", fmt.Errorf("HTML parse error: %w", err)
	}

	// Strip noise elements
	doc.Find("script, style, noscript, svg, header, footer, nav").Remove()

	title := NormalizeText(doc.Find("title").Text())
	if title == "" {
		title = rawURL
	}

	var mainText string
	if mainSel := doc.Find("main"); mainSel.Length() > 0 {
		mainText = mainSel.Text()
	} else if artSel := doc.Find("article"); artSel.Length() > 0 {
		mainText = artSel.Text()
	} else if bodySel := doc.Find("body"); bodySel.Length() > 0 {
		mainText = bodySel.Text()
	} else {
		mainText = doc.Text()
	}

	chunks := ChunkText(mainText, nil)
	return title, chunks, nil, "webpage", nil
}
