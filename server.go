package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	upstream *UpstreamClient
	apiKey   string
	started  time.Time
	models   []Model
}

func NewServer(upstream *UpstreamClient, apiKey string, modelIDs []string) *Server {
	models := make([]Model, len(modelIDs))
	for i, id := range modelIDs {
		models[i] = Model{
			ID:      id,
			Object:  "model",
			Created: 1700000000,
			OwnedBy: "chatjimmy",
		}
	}
	return &Server{
		upstream: upstream,
		apiKey:   apiKey,
		started:  time.Now(),
		models:   models,
	}
}

// ── Middleware ──

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey != "" {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.apiKey {
				writeJSON(w, http.StatusUnauthorized, APIError{
					Error: APIErrorDetail{
						Message: "Invalid API key",
						Type:    "authentication_error",
						Code:    "invalid_api_key",
					},
				})
				return
			}
		}
		next(w, r)
	}
}

func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// ── Router ──

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}

	switch {
	case path == "/" || path == "/health":
		cors(s.handleHealth).ServeHTTP(w, r)

	case path == "/v1/models" && r.Method == http.MethodGet:
		s.auth(cors(s.handleModels)).ServeHTTP(w, r)

	case path == "/v1/chat/completions" && r.Method == http.MethodPost:
		s.auth(cors(s.handleChatCompletions)).ServeHTTP(w, r)

	case path == "/v1/admin/logs" && r.Method == http.MethodGet:
		s.auth(cors(s.handleAdminLogs)).ServeHTTP(w, r)

	default:
		s.auth(cors(s.notFound)).ServeHTTP(w, r)
	}
}

// ── Handlers ──

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"uptime":  time.Since(s.started).String(),
		"version": "0.1.0",
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ModelsResponse{
		Object: "list",
		Data:   s.models,
	})
}

// handleAdminLogs returns the in-memory log ring buffer as plain text.
// Requires the same API key auth as other endpoints.
func (s *Server) handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	lines := debugLog.Lines()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, APIError{
		Error: APIErrorDetail{
			Message: fmt.Sprintf("Not found: %s %s", r.Method, r.URL.Path),
			Type:    "not_found",
		},
	})
}

// ── Chat completions ──

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logDebug("req decode error: %v", err)
		writeJSON(w, http.StatusBadRequest, APIError{
			Error: APIErrorDetail{
				Message: fmt.Sprintf("Invalid JSON: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	if len(req.Messages) == 0 {
		logDebug("req no messages")
		writeJSON(w, http.StatusBadRequest, APIError{
			Error: APIErrorDetail{
				Message: "messages is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	model := req.Model
	if model == "" {
		model = "llama3.1-8B"
	}

	logDebug("req model=%s msgs=%d stream=%v tools=%d", model, len(req.Messages), req.Stream, len(req.Tools))
	for i, m := range req.Messages {
		cStr := m.contentString()
		preview := cStr
		if len(preview) > 60 {
			preview = preview[:60] + "..."
		}
		logDebug("  msg[%d] role=%s content_len=%d preview=%q", i, m.Role, len(m.Content), preview)
	}

	messages := req.Messages

	// ── Tool handling ──
	hasTools := len(req.Tools) > 0
	if hasTools {
		if choice, ok := req.ToolChoice.(string); ok && choice == "none" {
			hasTools = false
		}
		if toolPrompt := BuildToolSystemPrompt(req.Tools, req.ToolChoice); toolPrompt != "" {
			messages = injectSystemMessage(messages, toolPrompt)
		}
	}

	jimmyReq := s.upstream.BuildJimmyRequestFromMessages(
		withToolResultsFormatted(messages), model,
	)

	logDebug("upstream req model=%s msgs=%d sysprompt_len=%d", jimmyReq.ChatOptions.SelectedModel, len(jimmyReq.Messages), len(jimmyReq.ChatOptions.SystemPrompt))

	if req.Stream {
		s.streamChatCompletion(w, r, jimmyReq, model, hasTools)
	} else {
		s.nonStreamChatCompletion(w, r, jimmyReq, model, hasTools)
	}
}

// injectSystemMessage prepends or appends to an existing system message.
func injectSystemMessage(msgs []ChatMessage, content string) []ChatMessage {
	for i, m := range msgs {
		if m.Role == "system" {
			existing := m.contentString()
			msgs[i].Content = contentPtr(existing + "\n\n" + content)
			return msgs
		}
	}
	result := make([]ChatMessage, 0, len(msgs)+1)
	result = append(result, ChatMessage{Role: "system", Content: contentPtr(content)})
	result = append(result, msgs...)
	return result
}

// withToolResultsFormatted converts tool call/result messages into a format
// the chatjimmy.ai text-only model can understand.
func withToolResultsFormatted(msgs []ChatMessage) []ChatMessage {
	result := make([]ChatMessage, len(msgs))
	copy(result, msgs)

	for i, m := range result {
		switch {
		case m.Role == "tool":
			result[i].Role = "user"
			format := fmt.Sprintf("[tool result for call %s]:\n%s", m.ToolCallID, m.contentString())
			if m.Name != "" {
				format = fmt.Sprintf("[tool result for %q (call %s)]:\n%s", m.Name, m.ToolCallID, m.contentString())
			}
			result[i].Content = contentPtr(format)
			result[i].ToolCallID = ""

		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			var parts []string
			for _, tc := range m.ToolCalls {
				parts = append(parts, fmt.Sprintf("%s{\"name\":%q,\"arguments\":%s}%s",
					toolCallBegin, tc.Function.Name, tc.Function.Arguments, toolCallEnd))
			}
			result[i].Content = contentPtr(strings.Join(parts, "\n"))
			result[i].ToolCalls = nil
		}
	}
	return result
}

// ── Non-streaming handler ──

func (s *Server) nonStreamChatCompletion(w http.ResponseWriter, r *http.Request, jimmyReq *JimmyRequest, model string, hasTools bool) {
	logDebug("nonstream calling DoRequest model=%s msgs=%d", model, len(jimmyReq.Messages))
	body, err := s.upstream.DoRequest(r.Context(), jimmyReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, APIError{
			Error: APIErrorDetail{
				Message: fmt.Sprintf("Upstream error: %v", err),
				Type:    "upstream_error",
			},
		})
		return
	}
	defer body.Close()

	// Read full response with stats
	sr := NewStreamReader(body)
	var fullBuf strings.Builder

	for {
		chunk, done, err := sr.ReadChunk()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, APIError{
				Error: APIErrorDetail{
					Message: fmt.Sprintf("Upstream read error: %v", err),
					Type:    "upstream_error",
				},
			})
			return
		}
		if chunk != nil {
			fullBuf.Write(chunk)
		}
		if done {
			break
		}
	}

	rawContent := strings.TrimSpace(fullBuf.String())
	chatID := generateID()
	created := nowUnix()

	// Extract token stats
	stats := sr.ExtractStats()
	usage := Usage{TotalTokens: 0}
	if stats != nil {
		usage.TotalTokens = stats.TotalTokens
	}

	logDebug("upstream raw: chat=%s len=%d has_stats=%v", chatID, len(rawContent), stats != nil)

	// Check for tool calls
	if hasTools && HasToolCalls(rawContent) {
		if parsed := FindToolCalls(rawContent); len(parsed) > 0 {
			toolCalls := convertToolCalls(parsed)
			finish := "tool_calls"
			resp := ChatCompletionResponse{
				ID: chatID, Object: "chat.completion", Created: created, Model: model,
				Choices: []Choice{{
					Index: 0,
					Message: ChatMessage{
						Role:      "assistant",
						Content:   nil, // null in JSON
						ToolCalls: toolCalls,
					},
					FinishReason: &finish,
				}},
				Usage: usage,
			}
			logDebug("resp tool_calls chat=%s finish=tool_calls n=%d", chatID, len(toolCalls))
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}

	// Normal text response
	finish := "stop"
	if rawContent == "" {
		logWarn("empty rawContent, chat=%s model=%s stats=%+v", chatID, model, stats)
		rawContent = "..."
	}
	resp := ChatCompletionResponse{
		ID: chatID, Object: "chat.completion", Created: created, Model: model,
		Choices: []Choice{{
			Index: 0,
			Message: ChatMessage{
				Role:    "assistant",
				Content: contentPtr(rawContent),
			},
			FinishReason: &finish,
		}},
		Usage: usage,
	}
	logDebug("resp text chat=%s finish=stop content_len=%d preview=%q usage=%+v", chatID, len(rawContent), rawContent[:min(len(rawContent), 60)], usage)
	writeJSON(w, http.StatusOK, resp)
}

// ── Streaming handler (passthrough) ──

// sniffEmptyBody reads the first chunk to detect completely empty upstream responses.
// Returns a buffer with the peeked data, or nil if the body was empty (EOF immediately).
// When stats-only (no text content), logs the stats and also returns nil (retryable).
func sniffEmptyBody(sr *StreamReader) (*bytes.Buffer, error) {
	chunk, done, err := sr.ReadChunk()
	if err != nil {
		return nil, err
	}
	if done && len(chunk) == 0 {
		// Check if there are stats (upstream returned stats-only response)
		if json := sr.StatsJSON(); json != "" {
			logInfo("upstream stats: %s", json)
		}
		return nil, nil // completely empty
	}
	buf := &bytes.Buffer{}
	if chunk != nil {
		buf.Write(chunk)
	}
	return buf, nil
}

func (s *Server) streamChatCompletion(w http.ResponseWriter, r *http.Request, jimmyReq *JimmyRequest, model string, hasTools bool) {
	logDebug("stream calling DoRequest model=%s msgs=%d", model, len(jimmyReq.Messages))
	body, err := s.upstream.DoRequest(r.Context(), jimmyReq)
	if err != nil {
		logDebug("stream upstream err: %v", err)
		writeJSON(w, http.StatusBadGateway, APIError{
			Error: APIErrorDetail{
				Message: fmt.Sprintf("Upstream error: %v", err),
				Type:    "upstream_error",
			},
		})
		return
	}
	defer body.Close()

	// ── Peek: check for empty body BEFORE sending SSE headers ──
	sr := NewStreamReader(body)
	peekBuf, err := sniffEmptyBody(sr)
	if err != nil {
		logDebug("stream peek error: %v", err)
		writeJSON(w, http.StatusBadGateway, APIError{
			Error: APIErrorDetail{
				Message: fmt.Sprintf("Upstream read error: %v", err),
				Type:    "upstream_error",
			},
		})
		return
	}

	// Empty body from upstream
	if peekBuf == nil {
		logDebug("stream empty body from upstream model=%s", model)
		// Without content, send an empty [DONE] so AstrBot doesn't hang
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		logDebug("stream no flusher")
		writeJSON(w, http.StatusInternalServerError, APIError{
			Error: APIErrorDetail{
				Message: "Streaming not supported by this transport",
				Type:    "server_error",
			},
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	chatID := generateID()
	created := nowUnix()
	streamStart := time.Now()
	var totalBytes int64

	logDebug("stream started chat=%s", chatID)

	// ── Determine response type ──
	isToolCall := false

	if hasTools {
		isToolCall = bytes.HasPrefix(peekBuf.Bytes(), []byte(toolCallBegin))
	}

	if isToolCall {
		logDebug("stream tool_call mode chat=%s peek_len=%d", chatID, peekBuf.Len())

		// ── Tool call mode: buffer all, emit deltas ──
		var fullBuf bytes.Buffer
		fullBuf.Write(peekBuf.Bytes())

		for {
			chunk, done, err := sr.ReadChunk()
			if err != nil {
				logError("stream toolcall read error: %v", err)
				break
			}
			if chunk != nil {
				fullBuf.Write(chunk)
			}
			if done {
				break
			}
		}

		rawContent := strings.TrimSpace(fullBuf.String())
		totalBytes = int64(len(rawContent))
		logDebug("stream tool_call raw chat=%s len=%d", chatID, len(rawContent))

		if parsed := FindToolCalls(rawContent); len(parsed) > 0 {
			toolCalls := convertToolCalls(parsed)

			writeSSE(w, ChatCompletionChunk{
				ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
				Choices: []ChunkChoice{{Delta: Delta{Role: "assistant"}}},
			})
			flusher.Flush()

			for i, tc := range toolCalls {
				writeSSE(w, ChatCompletionChunk{
					ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
					Choices: []ChunkChoice{{Delta: Delta{ToolCalls: []ToolCallDelta{{
						Index: i, ID: tc.ID, Type: "function",
						Function: &ToolCallDeltaFunc{Name: tc.Function.Name},
					}}}}},
				})
				flusher.Flush()

				writeSSE(w, ChatCompletionChunk{
					ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
					Choices: []ChunkChoice{{Delta: Delta{ToolCalls: []ToolCallDelta{{
						Index: i,
						Function: &ToolCallDeltaFunc{Arguments: tc.Function.Arguments},
					}}}}},
				})
				flusher.Flush()
			}

			finish := "tool_calls"
			writeSSE(w, ChatCompletionChunk{
				ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
				Choices: []ChunkChoice{{Delta: Delta{}, FinishReason: &finish}},
			})
			fmt.Fprintf(w, "data: [DONE]\n\n")
			writeStatsSSE(w, streamStart, totalBytes)
			flusher.Flush()

			logDebug("stream done tool_call chat=%s n=%d elapsed=%v stats=%s", chatID, len(toolCalls), time.Since(streamStart), sr.StatsJSON())
			return
		}
		// Fallthrough: tool call parsing failed, treat as text
		logDebug("stream tool_call peek not parsed, fallback to text chat=%s", chatID)
	}

	// ── Text mode: true passthrough streaming ──
	logDebug("stream text mode chat=%s", chatID)

	// Role announcement
	writeSSE(w, ChatCompletionChunk{
		ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
		Choices: []ChunkChoice{{Delta: Delta{Role: "assistant"}}},
	})
	flusher.Flush()

	// Emit peek buffer (from the sniff phase if we were in hasTools mode)
	if peekBuf.Len() > 0 {
		peek := peekBuf.String()
		totalBytes += int64(len(peek))
		writeSSE(w, ChatCompletionChunk{
			ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []ChunkChoice{{Delta: Delta{Content: strPtr(peek)}}},
		})
		flusher.Flush()
		logDebug("stream text peek chat=%s len=%d preview=%q", chatID, len(peek), peek[:min(len(peek), 60)])
	}

	firstChunk := true
	for {
		chunk, done, err := sr.ReadChunk()
		if err != nil {
			logError("stream read error: %v", err)
			break
		}
		if chunk != nil && len(chunk) > 0 {
			totalBytes += int64(len(chunk))
			if firstChunk {
				logDebug("stream first text chunk chat=%s len=%d preview=%q", chatID, len(chunk), string(chunk[:min(len(chunk), 60)]))
				firstChunk = false
			}
			writeSSE(w, ChatCompletionChunk{
				ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
				Choices: []ChunkChoice{{Delta: Delta{Content: strPtr(string(chunk))}}},
			})
			flusher.Flush()
		}
		if done {
			break
		}
	}

	// Finish
	finish := "stop"
	writeSSE(w, ChatCompletionChunk{
		ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
		Choices: []ChunkChoice{{Delta: Delta{}, FinishReason: &finish}},
	})
	fmt.Fprintf(w, "data: [DONE]\n\n")
	writeStatsSSE(w, streamStart, totalBytes)
	flusher.Flush()

	logDebug("stream done text chat=%s total_bytes=%d elapsed=%v stats=%s", chatID, totalBytes, time.Since(streamStart), sr.StatsJSON())
}
