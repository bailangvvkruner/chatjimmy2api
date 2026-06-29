package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// ── Stats parsing ──

var statsRe = regexp.MustCompile(`(?s)<\|stats\|>(.*?)<\|/stats\|>`)

type ChatStats struct {
	TotalTokens  int     `json:"total_tokens"`
	DecodeTokens int     `json:"decode_tokens"`
	DecodeRate   float64 `json:"decode_rate"`
	TTFT         float64 `json:"ttft"`
	TotalTime    float64 `json:"total_time"`
}

// ExtractStats parses the <|stats|> tag from text and returns the stats.
// Returns nil if no stats tag found or parse error.
func ExtractStats(s string) *ChatStats {
	match := statsRe.FindStringSubmatch(s)
	if match == nil {
		return nil
	}
	var stats ChatStats
	if err := json.Unmarshal([]byte(match[1]), &stats); err != nil {
		return nil
	}
	return &stats
}

// stripStats removes the <|stats|> tag and its content from the response.
func stripStats(s string) string {
	return statsRe.ReplaceAllString(s, "")
}

// ── Stats marker constants ──

var statsMarker = []byte("<|stats|>")

const statsMarkerLen = 9

// findStats returns the index of the <|stats|> marker, or -1 if not found.
func findStats(data []byte) int {
	return bytes.Index(data, statsMarker)
}

// ── Upstream client ──

type UpstreamClient struct {
	baseURL    string
	httpClient *http.Client
	bufPool    sync.Pool
}

func NewUpstreamClient(baseURL string) *UpstreamClient {
	transport := &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
	}

	return &UpstreamClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Minute,
		},
		bufPool: sync.Pool{
			New: func() any {
				buf := make([]byte, 32*1024)
				return &buf
			},
		},
	}
}

// BuildJimmyRequest converts an OpenAI-format request into a chatjimmy.ai request.
func (c *UpstreamClient) BuildJimmyRequest(req *ChatCompletionRequest) *JimmyRequest {
	var systemPrompt string
	messages := make([]ChatMessage, 0, len(req.Messages))

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			if systemPrompt != "" {
				systemPrompt += "\n"
			}
			systemPrompt += msg.contentString()
		} else {
			messages = append(messages, msg)
		}
	}

	model := req.Model
	if model == "" {
		model = "llama3.1-8B"
	}

	return &JimmyRequest{
		Messages: messages,
		ChatOptions: JimmyOptions{
			SelectedModel: model,
			SystemPrompt:  systemPrompt,
			TopK:          8,
		},
		Attachment: nil,
	}
}

// BuildJimmyRequestFromMessages builds a JimmyRequest from pre-processed messages.
func (c *UpstreamClient) BuildJimmyRequestFromMessages(messages []ChatMessage, model string) *JimmyRequest {
	var systemPrompt string
	chatMsgs := make([]ChatMessage, 0, len(messages))

	for _, msg := range messages {
		if msg.Role == "system" {
			if systemPrompt != "" {
				systemPrompt += "\n"
			}
			systemPrompt += msg.contentString()
		} else {
			chatMsgs = append(chatMsgs, msg)
		}
	}

	if model == "" {
		model = "llama3.1-8B"
	}

	return &JimmyRequest{
		Messages: chatMsgs,
		ChatOptions: JimmyOptions{
			SelectedModel: model,
			SystemPrompt:  systemPrompt,
			TopK:          8,
		},
		Attachment: nil,
	}
}

// DoRequest sends a request to the chatjimmy.ai API and returns the response body stream.
// The caller must close the returned io.ReadCloser.
func (c *UpstreamClient) DoRequest(jimmyReq *JimmyRequest) (io.ReadCloser, error) {
	body, err := json.Marshal(jimmyReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Origin", "https://chatjimmy.ai")
	req.Header.Set("Referer", "https://chatjimmy.ai/")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("upstream HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return resp.Body, nil
}

// ── Streaming reader ──

// StreamReader reads from the upstream response body and emits text chunks,
// automatically detecting and stripping the <|stats|> footer marker.
// After ReadChunk returns done=true, call ExtractStats() to get token stats.
type StreamReader struct {
	reader    io.Reader
	buf       []byte
	remainder []byte
	done      bool
	rawStats  []byte // captured JSON from <|stats|> tag
}

// NewStreamReader wraps an upstream response body for chunked reading.
func NewStreamReader(r io.Reader) *StreamReader {
	return &StreamReader{
		reader:    r,
		buf:       make([]byte, 32*1024),
		remainder: make([]byte, 0, 4096),
	}
}

// ReadChunk reads the next text chunk from the stream.
// Returns the chunk (may be nil), a bool indicating if the stream is complete,
// and any error encountered.
func (sr *StreamReader) ReadChunk() ([]byte, bool, error) {
	if sr.done {
		return nil, true, nil
	}

	for {
		n, err := sr.reader.Read(sr.buf)
		if n > 0 {
			sr.remainder = append(sr.remainder, sr.buf[:n]...)

			if idx := findStats(sr.remainder); idx >= 0 {
				var chunk []byte
				if idx > 0 {
					chunk = make([]byte, idx)
					copy(chunk, sr.remainder[:idx])
				}
				// Capture stats JSON
				sr.captureStats(sr.remainder[idx:])
				sr.done = true
				return chunk, true, nil
			}

			cut := len(sr.remainder)
			keep := statsMarkerLen - 1
			if cut > keep {
				chunk := make([]byte, cut-keep)
				copy(chunk, sr.remainder[:cut-keep])
				sr.remainder = copyBuf(sr.remainder[cut-keep:])
				return chunk, false, nil
			}
		}

		if err != nil {
			if err == io.EOF {
				sr.done = true
				if len(sr.remainder) > 0 {
					chunk := make([]byte, len(sr.remainder))
					copy(chunk, sr.remainder)
					sr.remainder = sr.remainder[:0]
					return chunk, true, nil
				}
				return nil, true, nil
			}
			return nil, false, err
		}
	}
}

// captureStats extracts the stats JSON from a buffer starting at <|stats|>.
func (sr *StreamReader) captureStats(data []byte) {
	match := statsRe.FindSubmatch(data)
	if match != nil {
		sr.rawStats = make([]byte, len(match[1]))
		copy(sr.rawStats, match[1])
	}
}

// ExtractStats returns the parsed token stats captured during streaming.
// Only valid after ReadChunk returns done=true.
func (sr *StreamReader) ExtractStats() *ChatStats {
	if len(sr.rawStats) == 0 {
		return nil
	}
	var stats ChatStats
	if err := json.Unmarshal(sr.rawStats, &stats); err != nil {
		return nil
	}
	return &stats
}

func copyBuf(src []byte) []byte {
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}
