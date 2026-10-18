package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ai-research-mindmapper-go/pkg/config"
	"ai-research-mindmapper-go/pkg/db"
)

const WhisperMaxBytes = 24 * 1024 * 1024 // 24 MB

func IsYouTubeURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Host)
	return strings.Contains(host, "youtube.com") || strings.Contains(host, "youtu.be")
}

func YouTubeVideoID(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Host)
	if strings.Contains(host, "youtu.be") {
		return strings.Trim(parsed.Path, "/")
	}
	return parsed.Query().Get("v")
}

func YouTubeOEmbedTitle(rawURL string) string {
	client := &http.Client{Timeout: 6 * time.Second}
	reqURL := fmt.Sprintf("https://www.youtube.com/oembed?url=%s&format=json", url.QueryEscape(rawURL))
	resp, err := client.Get(reqURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()

	var data struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
		return data.Title
	}
	return ""
}

func FetchViaSupadata(rawURL, apiKey string) ([]db.ChunkItem, string) {
	if apiKey == "" {
		return nil, "SUPADATA_API_KEY not set."
	}

	client := &http.Client{Timeout: 15 * time.Second}
	reqURL := fmt.Sprintf("https://api.supadata.ai/v1/transcript?url=%s&text=true&lang=en", url.QueryEscape(rawURL))
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Sprintf("Supadata request creation failed: %v", err)
	}
	req.Header.Set("x-api-key", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Sprintf("Supadata request failed: %v", err)
	}
	defer resp.Body.Close()

	// 202 Async job
	if resp.StatusCode == http.StatusAccepted {
		var jobResp struct {
			JobID string `json:"jobId"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&jobResp); err != nil || jobResp.JobID == "" {
			return nil, "Supadata returned 202 but no jobId."
		}

		pollClient := &http.Client{Timeout: 10 * time.Second}
		for i := 0; i < 25; i++ {
			time.Sleep(2 * time.Second)
			pollURL := fmt.Sprintf("https://api.supadata.ai/v1/transcript/%s", jobResp.JobID)
			pReq, _ := http.NewRequest(http.MethodGet, pollURL, nil)
			pReq.Header.Set("x-api-key", apiKey)

			pResp, err := pollClient.Do(pReq)
			if err != nil {
				continue
			}
			var pData struct {
				Status  string `json:"status"`
				Content any    `json:"content"`
				Error   string `json:"error"`
			}
			_ = json.NewDecoder(pResp.Body).Decode(&pData)
			pResp.Body.Close()

			if pData.Status == "completed" {
				if s, ok := pData.Content.(string); ok && len(s) > 80 {
					return ChunkText(s, nil), ""
				}
				return nil, "Supadata job completed but returned empty content."
			}
			if pData.Status == "failed" {
				return nil, fmt.Sprintf("Supadata async job failed: %s", pData.Error)
			}
		}
		return nil, "Supadata async job timed out after 50 s."
	}

	if resp.StatusCode != http.StatusOK {
		var errData struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errData)
		msg := errData.Message
		if msg == "" {
			msg = errData.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Sprintf("Supadata error: %s", msg)
	}

	var data struct {
		Content any `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, "Supadata returned non-JSON response."
	}

	if s, ok := data.Content.(string); ok && len(s) > 80 {
		return ChunkText(s, nil), ""
	}

	return nil, "Supadata returned an empty transcript."
}

func FetchViaEdge(videoID, edgeURL string) ([]db.ChunkItem, string) {
	if edgeURL == "" {
		return nil, "EDGE_TRANSCRIPT_URL not configured."
	}

	client := &http.Client{Timeout: 12 * time.Second}
	reqURL := fmt.Sprintf("%s?v=%s", edgeURL, url.QueryEscape(videoID))
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Sprintf("Edge request build failed: %v", err)
	}
	req.Header.Set("User-Agent", "AIResearchMindmapper/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Sprintf("Edge function unreachable: %v", err)
	}
	defer resp.Body.Close()

	var data struct {
		Success bool   `json:"success"`
		Text    string `json:"text"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
		if resp.StatusCode == http.StatusOK && data.Success && data.Text != "" {
			return ChunkText(data.Text, nil), ""
		}
		if data.Error != "" {
			return nil, data.Error
		}
	}
	return nil, fmt.Sprintf("Edge function returned HTTP %d", resp.StatusCode)
}

func DownloadAudioYtDlp(ctx context.Context, rawURL, videoID string) (string, error) {
	ytDlpPath, err := exec.LookPath("yt-dlp")
	if err != nil {
		return "", errors.New("yt-dlp is not installed")
	}

	audioPattern := fmt.Sprintf("/tmp/yt_audio_%s_%d.%%(ext)s", videoID, time.Now().Unix())
	cmd := exec.CommandContext(ctx, ytDlpPath,
		"-f", "worstaudio[ext=m4a]/worstaudio[ext=webm]/bestaudio[ext=m4a]/bestaudio[ext=webm]/bestaudio",
		"-o", audioPattern,
		"--quiet",
		"--no-warnings",
		"--no-playlist",
		"--max-filesize", fmt.Sprintf("%d", WhisperMaxBytes),
		"--socket-timeout", "4",
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		rawURL,
	)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("yt-dlp execution failed: %w", err)
	}

	matches, _ := filepath.Glob(fmt.Sprintf("/tmp/yt_audio_%s_*", videoID))
	for _, m := range matches {
		ext := strings.ToLower(filepath.Ext(m))
		if ext == ".m4a" || ext == ".webm" || ext == ".mp3" || ext == ".ogg" || ext == ".opus" {
			return m, nil
		}
	}

	return "", errors.New("yt-dlp finished but audio file not found in /tmp")
}

func TranscribeWithGroqWhisper(ctx context.Context, audioPath, groqAPIKey string) (string, error) {
	if groqAPIKey == "" {
		return "", errors.New("GROQ_API_KEY not set")
	}

	fileInfo, err := os.Stat(audioPath)
	if err != nil {
		return "", err
	}
	if fileInfo.Size() > WhisperMaxBytes {
		return "", errors.New("audio file exceeds Groq Whisper 25 MB limit")
	}

	file, err := os.Open(audioPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}
	_ = writer.WriteField("model", "whisper-large-v3")
	_ = writer.WriteField("response_format", "text")
	_ = writer.WriteField("language", "en")
	_ = writer.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.groq.com/openai/v1/audio/transcriptions", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+groqAPIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("groq whisper HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	return strings.TrimSpace(string(respBytes)), nil
}

func ExtractYouTube(cfg *config.Config, rawURL string) (string, []db.ChunkItem, []string, string, error) {
	videoID := YouTubeVideoID(rawURL)
	if videoID == "" {
		return "", nil, nil, "", errors.New("could not find a YouTube video id in the URL")
	}

	title := YouTubeOEmbedTitle(rawURL)
	if title == "" {
		title = fmt.Sprintf("YouTube video %s", videoID)
	}

	// 1. Supadata transcript API
	chunks, supaWarn := FetchViaSupadata(rawURL, cfg.SupadataAPIKey)
	if len(chunks) > 0 {
		return title, chunks, nil, "youtube", nil
	}

	// 2. Vercel edge proxy
	chunks, edgeWarn := FetchViaEdge(videoID, cfg.EdgeTranscriptURL)
	if len(chunks) > 0 {
		return title, chunks, nil, "youtube", nil
	}

	// 3. yt-dlp + Groq Whisper
	var whisperWarn string
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.YouTubePipelineTimeout)*time.Second)
	defer cancel()

	audioPath, err := DownloadAudioYtDlp(ctx, rawURL, videoID)
	if err == nil && audioPath != "" {
		defer os.Remove(audioPath)
		transcript, tErr := TranscribeWithGroqWhisper(ctx, audioPath, cfg.GroqAPIKey)
		if tErr == nil && HasRealContent(transcript, 300) {
			return title, ChunkText(transcript, nil), nil, "youtube", nil
		}
		if tErr != nil {
			whisperWarn = tErr.Error()
		}
	} else if err != nil {
		whisperWarn = err.Error()
	}

	var warnings []string
	if supaWarn != "" {
		warnings = append(warnings, supaWarn)
	}
	if edgeWarn != "" {
		warnings = append(warnings, edgeWarn)
	}
	if whisperWarn != "" {
		warnings = append(warnings, fmt.Sprintf("Audio pipeline error: %s", whisperWarn))
	}
	if len(warnings) == 0 {
		warnings = append(warnings, "Could not extract YouTube transcript. Paste the transcript as a .txt file.")
	}

	return title, nil, warnings, "youtube", nil
}
