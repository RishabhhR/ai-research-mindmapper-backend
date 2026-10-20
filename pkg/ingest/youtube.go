package ingest

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"regexp"
	"strings"
	"time"

	"ai-research-mindmapper-go/pkg/config"
	"ai-research-mindmapper-go/pkg/db"
)

const WhisperMaxBytes = 24 * 1024 * 1024 // 24 MB
var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func IsYouTubeURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be" || host == "www.youtu.be"
}

func YouTubeVideoID(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if !IsYouTubeURL(rawURL) {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	var id string
	if host == "youtu.be" || host == "www.youtu.be" {
		id = strings.Split(strings.Trim(parsed.Path, "/"), "/")[0]
	} else if strings.HasPrefix(parsed.Path, "/shorts/") || strings.HasPrefix(parsed.Path, "/live/") || strings.HasPrefix(parsed.Path, "/embed/") {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) > 1 {
			id = parts[1]
		}
	} else {
		id = parsed.Query().Get("v")
	}
	if !videoIDPattern.MatchString(id) {
		return ""
	}
	return id
}

func YouTubeOEmbedTitle(ctx context.Context, rawURL string) string {
	client := &http.Client{Timeout: 6 * time.Second}
	reqURL := fmt.Sprintf("https://www.youtube.com/oembed?url=%s&format=json", url.QueryEscape(rawURL))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
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

func FetchViaSupadata(ctx context.Context, rawURL, apiKey string) ([]db.ChunkItem, string) {
	if apiKey == "" {
		return nil, "SUPADATA_API_KEY not set."
	}

	client := &http.Client{Timeout: 15 * time.Second}
	reqURL := fmt.Sprintf("https://api.supadata.ai/v1/transcript?url=%s&text=true&lang=en", url.QueryEscape(rawURL))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
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
			select {
			case <-ctx.Done():
				return nil, "Transcript processing timed out. Please try again."
			case <-time.After(2 * time.Second):
			}
			pollURL := fmt.Sprintf("https://api.supadata.ai/v1/transcript/%s", jobResp.JobID)
			pReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
			if err != nil {
				return nil, "Could not poll transcript processing."
			}
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

func FetchViaEdge(ctx context.Context, videoID, edgeURL string) ([]db.ChunkItem, string) {
	if edgeURL == "" {
		return nil, "EDGE_TRANSCRIPT_URL not configured."
	}

	client := &http.Client{Timeout: 12 * time.Second}
	reqURL := fmt.Sprintf("%s?v=%s", edgeURL, url.QueryEscape(videoID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
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

func DownloadAudioYtDlp(ctx context.Context, rawURL, videoID, cookiesB64 string) (string, error) {
	ytDlpPath, err := exec.LookPath("yt-dlp")
	if err != nil {
		return "", errors.New("yt-dlp is not installed")
	}

	prefix := fmt.Sprintf("/tmp/yt_audio_%s_%d", videoID, time.Now().UnixNano())
	audioPattern := prefix + ".%(ext)s"
	args := []string{
		"-f", "worstaudio[ext=m4a]/worstaudio[ext=webm]/bestaudio[ext=m4a]/bestaudio[ext=webm]/bestaudio",
		"-o", audioPattern,
		"--quiet",
		"--no-warnings",
		"--no-playlist",
		"--max-filesize", fmt.Sprintf("%d", WhisperMaxBytes),
		"--socket-timeout", "4",
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	}
	if cookiesB64 != "" {
		cookies, err := base64.StdEncoding.DecodeString(cookiesB64)
		if err != nil {
			return "", errors.New("YOUTUBE_COOKIES_B64 is invalid")
		}
		cookieFile, err := os.CreateTemp("/tmp", "yt-cookies-*.txt")
		if err != nil {
			return "", errors.New("could not prepare YouTube cookies")
		}
		defer os.Remove(cookieFile.Name())
		if _, err := cookieFile.Write(cookies); err != nil {
			cookieFile.Close()
			return "", errors.New("could not write YouTube cookies")
		}
		if err := cookieFile.Close(); err != nil {
			return "", errors.New("could not close YouTube cookies")
		}
		args = append(args, "--cookies", cookieFile.Name())
	}
	args = append(args, rawURL)
	cmd := exec.CommandContext(ctx, ytDlpPath, args...)

	if err := cmd.Run(); err != nil {
		matches, _ := filepath.Glob(prefix + ".*")
		for _, m := range matches {
			_ = os.Remove(m)
		}
		return "", fmt.Errorf("yt-dlp execution failed: %w", err)
	}

	matches, _ := filepath.Glob(prefix + ".*")
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

func ExtractYouTube(ctx context.Context, cfg *config.Config, rawURL string) (string, []db.ChunkItem, []string, string, error) {
	videoID := YouTubeVideoID(rawURL)
	if videoID == "" {
		return "", nil, nil, "", errors.New("could not find a YouTube video id in the URL")
	}

	title := YouTubeOEmbedTitle(ctx, rawURL)
	if title == "" {
		title = fmt.Sprintf("YouTube video %s", videoID)
	}

	// 1. Supadata transcript API
	chunks, supaWarn := FetchViaSupadata(ctx, rawURL, cfg.SupadataAPIKey)
	if len(chunks) > 0 {
		return title, chunks, nil, "youtube", nil
	}

	// 2. Vercel edge proxy
	if ctx.Err() != nil {
		return title, nil, []string{"Transcript processing timed out."}, "youtube", nil
	}
	chunks, edgeWarn := FetchViaEdge(ctx, videoID, cfg.EdgeTranscriptURL)
	if len(chunks) > 0 {
		return title, chunks, nil, "youtube", nil
	}

	// 3. yt-dlp + Groq Whisper
	var whisperWarn string
	if ctx.Err() != nil {
		return title, nil, []string{"Transcript processing timed out."}, "youtube", nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.YouTubePipelineTimeout)*time.Second)
	defer cancel()

	audioPath, err := DownloadAudioYtDlp(ctx, rawURL, videoID, cfg.YouTubeCookiesB64)
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
