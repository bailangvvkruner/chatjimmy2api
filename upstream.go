package main

import (
	"bytes"
	"context"
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

const (
	// maxRequestBodySize is the maximum upstream JSON body size in bytes.
	// The upstream nginx has client_max_body_size ~1MB (1,048,576 bytes).
	// Requests exceeding this return HTTP 413. We truncate at 768KB to stay
	// well below the limit with room for system prompt and headers.
	maxRequestBodySize = 768 * 1024 // 768KB (nginx limit is ~1MB)
)

type UpstreamClient struct {
	baseURL    string
	httpClient *http.Client
	bufPool    sync.Pool
	limiter    *ContextLimiter
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
		limiter: NewContextLimiter(),
	}
}

// Limiter returns the context limiter used for token tracking and truncation.
func (c *UpstreamClient) Limiter() *ContextLimiter {
	return c.limiter
}

// BuildJimmyRequest converts an OpenAI-format request into a chatjimmy.ai request.
// All message content is normalized to plain text strings since the upstream
// chatjimmy.ai does not support content arrays (multi-modal format).
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
			// Normalize content: convert content arrays to plain text
			contentStr := msg.contentString()
			msg.Content = contentPtr(contentStr)
			messages = append(messages, msg)
		}
	}

	// Truncate messages if the total body exceeds the upstream limit
	maxSize := maxRequestBodySize
	if maxSize > 0 {
		overhead := 512 + len(systemPrompt)
		estimate := estimateBodySize(messages)
		truncated := truncateMessages(messages, maxSize, overhead)
		if len(truncated) < len(messages) {
			logDebug("truncated msgs=%d→%d estimate=%d overhead=%d max=%d", len(messages), len(truncated), estimate, overhead, maxSize)
			messages = truncated
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
// All message content is normalized to plain text strings since the upstream
// chatjimmy.ai does not support content arrays (multi-modal format).
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
			// Normalize content: convert content arrays to plain text
			contentStr := msg.contentString()
			msg.Content = contentPtr(contentStr)
			chatMsgs = append(chatMsgs, msg)
		}
	}

	// Phase 1: Truncate by token limit (model context window)
	truncated, _ := c.limiter.TruncateByTokens(chatMsgs, model)
	if len(truncated) < len(chatMsgs) {
		logDebug("ctxlimiter truncated msgs=%d→%d model=%s", len(chatMsgs), len(truncated), model)
		chatMsgs = truncated
	}

	// Phase 2: Truncate by body size (nginx limit ~1MB, we use 768KB)
	maxSize := maxRequestBodySize
	if maxSize > 0 {
		overhead := 512 + len(systemPrompt)
		estimate := estimateBodySize(chatMsgs)
		truncated := truncateMessages(chatMsgs, maxSize, overhead)
		if len(truncated) < len(chatMsgs) {
			logDebug("body truncation msgs=%d→%d estimate=%d overhead=%d max=%d", len(chatMsgs), len(truncated), estimate, overhead, maxSize)
			chatMsgs = truncated
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
func (c *UpstreamClient) DoRequest(ctx context.Context, jimmyReq *JimmyRequest) (io.ReadCloser, error) {
	body, err := json.Marshal(jimmyReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	logDebug("upstream body_size=%d msgs=%d sysprompt_len=%d body_preview=%q", len(body), len(jimmyReq.Messages), len(jimmyReq.ChatOptions.SystemPrompt), string(body[:min(len(body), 120)]))

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Randomize all fingerprintable headers (UA, IP headers, accept-language,
	// sec-ch-ua, etc.) so each request looks like a different browser/client.
	setRandomHeaders(req)
	req.Header.Set("Content-Type", "application/json")
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

	logDebug("upstream status=%d content-type=%q content-length=%s", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Length"))

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
				// Capture stats JSON (always, even when idx==0)
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

// StatsJSON returns the raw stats JSON string, or empty if none captured.
func (sr *StreamReader) StatsJSON() string {
	if len(sr.rawStats) == 0 {
		return ""
	}
	return string(sr.rawStats)
}

func copyBuf(src []byte) []byte {
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

// truncateMessages removes older messages until the estimated JSON body size
// is under maxSize. It always keeps the first and last message.
// overhead is extra bytes for JSON framing, system prompt, etc.
func truncateMessages(msgs []ChatMessage, maxSize int, overhead int) []ChatMessage {
	if len(msgs) <= 2 {
		return msgs
	}
	estimate := estimateBodySize(msgs)
	if estimate+overhead <= maxSize {
		return msgs
	}
	// Drop from the middle (index 1), keep first and last
	droppable := len(msgs) - 2 // messages we can drop (not first, not last)
	toDrop := 1
	for estimate+overhead > maxSize && droppable > 0 {
		idx := 1 + (toDrop-1)%droppable
		msgs = append(msgs[:idx], msgs[idx+1:]...)
		droppable--
		estimate = estimateBodySize(msgs)
	}
	return msgs
}

// estimateBodySize estimates the JSON body size of a []ChatMessage in bytes.
func estimateBodySize(msgs []ChatMessage) int {
	size := 2 // [...]
	for _, m := range msgs {
		size += 20 // {"role":"x","content":"..."}, commas, etc.
		size += len(m.Role)
		size += len(m.Content)
		if len(m.Name) > 0 {
			size += 10 + len(m.Name) // "name":"..."
		}
		if len(m.ToolCalls) > 0 {
			size += 50 // rough tool call overhead
		}
		if m.ToolCallID != "" {
			size += 20 + len(m.ToolCallID) // "tool_call_id":"..."
		}
	}
	return size
}
