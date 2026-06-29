package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

// ── OpenAI-compatible request types ──

type ChatCompletionRequest struct {
	Model       string           `json:"model"`
	Messages    []ChatMessage    `json:"messages"`
	Stream      bool             `json:"stream"`
	Temperature float64          `json:"temperature,omitempty"`
	TopP        float64          `json:"top_p,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	ToolChoice  any              `json:"tool_choice,omitempty"`
}

type ChatMessage struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"` // nil → null in JSON (for tool calls)
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name      string     `json:"name,omitempty"` // for role:tool
}

// ── Tool definition (in request) ──

type ToolDefinition struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ── Response types (non-streaming) ──

type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason *string     `json:"finish_reason"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // "function"
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ── SSE chunk types (streaming) ──

type ChatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
}

type ChunkChoice struct {
	Index        int        `json:"index"`
	Delta        Delta      `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type Delta struct {
	Role      string          `json:"role,omitempty"`
	Content   *string         `json:"content,omitempty"` // nil → field omitted
	ToolCalls []ToolCallDelta `json:"tool_calls,omitempty"`
}

type ToolCallDelta struct {
	Index    int               `json:"index"`
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type,omitempty"`
	Function *ToolCallDeltaFunc `json:"function,omitempty"`
}

type ToolCallDeltaFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ── chatjimmy.ai request types ──

type JimmyRequest struct {
	Messages    []ChatMessage `json:"messages"`
	ChatOptions JimmyOptions  `json:"chatOptions"`
	Attachment  *string       `json:"attachment"`
}

type JimmyOptions struct {
	SelectedModel string `json:"selectedModel"`
	SystemPrompt  string `json:"systemPrompt"`
	TopK          int    `json:"topK"`
}

// ── API error types ──

type APIError struct {
	Error APIErrorDetail `json:"error"`
}

type APIErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

type ModelsResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ── String pointer helper ──

func strPtr(s string) *string { return &s }

func safeStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ── JSON/SSE helpers ──

var jsonBufPool sync.Pool

func init() {
	jsonBufPool = sync.Pool{New: func() any { return make([]byte, 0, 4096) }}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, `{"error":{"message":"internal error"}}`, http.StatusInternalServerError)
	}
}

func writeSSE(w io.Writer, v any) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// ── ID generation ──

var (
	idRNG   = rand.New(rand.NewSource(time.Now().UnixNano()))
	idRNGMu sync.Mutex
)

func generateID() string {
	idRNGMu.Lock()
	n := idRNG.Uint64()
	idRNGMu.Unlock()
	return fmt.Sprintf("chatcmpl-%016x", n)
}

func generateToolCallID() string {
	idRNGMu.Lock()
	n := idRNG.Uint64()
	idRNGMu.Unlock()
	return fmt.Sprintf("call_%016x", n)
}

func nowUnix() int64 {
	return time.Now().Unix()
}
