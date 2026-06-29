package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	upstream *UpstreamClient
	apiKey   string
	started  time.Time
}

func NewServer(upstream *UpstreamClient, apiKey string) *Server {
	return &Server{
		upstream: upstream,
		apiKey:   apiKey,
		started:  time.Now(),
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
		Data: []Model{
			{
				ID:      "llama3.1-8B",
				Object:  "model",
				Created: 1700000000,
				OwnedBy: "chatjimmy",
			},
		},
	})
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
		writeJSON(w, http.StatusBadRequest, APIError{
			Error: APIErrorDetail{
				Message: fmt.Sprintf("Invalid JSON: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	if len(req.Messages) == 0 {
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

	messages := req.Messages

	// ── Tool handling ──
	// hasTools controls whether we parse tool calls from the response.
	// tool_choice="none" disables tool calling even if tools are provided.
	hasTools := len(req.Tools) > 0
	if hasTools {
		if choice, ok := req.ToolChoice.(string); ok && choice == "none" {
			hasTools = false
		}
		// Always inject tool prompt (it handles tool_choice internally)
		if toolPrompt := BuildToolSystemPrompt(req.Tools, req.ToolChoice); toolPrompt != "" {
			messages = injectSystemMessage(messages, toolPrompt)
		}
	}
	// ─────────────────

	// Build upstream request
	jimmyReq := s.upstream.BuildJimmyRequestFromMessages(
		withToolResultsFormatted(messages), model,
	)

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
	body, err := s.upstream.DoRequest(jimmyReq)
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
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}

	// Normal text response
	finish := "stop"
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
	writeJSON(w, http.StatusOK, resp)
}

// ── Streaming handler (passthrough) ──

func (s *Server) streamChatCompletion(w http.ResponseWriter, r *http.Request, jimmyReq *JimmyRequest, model string, hasTools bool) {
	body, err := s.upstream.DoRequest(jimmyReq)
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

	flusher, ok := w.(http.Flusher)
	if !ok {
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
	sr := NewStreamReader(body)
	streamStart := time.Now()
	var totalBytes int64

	// ── Determine response type ──
	// Peek at the first few bytes. If starts with <tool_call>, buffer all and
	// emit as tool call deltas. Otherwise, true passthrough streaming.
	isToolCall := false
	var peekBuf bytes.Buffer

	if hasTools {
		for peekBuf.Len() < len(toolCallBegin) {
			chunk, done, err := sr.ReadChunk()
			if err != nil {
				log.Printf("stream sniff error: %v", err)
				break
			}
			if chunk != nil {
				peekBuf.Write(chunk)
			}
			if done {
				break
			}
		}
		isToolCall = bytes.HasPrefix(peekBuf.Bytes(), []byte(toolCallBegin))
	}

	if isToolCall {
		// ── Tool call mode: buffer all, emit deltas ──
		var fullBuf bytes.Buffer
		fullBuf.Write(peekBuf.Bytes())

		for {
			chunk, done, err := sr.ReadChunk()
			if err != nil {
				log.Printf("stream toolcall read error: %v", err)
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
		if parsed := FindToolCalls(rawContent); len(parsed) > 0 {
			toolCalls := convertToolCalls(parsed)

			// Role announcement
			writeSSE(w, ChatCompletionChunk{
				ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
				Choices: []ChunkChoice{{Delta: Delta{Role: "assistant"}}},
			})
			flusher.Flush()

			// Each tool call: name chunk → arguments chunk
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
			return
		}
		// Fallthrough: tool call parsing failed, treat as text
	}

	// ── Text mode: true passthrough streaming ──

	// Role announcement
	writeSSE(w, ChatCompletionChunk{
		ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
		Choices: []ChunkChoice{{Delta: Delta{Role: "assistant"}}},
	})
	flusher.Flush()

	// Emit peek buffer (from the sniff phase if we were in hasTools mode)
	if peekBuf.Len() > 0 {
		writeSSE(w, ChatCompletionChunk{
			ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []ChunkChoice{{Delta: Delta{Content: strPtr(peekBuf.String())}}},
		})
		flusher.Flush()
	}

	// Stream remaining chunks in real-time
	for {
		chunk, done, err := sr.ReadChunk()
		if err != nil {
			log.Printf("stream read error: %v", err)
			break
		}
		if chunk != nil && len(chunk) > 0 {
			totalBytes += int64(len(chunk))
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
}
