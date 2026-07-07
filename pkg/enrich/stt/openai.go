package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/mxpv/podsync/pkg/feed"
)

// openAI transcribes audio via an OpenAI-compatible
// /v1/audio/transcriptions endpoint (OpenAI, Groq, local whisper servers).
type openAI struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func newOpenAI(cfg *feed.STTProviderConfig, timeout time.Duration) (Provider, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("base_url is required")
	}
	if cfg.Model == "" {
		return nil, errors.New("model is required")
	}
	return &openAI{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

func (p *openAI) Name() string { return "openai" }

func (p *openAI) Transcribe(ctx context.Context, mediaPath, lang, outPath string) error {
	file, err := os.Open(mediaPath)
	if err != nil {
		return errors.Wrap(err, "failed to open media file")
	}

	fields := map[string]string{
		"model":           p.model,
		"response_format": "vtt",
	}
	if lang != "" {
		fields["language"] = lang
	}

	bodyReader, bodyWriter := io.Pipe()
	writer := multipart.NewWriter(bodyWriter)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/audio/transcriptions", bodyReader)
	if err != nil {
		file.Close()
		bodyReader.Close()
		bodyWriter.Close()
		return errors.Wrap(err, "failed to create request")
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	writeErr := make(chan error, 1)
	go func() {
		defer file.Close()
		err := writeMultipart(writer, file, filepath.Base(mediaPath), fields)
		if err != nil {
			bodyWriter.CloseWithError(err)
			writeErr <- err
			return
		}
		writeErr <- bodyWriter.Close()
	}()

	resp, err := p.client.Do(req)
	if err != nil {
		bodyReader.Close()
		<-writeErr
		return errors.Wrap(err, "transcription request failed")
	}
	defer resp.Body.Close()
	if err := <-writeErr; err != nil {
		return errors.Wrap(err, "failed to stream transcription request")
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
	if err != nil {
		return errors.Wrap(err, "failed to read transcription response")
	}

	if resp.StatusCode != http.StatusOK {
		return errors.Errorf("transcription API returned %d: %s", resp.StatusCode, truncate(string(data), 512))
	}

	vtt := string(data)
	if !strings.HasPrefix(strings.TrimSpace(vtt), "WEBVTT") {
		// Some servers ignore response_format and reply with JSON.
		converted, err := vttFromJSONResponse(data)
		if err != nil {
			return errors.Wrap(err, "response is neither VTT nor a known JSON shape")
		}
		vtt = converted
	}

	return os.WriteFile(outPath, []byte(vtt), 0o644) //nolint:gosec // served publicly anyway
}

func writeMultipart(writer *multipart.Writer, file *os.File, filename string, fields map[string]string) error {
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return errors.Wrap(err, "failed to create multipart file")
	}
	if _, err := io.Copy(part, file); err != nil {
		return errors.Wrap(err, "failed to read media file")
	}

	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return errors.Wrap(err, "failed to write multipart field")
		}
	}
	if err := writer.Close(); err != nil {
		return errors.Wrap(err, "failed to finalize multipart body")
	}
	return nil
}

// vttFromJSONResponse synthesizes a VTT file from an OpenAI JSON or
// verbose_json transcription response.
func vttFromJSONResponse(data []byte) (string, error) {
	var parsed struct {
		Text     string `json:"text"`
		Segments []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("WEBVTT\n\n")

	if len(parsed.Segments) > 0 {
		for _, segment := range parsed.Segments {
			text := strings.TrimSpace(segment.Text)
			if text == "" {
				continue
			}
			fmt.Fprintf(&b, "%s --> %s\n%s\n\n", vttTimestamp(segment.Start), vttTimestamp(segment.End), text)
		}
		return b.String(), nil
	}

	text := strings.TrimSpace(parsed.Text)
	if text == "" {
		return "", errors.New("empty transcription response")
	}
	fmt.Fprintf(&b, "%s --> %s\n%s\n", vttTimestamp(0), vttTimestamp(0), text)
	return b.String(), nil
}

func vttTimestamp(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	return fmt.Sprintf("%02d:%02d:%02d.%03d",
		int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60, d.Milliseconds()%1000)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
